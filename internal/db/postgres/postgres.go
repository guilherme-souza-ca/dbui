// Package postgres implements db.Session on top of pgx.
//
// PostgreSQL connections are bound to a single database, so a Session keeps
// one handle per database that has been used: a small pool for catalog
// queries and a dedicated connection for the SQL editor, so that session
// state (SET, BEGIN ... COMMIT across executions) behaves like psql.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/sqlsplit"
)

const (
	connectTimeout   = 10 * time.Second
	sqlStateReadOnly = "25006" // read_only_sql_transaction
)

// Session is a db.Session for one PostgreSQL instance.
type Session struct {
	conn     config.Connection
	password string

	mu      sync.Mutex
	handles map[string]*handle
}

type handle struct {
	pool *pgxpool.Pool

	execMu sync.Mutex // serializes editor executions
	exec   *pgx.Conn  // lazily opened, reopened if broken
}

var _ db.Session = (*Session)(nil)

// Open connects to the connection's default database and verifies it.
func Open(ctx context.Context, conn config.Connection, password string) (*Session, error) {
	s := &Session{conn: conn, password: password, handles: map[string]*handle{}}
	h, err := s.handle(ctx, conn.DefaultDatabase())
	if err != nil {
		return nil, err
	}
	if err := h.pool.Ping(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// connConfig builds a pgx config for the given database.
func (s *Session) connConfig(database string) (*pgx.ConnConfig, error) {
	u := url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(s.conn.Host, strconv.Itoa(s.conn.Port)),
		Path:   "/" + database,
	}
	if s.password != "" {
		u.User = url.UserPassword(s.conn.User, s.password)
	} else {
		u.User = url.User(s.conn.User)
	}
	q := url.Values{}
	if s.conn.SSLMode != "" {
		q.Set("sslmode", s.conn.SSLMode)
	}
	q.Set("application_name", "dbui")
	q.Set("connect_timeout", strconv.Itoa(int(connectTimeout.Seconds())))
	u.RawQuery = q.Encode()

	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, err
	}
	if s.conn.ReadOnly {
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}
	// On context cancellation ask the server to cancel the running query
	// instead of tearing down the connection (pgx's default).
	cfg.BuildContextWatcherHandler = func(pc *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn:               pc,
			CancelRequestDelay: 0,
			DeadlineDelay:      5 * time.Second,
		}
	}
	return cfg, nil
}

func (s *Session) handle(ctx context.Context, database string) (*handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handles[database]; ok {
		return h, nil
	}
	cc, err := s.connConfig(database)
	if err != nil {
		return nil, err
	}
	pcfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}
	pcfg.ConnConfig = cc
	pcfg.MaxConns = 2
	pcfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, err
	}
	h := &handle{pool: pool}
	s.handles[database] = h
	return h, nil
}

// Close releases every connection of the session.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, h := range s.handles {
		h.execMu.Lock()
		if h.exec != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = h.exec.Close(ctx)
			cancel()
		}
		h.execMu.Unlock()
		h.pool.Close()
		delete(s.handles, name)
	}
}

// ListDatabases returns the databases that accept connections.
func (s *Session) ListDatabases(ctx context.Context) ([]string, error) {
	return s.strings(ctx, s.conn.DefaultDatabase(),
		`SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname`)
}

// ListSchemas returns user schemas (system and toast schemas are hidden).
func (s *Session) ListSchemas(ctx context.Context, database string) ([]string, error) {
	return s.strings(ctx, database, `
		SELECT nspname FROM pg_namespace
		WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
		ORDER BY nspname`)
}

// ListTables returns tables, views and similar relations of a schema.
func (s *Session) ListTables(ctx context.Context, database, schema string) ([]db.Table, error) {
	h, err := s.handle(ctx, database)
	if err != nil {
		return nil, err
	}
	rows, err := h.pool.Query(ctx, `
		SELECT c.relname, c.relkind::text
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		ORDER BY c.relname`, schema)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.Table, error) {
		var t db.Table
		var kind string
		err := row.Scan(&t.Name, &kind)
		t.Kind = relKind(kind)
		return t, err
	})
}

func relKind(k string) db.TableKind {
	switch k {
	case "p":
		return db.KindPartitionedTable
	case "v":
		return db.KindView
	case "m":
		return db.KindMaterializedView
	case "f":
		return db.KindForeignTable
	default:
		return db.KindTable
	}
}

func (s *Session) strings(ctx context.Context, database, sql string) ([]string, error) {
	h, err := s.handle(ctx, database)
	if err != nil {
		return nil, err
	}
	rows, err := h.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Exec runs sql (one or more statements, simple protocol) on the database's
// dedicated editor connection. With multiple statements the last result that
// returned rows is reported, otherwise the last command tag.
func (s *Session) Exec(ctx context.Context, database, sql string, opts db.ExecOptions) (*db.Result, error) {
	h, err := s.handle(ctx, database)
	if err != nil {
		return nil, err
	}
	h.execMu.Lock()
	defer h.execMu.Unlock()

	if h.exec == nil || h.exec.IsClosed() {
		cc, err := s.connConfig(database)
		if err != nil {
			return nil, err
		}
		if h.exec, err = pgx.ConnectConfig(ctx, cc); err != nil {
			return nil, err
		}
	}
	conn := h.exec

	start := time.Now()
	var res *db.Result
	if opts.Guard && !s.conn.ReadOnly {
		res, err = guarded(ctx, conn, sql, opts.MaxRows)
	} else {
		res, err = run(ctx, conn, sql, opts.MaxRows)
	}
	if res != nil {
		res.Duration = time.Since(start)
	}
	return res, err
}

// guarded runs sql inside a read-only (sub)transaction. Reads succeed as
// normal; writes are rolled back and reported as ErrWriteNeedsConfirmation.
func guarded(ctx context.Context, conn *pgx.Conn, sql string, maxRows int) (*db.Result, error) {
	// Transaction control cannot be probed inside our own transaction (the
	// user's BEGIN would be swallowed by the probe). It never writes by
	// itself, so it runs directly; mixed with other statements we cannot
	// probe safely and ask for confirmation instead.
	switch txControl(sql) {
	case txAll:
		return run(ctx, conn, sql, maxRows)
	case txMixed:
		return nil, db.ErrWriteNeedsConfirmation
	}

	pc := conn.PgConn()
	switch pc.TxStatus() {
	case 'E':
		// The user's transaction already failed; nothing can write.
		return run(ctx, conn, sql, maxRows)

	case 'T':
		// Inside the user's own transaction: probe in a savepoint so we can
		// undo the read-only flag and any failure without losing their work.
		if err := simple(ctx, pc, "SAVEPOINT dbui_guard; SET LOCAL transaction_read_only = on"); err != nil {
			return nil, err
		}
		res, err := run(ctx, conn, sql, maxRows)
		if pc.TxStatus() != 'I' {
			if rbErr := simple(ctx, pc, "ROLLBACK TO SAVEPOINT dbui_guard; RELEASE SAVEPOINT dbui_guard"); rbErr != nil && err == nil {
				err = rbErr
			}
		}
		return res, writeErr(err)

	default:
		if err := simple(ctx, pc, "BEGIN READ ONLY"); err != nil {
			return nil, err
		}
		res, err := run(ctx, conn, sql, maxRows)
		if pc.TxStatus() != 'I' {
			// COMMIT keeps harmless session changes such as SET; in a failed
			// transaction it rolls back.
			if cErr := simple(ctx, pc, "COMMIT"); cErr != nil && err == nil {
				err = cErr
			}
		}
		return res, writeErr(err)
	}
}

type txKind int

const (
	txNone txKind = iota
	txAll
	txMixed
)

func txControl(sql string) txKind {
	stmts := sqlsplit.Split(sql)
	n := 0
	for _, st := range stmts {
		switch sqlsplit.Keyword(st.Text) {
		case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT", "SAVEPOINT", "RELEASE":
			n++
		}
	}
	switch {
	case n == 0:
		return txNone
	case n == len(stmts):
		return txAll
	default:
		return txMixed
	}
}

func writeErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlStateReadOnly {
		return db.ErrWriteNeedsConfirmation
	}
	return err
}

func simple(ctx context.Context, pc *pgconn.PgConn, sql string) error {
	_, err := pc.Exec(ctx, sql).ReadAll()
	return err
}

func run(ctx context.Context, conn *pgx.Conn, sql string, maxRows int) (*db.Result, error) {
	mrr := conn.PgConn().Exec(ctx, sql)
	var final *db.Result
	for mrr.NextResult() {
		rr := mrr.ResultReader()
		cur := &db.Result{}
		for _, fd := range rr.FieldDescriptions() {
			cur.Columns = append(cur.Columns, db.Column{Name: fd.Name, Type: typeName(conn, fd.DataTypeOID)})
		}
		for rr.NextRow() {
			cur.TotalRows++
			if maxRows > 0 && len(cur.Rows) >= maxRows {
				continue
			}
			vals := rr.Values()
			row := make([]db.Cell, len(vals))
			for i, v := range vals {
				if v == nil {
					row[i] = db.Cell{Null: true}
				} else {
					row[i] = db.Cell{Value: string(v)}
				}
			}
			cur.Rows = append(cur.Rows, row)
		}
		tag, err := rr.Close()
		if err != nil {
			_ = mrr.Close()
			return nil, err
		}
		cur.CommandTag = tag.String()
		cur.Truncated = cur.TotalRows > len(cur.Rows)
		if final == nil || !final.HasRows() || cur.HasRows() {
			final = cur
		}
	}
	if err := mrr.Close(); err != nil {
		return nil, err
	}
	if final == nil {
		final = &db.Result{CommandTag: "EMPTY"}
	}
	return final, nil
}

func typeName(conn *pgx.Conn, oid uint32) string {
	if t, ok := conn.TypeMap().TypeForOID(oid); ok {
		return t.Name
	}
	return fmt.Sprintf("oid:%d", oid)
}

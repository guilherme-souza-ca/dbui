package postgres

import (
	"context"
	"errors"
	"flag"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/db"
)

// Integration tests need Docker. On Colima export DOCKER_HOST and
// TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE (see README). Skipped with -short.

var testConn config.Connection

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() || os.Getenv("DBUI_SKIP_INTEGRATION") != "" {
		os.Exit(0)
	}
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithUsername("dbui"),
		tcpostgres.WithPassword("secret"),
		tcpostgres.WithDatabase("app"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	host, _ := ctr.Host(ctx)
	port, _ := ctr.MappedPort(ctx, "5432/tcp")
	p, _ := strconv.Atoi(port.Port())
	testConn = config.Connection{Name: "test", Host: host, Port: p, User: "dbui", Database: "app", SSLMode: "disable"}

	setup, err := Open(ctx, testConn, "secret")
	if err != nil {
		panic(err)
	}
	for _, sql := range []string{
		`CREATE DATABASE other`,
		`CREATE SCHEMA sales`,
		`CREATE TABLE sales.orders (id int PRIMARY KEY, note text)`,
		`CREATE VIEW sales.big_orders AS SELECT * FROM sales.orders WHERE id > 10`,
		`INSERT INTO sales.orders SELECT g, 'n' || g FROM generate_series(1, 50) g`,
		`INSERT INTO sales.orders VALUES (100, NULL)`,
	} {
		if _, err := setup.Exec(ctx, "app", sql, db.ExecOptions{}); err != nil {
			panic(err)
		}
	}
	setup.Close()

	code := m.Run()
	_ = testcontainers.TerminateContainer(ctr)
	os.Exit(code)
}

func open(t *testing.T, mut func(*config.Connection)) *Session {
	t.Helper()
	c := testConn
	if mut != nil {
		mut(&c)
	}
	s, err := Open(context.Background(), c, "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestOpenWrongPassword(t *testing.T) {
	_, err := Open(context.Background(), testConn, "wrong")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "28P01" {
		t.Fatalf("expected auth failure, got %v", err)
	}
}

func TestOpenUnreachable(t *testing.T) {
	c := testConn
	c.Host = "127.0.0.1"
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	c.Port = l.Addr().(*net.TCPAddr).Port
	l.Close()
	if _, err := Open(context.Background(), c, "secret"); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestCatalog(t *testing.T) {
	s := open(t, nil)
	ctx := context.Background()

	dbs, err := s.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dbs, "app") || !contains(dbs, "other") || contains(dbs, "template0") {
		t.Fatalf("databases = %v", dbs)
	}

	schemas, err := s.ListSchemas(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(schemas, "public") || !contains(schemas, "sales") || contains(schemas, "pg_catalog") {
		t.Fatalf("schemas = %v", schemas)
	}

	tables, err := s.ListTables(ctx, "app", "sales")
	if err != nil {
		t.Fatal(err)
	}
	want := []db.Table{{Name: "big_orders", Kind: db.KindView}, {Name: "orders", Kind: db.KindTable}}
	if len(tables) != 2 || tables[0] != want[0] || tables[1] != want[1] {
		t.Fatalf("tables = %v", tables)
	}

	// A second database gets its own handle.
	if _, err := s.ListSchemas(ctx, "other"); err != nil {
		t.Fatal(err)
	}
}

func TestExecRowsAndCommandTag(t *testing.T) {
	s := open(t, nil)
	ctx := context.Background()

	res, err := s.Exec(ctx, "app", "SELECT id, note FROM sales.orders WHERE id IN (1, 100) ORDER BY id", db.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasRows() || len(res.Rows) != 2 {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.Columns[0] != (db.Column{Name: "id", Type: "int4"}) || res.Columns[1].Type != "text" {
		t.Fatalf("columns = %+v", res.Columns)
	}
	if res.Rows[0][1] != (db.Cell{Value: "n1"}) || !res.Rows[1][1].Null {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if res.CommandTag != "SELECT 2" || res.Duration <= 0 {
		t.Fatalf("tag=%q duration=%v", res.CommandTag, res.Duration)
	}

	res, err = s.Exec(ctx, "app", "UPDATE sales.orders SET note = note WHERE id <= 3", db.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.HasRows() || res.CommandTag != "UPDATE 3" {
		t.Fatalf("unexpected result %+v", res)
	}

	res, err = s.Exec(ctx, "app", "UPDATE sales.orders SET note = note WHERE id = 1 RETURNING id", db.ExecOptions{})
	if err != nil || !res.HasRows() || len(res.Rows) != 1 {
		t.Fatalf("RETURNING should be tabular: %+v %v", res, err)
	}
}

func TestExecMultipleStatementsReportsLastRows(t *testing.T) {
	s := open(t, nil)
	res, err := s.Exec(context.Background(), "app", "SELECT 1 AS a; SELECT 2 AS b; SET search_path = sales", db.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Columns) != 1 || res.Columns[0].Name != "b" {
		t.Fatalf("expected the last tabular result, got %+v", res)
	}
}

func TestExecTruncates(t *testing.T) {
	s := open(t, nil)
	res, err := s.Exec(context.Background(), "app", "SELECT * FROM sales.orders", db.ExecOptions{MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 10 || res.TotalRows != 51 || !res.Truncated {
		t.Fatalf("rows=%d total=%d truncated=%v", len(res.Rows), res.TotalRows, res.Truncated)
	}
}

func TestExecErrorKeepsPosition(t *testing.T) {
	s := open(t, nil)
	_, err := s.Exec(context.Background(), "app", "SELECT nope FROM sales.orders", db.ExecOptions{})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42703" || pgErr.Position != 8 {
		t.Fatalf("got %#v", err)
	}
	// The connection is still usable after an error.
	if _, err := s.Exec(context.Background(), "app", "SELECT 1", db.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStatePersistsAcrossExecutions(t *testing.T) {
	s := open(t, nil)
	ctx := context.Background()
	if _, err := s.Exec(ctx, "app", "SET search_path = sales", db.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "app", "SELECT count(*) FROM orders", db.ExecOptions{}); err != nil {
		t.Fatalf("search_path should persist on the editor connection: %v", err)
	}
}

func TestCancel(t *testing.T) {
	s := open(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := s.Exec(ctx, "app", "SELECT pg_sleep(30)", db.ExecOptions{})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel took too long")
	}
	if _, err := s.Exec(context.Background(), "app", "SELECT 1", db.ExecOptions{}); err != nil {
		t.Fatalf("session should recover after cancel: %v", err)
	}
}

func TestReadOnlyConnection(t *testing.T) {
	s := open(t, func(c *config.Connection) { c.ReadOnly = true })
	_, err := s.Exec(context.Background(), "app", "DELETE FROM sales.orders WHERE id = 1", db.ExecOptions{})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("expected read-only error, got %v", err)
	}
}

func TestGuard(t *testing.T) {
	s := open(t, nil)
	ctx := context.Background()
	guard := db.ExecOptions{Guard: true}

	res, err := s.Exec(ctx, "app", "SELECT count(*) FROM sales.orders", guard)
	if err != nil || res.Rows[0][0].Value != "51" {
		t.Fatalf("reads must pass the guard: %+v %v", res, err)
	}

	_, err = s.Exec(ctx, "app", "UPDATE sales.orders SET note = 'x' WHERE id = 1", guard)
	if !errors.Is(err, db.ErrWriteNeedsConfirmation) {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	res, _ = s.Exec(ctx, "app", "SELECT note FROM sales.orders WHERE id = 1", guard)
	if res.Rows[0][0].Value == "x" {
		t.Fatal("guarded write must not be applied")
	}

	// Confirmed run.
	if _, err := s.Exec(ctx, "app", "UPDATE sales.orders SET note = 'x' WHERE id = 1", db.ExecOptions{}); err != nil {
		t.Fatal(err)
	}

	// SET survives the guard (the probe commits a read-only transaction).
	if _, err := s.Exec(ctx, "app", "SET search_path = sales", guard); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "app", "SELECT 1 FROM orders LIMIT 1", guard); err != nil {
		t.Fatalf("search_path lost after guarded SET: %v", err)
	}
}

func TestGuardInsideUserTransaction(t *testing.T) {
	s := open(t, nil)
	ctx := context.Background()
	guard := db.ExecOptions{Guard: true}

	if _, err := s.Exec(ctx, "app", "BEGIN", guard); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "app", "UPDATE sales.orders SET note = 'tx' WHERE id = 2", guard); !errors.Is(err, db.ErrWriteNeedsConfirmation) {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	// The user's transaction must still be alive and writable.
	if _, err := s.Exec(ctx, "app", "UPDATE sales.orders SET note = 'tx' WHERE id = 2", db.ExecOptions{}); err != nil {
		t.Fatalf("transaction should be writable after a guarded probe: %v", err)
	}
	res, err := s.Exec(ctx, "app", "SELECT note FROM sales.orders WHERE id = 2", guard)
	if err != nil || res.Rows[0][0].Value != "tx" {
		t.Fatalf("read inside transaction: %+v %v", res, err)
	}
	if _, err := s.Exec(ctx, "app", "ROLLBACK", guard); err != nil {
		t.Fatal(err)
	}
	res, _ = s.Exec(ctx, "app", "SELECT note FROM sales.orders WHERE id = 2", guard)
	if res.Rows[0][0].Value != "n2" {
		t.Fatalf("rollback should undo the write, got %q", res.Rows[0][0].Value)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

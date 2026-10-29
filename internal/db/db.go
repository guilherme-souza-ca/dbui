// Package db defines the database operations the TUI depends on. Engine
// specific implementations live in sub-packages (currently only postgres).
package db

import (
	"context"
	"errors"
	"time"
)

// ErrWriteNeedsConfirmation is returned by Exec in guarded mode when the
// statement tried to write. The caller should ask the user and re-run it
// with Guard disabled.
var ErrWriteNeedsConfirmation = errors.New("statement writes to a production connection")

// TableKind distinguishes relations shown in the explorer.
type TableKind string

const (
	KindTable            TableKind = "table"
	KindPartitionedTable TableKind = "partitioned table"
	KindView             TableKind = "view"
	KindMaterializedView TableKind = "materialized view"
	KindForeignTable     TableKind = "foreign table"
)

// Table is a relation inside a schema.
type Table struct {
	Name string
	Kind TableKind
}

// Column describes a result column.
type Column struct {
	Name string
	Type string
}

// Cell is a value in text form. Null distinguishes SQL NULL from "".
type Cell struct {
	Value string
	Null  bool
}

// Result is the outcome of executing SQL. When Columns is empty the statement
// did not return rows and CommandTag describes what happened.
type Result struct {
	Columns    []Column
	Rows       [][]Cell
	TotalRows  int  // rows returned by the server, may exceed len(Rows)
	Truncated  bool // true when TotalRows > len(Rows)
	CommandTag string
	Duration   time.Duration
}

// HasRows reports whether the result is tabular.
func (r *Result) HasRows() bool { return len(r.Columns) > 0 }

// ExecOptions tunes Exec.
type ExecOptions struct {
	// MaxRows caps how many rows are kept in memory. Zero means unlimited.
	MaxRows int
	// Guard runs the statement read-only first and returns
	// ErrWriteNeedsConfirmation if it attempts to write.
	Guard bool
}

// Session is an open connection to one instance.
type Session interface {
	ListDatabases(ctx context.Context) ([]string, error)
	ListSchemas(ctx context.Context, database string) ([]string, error)
	ListTables(ctx context.Context, database, schema string) ([]Table, error)
	Exec(ctx context.Context, database, sql string, opts ExecOptions) (*Result, error)
	Close()
}

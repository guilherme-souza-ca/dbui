// Package tuitest has helpers for testing Bubble Tea models without a
// terminal: a fake db.Session and a synchronous command runner.
package tuitest

import (
	"context"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/secret"
)

var secretNotFound = secret.ErrNotFound

// Collect runs cmd and returns the messages it produces, expanding batches.
// Commands that take longer than wait (timers, ticks) are dropped.
func Collect(cmd tea.Cmd, wait time.Duration) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, Collect(c, wait)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(wait):
		return nil
	}
}

// ExecCall records a call to FakeSession.Exec.
type ExecCall struct {
	Database, SQL string
	Opts          db.ExecOptions
}

// FakeSession is an in-memory db.Session.
type FakeSession struct {
	mu        sync.Mutex
	Databases []string
	Schemas   map[string][]string   // database -> schemas
	Tables    map[string][]db.Table // "database.schema" -> tables
	// ExecFn computes results; by default it returns a one-row result.
	ExecFn func(call ExecCall) (*db.Result, error)
	Calls  []ExecCall
	Closed bool
}

func (f *FakeSession) ListDatabases(context.Context) ([]string, error) { return f.Databases, nil }
func (f *FakeSession) ListSchemas(_ context.Context, database string) ([]string, error) {
	return f.Schemas[database], nil
}
func (f *FakeSession) ListTables(_ context.Context, database, schema string) ([]db.Table, error) {
	return f.Tables[database+"."+schema], nil
}

func (f *FakeSession) Exec(_ context.Context, database, sql string, opts db.ExecOptions) (*db.Result, error) {
	call := ExecCall{Database: database, SQL: sql, Opts: opts}
	f.mu.Lock()
	f.Calls = append(f.Calls, call)
	fn := f.ExecFn
	f.mu.Unlock()
	if fn != nil {
		return fn(call)
	}
	if opts.Guard && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "SELECT") {
		return nil, db.ErrWriteNeedsConfirmation
	}
	return &db.Result{
		Columns:    []db.Column{{Name: "id", Type: "int4"}, {Name: "name", Type: "text"}},
		Rows:       [][]db.Cell{{{Value: "1"}, {Value: "alice"}}, {{Value: "2"}, {Null: true}}},
		TotalRows:  2,
		CommandTag: "SELECT 2",
		Duration:   3 * time.Millisecond,
	}, nil
}

func (f *FakeSession) Close() {
	f.mu.Lock()
	f.Closed = true
	f.mu.Unlock()
}

// LastCall returns the most recent Exec call.
func (f *FakeSession) LastCall() (ExecCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Calls) == 0 {
		return ExecCall{}, false
	}
	return f.Calls[len(f.Calls)-1], true
}

// NewFake returns a fake with one database "app", schema "public" and a
// table "users".
func NewFake() *FakeSession {
	return &FakeSession{
		Databases: []string{"app", "postgres"},
		Schemas:   map[string][]string{"app": {"public"}},
		Tables:    map[string][]db.Table{"app.public": {{Name: "users", Kind: db.KindTable}, {Name: "active_users", Kind: db.KindView}}},
	}
}

// Key builds a key press message for a key name such as "enter", "ctrl+e"
// or a single character.
func Key(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "f5":
		return tea.KeyPressMsg{Code: tea.KeyF5}
	}
	if strings.HasPrefix(name, "ctrl+") {
		r := []rune(name[5:])[0]
		return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
	}
	if strings.HasPrefix(name, "alt+") {
		r := []rune(name[4:])[0]
		return tea.KeyPressMsg{Code: r, Mod: tea.ModAlt}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: name}
}

// FakeSecrets is an in-memory secret.Store.
type FakeSecrets struct {
	mu sync.Mutex
	M  map[string]string
}

func (f *FakeSecrets) Get(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pw, ok := f.M[name]
	if !ok {
		return "", secretNotFound
	}
	return pw, nil
}

func (f *FakeSecrets) Set(name, pw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.M == nil {
		f.M = map[string]string{}
	}
	f.M[name] = pw
	return nil
}

func (f *FakeSecrets) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.M, name)
	return nil
}

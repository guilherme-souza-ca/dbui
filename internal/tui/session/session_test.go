package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/tui/tuitest"
)

const wait = 50 * time.Millisecond

// pump feeds msg to the model and keeps applying the messages produced by
// the resulting commands. Messages the session does not own (confirm
// requests) are returned to the caller.
func pump(t *testing.T, m Model, msg tea.Msg) (Model, []tea.Msg) {
	t.Helper()
	var external []tea.Msg
	queue := []tea.Msg{msg}
	for i := 0; len(queue) > 0; i++ {
		if i > 100 {
			t.Fatal("message loop did not settle")
		}
		next := queue[0]
		queue = queue[1:]
		if _, ok := next.(RequestConfirmMsg); ok {
			external = append(external, next)
			continue
		}
		var cmd tea.Cmd
		m, cmd = m.Update(next)
		queue = append(queue, tuitest.Collect(cmd, wait)...)
	}
	return m, external
}

func newTab(t *testing.T, conn config.Connection) (Model, *tuitest.FakeSession) {
	t.Helper()
	fake := tuitest.NewFake()
	m := New(1, conn, fake)
	m.SetSize(120, 40)
	for _, msg := range tuitest.Collect(m.Init(), wait) {
		m, _ = pump(t, m, msg)
	}
	return m, fake
}

func typeText(t *testing.T, m Model, s string) Model {
	for _, r := range s {
		m, _ = pump(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

var local = config.Connection{Name: "local", Host: "localhost", Port: 5432, User: "postgres"}

func TestExecuteStatementUnderCursor(t *testing.T) {
	m, fake := newTab(t, local)
	m, _ = pump(t, m, tuitest.Key("tab")) // explorer -> editor
	if !m.EditorFocused() {
		t.Fatal("editor should be focused")
	}
	m = typeText(t, m, "select 1;\nselect 2")
	m, _ = pump(t, m, tuitest.Key("ctrl+e"))

	call, ok := fake.LastCall()
	if !ok || call.SQL != "select 2" || call.Database != "postgres" || call.Opts.Guard {
		t.Fatalf("unexpected call %+v", call)
	}
	if call.Opts.MaxRows != DefaultMaxRows {
		t.Fatalf("MaxRows = %d", call.Opts.MaxRows)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"alice", "∅", "Results · 2 rows"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestOpenTableFromExplorer(t *testing.T) {
	m, fake := newTab(t, local)
	// databases: app, postgres. Expand app -> public -> users.
	m, _ = pump(t, m, tuitest.Key("enter")) // expand + use "app"
	if m.Database() != "app" {
		t.Fatalf("database = %q", m.Database())
	}
	m, _ = pump(t, m, tuitest.Key("down")) // public
	m, _ = pump(t, m, tuitest.Key("right"))
	m, _ = pump(t, m, tuitest.Key("down")) // users (fake keeps insertion order)
	m, _ = pump(t, m, tuitest.Key("enter"))

	call, _ := fake.LastCall()
	if call.SQL != `SELECT * FROM "public"."users" LIMIT 200` || call.Database != "app" {
		t.Fatalf("unexpected call %+v", call)
	}
	if !strings.Contains(m.editor.Value(), `SELECT * FROM "public"."users" LIMIT 200;`) {
		t.Fatalf("editor = %q", m.editor.Value())
	}
	if m.focus != focusResults {
		t.Fatal("results should be focused after opening a table")
	}
}

func TestProductionWriteNeedsConfirmation(t *testing.T) {
	prod := local
	prod.Production = true
	m, fake := newTab(t, prod)
	m, _ = pump(t, m, tuitest.Key("tab"))
	m = typeText(t, m, "update t set x = 1")

	m, external := pump(t, m, tuitest.Key("ctrl+e"))
	if len(external) != 1 {
		t.Fatalf("expected a confirmation request, got %v", external)
	}
	req := external[0].(RequestConfirmMsg)
	if !strings.Contains(req.Prompt, "PRODUCTION") || !strings.Contains(req.Prompt, "update t set x = 1") {
		t.Fatalf("prompt = %q", req.Prompt)
	}
	if call, _ := fake.LastCall(); !call.Opts.Guard {
		t.Fatal("first run must be guarded")
	}

	// Declining does not run anything else.
	n := len(fake.Calls)
	m, _ = pump(t, m, req.OnNo)
	if len(fake.Calls) != n || !strings.Contains(ansi.Strip(m.View()), "not executed") {
		t.Fatal("declined statement must not run")
	}

	// Confirming re-runs unguarded.
	m, _ = pump(t, m, req.OnYes)
	call, _ := fake.LastCall()
	if call.Opts.Guard || call.SQL != "update t set x = 1" {
		t.Fatalf("confirmed call = %+v", call)
	}
}

func TestReadOnlyConnectionIsNotGuarded(t *testing.T) {
	ro := local
	ro.Production, ro.ReadOnly = true, true
	m, fake := newTab(t, ro)
	m, _ = pump(t, m, tuitest.Key("tab"))
	m = typeText(t, m, "select 1")
	m, _ = pump(t, m, tuitest.Key("ctrl+e"))
	if call, _ := fake.LastCall(); call.Opts.Guard {
		t.Fatal("read-only connections cannot write, no guard needed")
	}
	if !strings.Contains(ansi.Strip(m.View()), "read-only") {
		t.Fatal("editor title should flag read-only")
	}
}

func TestErrorIsShownWithCaret(t *testing.T) {
	m, fake := newTab(t, local)
	fake.ExecFn = func(tuitest.ExecCall) (*db.Result, error) {
		return nil, &pgconn.PgError{Severity: "ERROR", Code: "42703", Message: `column "nope" does not exist`, Position: 8}
	}
	m, _ = pump(t, m, tuitest.Key("tab"))
	m = typeText(t, m, "select 1;\nselect nope from t")
	m, _ = pump(t, m, tuitest.Key("ctrl+e"))

	view := ansi.Strip(m.View())
	if !strings.Contains(view, `ERROR 42703: column "nope" does not exist`) {
		t.Fatalf("error not shown:\n%s", view)
	}
	// The statement starts at rune offset 10; position 8 -> offset 17 = row 1, col 7.
	if row, col := m.editor.CursorPosition(); row != 1 || col != 7 {
		t.Fatalf("cursor at %d:%d, want 1:7", row, col)
	}
}

func TestFormatError(t *testing.T) {
	err := &pgconn.PgError{Severity: "ERROR", Code: "42601", Message: "syntax error", Position: 16, Hint: "check it"}
	got := FormatError(err, "select 1,\nfrom x")
	want := "ERROR 42601: syntax error\nLINE 2: from x\n             ^\nHINT: check it"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if FormatError(errors.New("boom"), "") != "ERROR: boom" {
		t.Fatal("plain errors should be prefixed")
	}
}

func TestStaleResultsAreIgnored(t *testing.T) {
	m, _ := newTab(t, local)
	m.req = 5
	m.running = true
	m, _ = pump(t, m, queryDoneMsg{ID: m.id, req: 4, res: &db.Result{CommandTag: "OLD"}})
	if !m.running {
		t.Fatal("a stale result must not finish the current query")
	}
}

func TestCancel(t *testing.T) {
	m, fake := newTab(t, local)
	release := make(chan struct{})
	fake.ExecFn = func(tuitest.ExecCall) (*db.Result, error) {
		<-release
		return nil, errors.New("canceled by server")
	}
	m, _ = pump(t, m, tuitest.Key("tab"))
	m = typeText(t, m, "select pg_sleep(10)")

	m2, cmd := m.Update(tuitest.Key("ctrl+e"))
	m = m2
	if !m.Running() {
		t.Fatal("query should be running")
	}
	m, _ = m.Update(tuitest.Key("ctrl+c"))
	close(release)
	for _, msg := range tuitest.Collect(cmd, time.Second) {
		m, _ = pump(t, m, msg)
	}
	if m.Running() || !strings.Contains(ansi.Strip(m.View()), "Query canceled") {
		t.Fatalf("expected canceled state:\n%s", ansi.Strip(m.View()))
	}
}

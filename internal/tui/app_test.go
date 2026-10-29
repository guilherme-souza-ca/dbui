package tui

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/tui/tuitest"
)

type harness struct {
	deps    Deps
	fake    *tuitest.FakeSession
	secrets *tuitest.FakeSecrets
	opened  []string // passwords used to open
}

func newHarness(t *testing.T, conns ...config.Connection) *harness {
	t.Helper()
	store, err := config.Load(filepath.Join(t.TempDir(), "connections.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		if err := store.Upsert("", c); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{fake: tuitest.NewFake(), secrets: &tuitest.FakeSecrets{}}
	h.deps = Deps{
		Store:   store,
		Secrets: h.secrets,
		Open: func(_ context.Context, c config.Connection, password string) (db.Session, error) {
			h.opened = append(h.opened, password)
			if c.Name == "locked" && password != "right" {
				return nil, &pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: "password authentication failed"}
			}
			return h.fake, nil
		},
	}
	return h
}

func waitFor(t *testing.T, tm *teatest.TestModel, texts ...string) {
	t.Helper()
	var seen []byte
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		seen = append(seen, b...)
		plain := []byte(ansi.Strip(string(seen)))
		for _, s := range texts {
			if !bytes.Contains(plain, []byte(s)) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
}

var localConn = config.Connection{Name: "local", Host: "localhost", Port: 5432, User: "postgres", Database: "app"}

func TestEndToEndBrowseTable(t *testing.T) {
	h := newHarness(t, localConn)
	tm := teatest.NewTestModel(t, New(h.deps, ""), teatest.WithInitialTermSize(140, 40))

	waitFor(t, tm, "Connections", "local")
	tm.Send(tuitest.Key("enter")) // connect
	waitFor(t, tm, "Explorer", "app")

	tm.Send(tuitest.Key("enter")) // expand app
	waitFor(t, tm, "public")
	tm.Send(tuitest.Key("down"))
	tm.Send(tuitest.Key("right")) // expand public
	waitFor(t, tm, "users")
	tm.Send(tuitest.Key("down"))
	tm.Send(tuitest.Key("enter")) // open users
	waitFor(t, tm, "alice", "2 rows")

	tm.Send(tuitest.Key("ctrl+c"))
	waitFor(t, tm, "Quit dbui?")
	tm.Send(tuitest.Key("y"))

	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)
	if len(final.tabs) != 1 || final.tabs[0].Database() != "app" {
		t.Fatalf("unexpected final state: %d tabs", len(final.tabs))
	}
	if !h.fake.Closed {
		t.Fatal("session should be closed on quit")
	}
	if call, _ := h.fake.LastCall(); call.SQL != `SELECT * FROM "public"."users" LIMIT 200` {
		t.Fatalf("last SQL = %q", call.SQL)
	}
}

func TestEndToEndPasswordPromptOnAuthFailure(t *testing.T) {
	locked := localConn
	locked.Name = "locked"
	h := newHarness(t, locked)
	tm := teatest.NewTestModel(t, New(h.deps, "locked"), teatest.WithInitialTermSize(140, 40))

	waitFor(t, tm, "Password for locked")
	tm.Type("right")
	tm.Send(tuitest.Key("enter"))
	waitFor(t, tm, "Explorer")

	if pw, _ := h.secrets.Get("locked"); pw != "right" {
		t.Fatalf("prompted password should be saved, got %q", pw)
	}
	tm.Send(tuitest.Key("ctrl+c"))
	tm.Send(tuitest.Key("ctrl+c")) // second ctrl+c quits without answering
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

func TestCreateConnectionThroughForm(t *testing.T) {
	h := newHarness(t)
	tm := teatest.NewTestModel(t, New(h.deps, ""), teatest.WithInitialTermSize(140, 50))

	waitFor(t, tm, "No saved connections")
	tm.Send(tuitest.Key("n"))
	waitFor(t, tm, "New connection")
	// Huh advances fields asynchronously, so pace the input.
	step := func(msgs ...tea.Msg) {
		for _, m := range msgs {
			tm.Send(m)
		}
		time.Sleep(40 * time.Millisecond)
	}
	typeStr := func(s string) {
		tm.Type(s)
		time.Sleep(40 * time.Millisecond)
	}
	enter := tuitest.Key("enter")
	typeStr("staging")
	step(enter) // name
	step(enter) // host (default)
	step(enter) // port (default)
	step(enter) // user (default)
	typeStr("pw")
	step(enter) // password
	step(enter) // database (default)
	waitFor(t, tm, "SSL mode")
	step(enter)                      // ssl mode
	step(enter)                      // read-only: no
	step(tuitest.Key("left"), enter) // production: yes
	waitFor(t, tm, "Saved staging")

	tm.Send(tuitest.Key("ctrl+c"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	reloaded, err := config.Load(h.deps.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	c, ok := reloaded.Get("staging")
	if !ok || c.Host != "localhost" || c.Port != 5432 || !c.Production || c.ReadOnly {
		t.Fatalf("saved connection = %+v (ok=%v)", c, ok)
	}
	if pw, _ := h.secrets.Get("staging"); pw != "pw" {
		t.Fatalf("password not stored, got %q", pw)
	}
}

var _ tea.Model = Model{}

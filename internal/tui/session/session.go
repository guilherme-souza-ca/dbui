// Package session is one connection tab: explorer, SQL editor and results.
package session

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/tui/editor"
	"github.com/guilhermesouza/dbui/internal/tui/explorer"
	"github.com/guilhermesouza/dbui/internal/tui/results"
	"github.com/guilhermesouza/dbui/internal/tui/routing"
	"github.com/guilhermesouza/dbui/internal/tui/styles"
)

// DefaultMaxRows is how many rows are kept per result.
const DefaultMaxRows = 1000

// TableRowLimit is the LIMIT used when opening a table from the explorer.
const TableRowLimit = 200

type focus int

const (
	focusExplorer focus = iota
	focusEditor
	focusResults
	focusCount
)

// KeyMap lists session-level bindings.
type KeyMap struct {
	NextPane, PrevPane, Execute, Cancel, External key.Binding
}

var Keys = KeyMap{
	NextPane: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),
	PrevPane: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev pane")),
	Execute:  key.NewBinding(key.WithKeys("ctrl+e", "f5"), key.WithHelp("ctrl+e", "run")),
	Cancel:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel query")),
	External: key.NewBinding(key.WithKeys("f2", "alt+e"), key.WithHelp("f2", "$EDITOR")),
}

// RequestConfirmMsg asks the root model to show a yes/no dialog. The chosen
// message is delivered back through the normal update loop.
type RequestConfirmMsg struct {
	Prompt      string
	OnYes, OnNo tea.Msg
}

// RunConfirmedMsg re-runs a statement after the user confirmed a write.
type RunConfirmedMsg struct {
	routing.ID
	Database, SQL string
	Offset        int
}

// ConfirmDeclinedMsg reports that the user declined a write.
type ConfirmDeclinedMsg struct{ routing.ID }

type queryDoneMsg struct {
	routing.ID
	req      int
	database string
	sql      string
	offset   int
	res      *db.Result
	err      error
}

type tickMsg struct {
	routing.ID
	req int
}

// Model is a connection tab.
type Model struct {
	id   routing.ID
	conn config.Connection
	db   db.Session

	explorer explorer.Model
	editor   editor.Model
	results  results.Model
	focus    focus

	database string // database the editor runs against

	running       bool
	started       time.Time
	cancel        context.CancelFunc
	req           int
	focusOnResult bool
	maxRows       int

	width, height int
}

// New creates a tab for an open session.
func New(id int, conn config.Connection, s db.Session) Model {
	m := Model{
		id:       routing.ID(id),
		conn:     conn,
		db:       s,
		explorer: explorer.New(id, s),
		editor:   editor.New(id),
		results:  results.New(),
		database: conn.DefaultDatabase(),
		maxRows:  DefaultMaxRows,
	}
	return m
}

// ID returns the session id.
func (m Model) ID() int { return int(m.id) }

// Connection returns the connection the tab belongs to.
func (m Model) Connection() config.Connection { return m.conn }

// Database returns the database the editor targets.
func (m Model) Database() string { return m.database }

// Running reports whether a query is executing.
func (m Model) Running() bool { return m.running }

// Init loads the explorer.
func (m *Model) Init() tea.Cmd {
	m.setFocus(focusExplorer)
	return m.explorer.Init()
}

// Close cancels any running query and closes the connections in the
// background.
func (m *Model) Close() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	s := m.db
	return func() tea.Msg {
		s.Close()
		return nil
	}
}

// SetSize sets the size available to the tab.
func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	ew, eh := m.layout()
	m.explorer.SetSize(ew-2, h-2)
	m.editor.SetSize(w-ew-2, eh-2)
	m.results.SetSize(w-ew-2, h-eh-2)
}

// layout returns the explorer width and the editor height (outer sizes).
func (m Model) layout() (explorerW, editorH int) {
	explorerW = min(max(m.width/4, 24), 42)
	if m.width < 72 {
		explorerW = m.width / 3
	}
	editorH = max(m.height*2/5, 6)
	return explorerW, editorH
}

func (m *Model) setFocus(f focus) {
	m.focus = f
	if f == focusEditor {
		m.editor.Focus()
	} else {
		m.editor.Blur()
	}
}

// Update handles messages routed to this tab and key presses while it is
// the active tab.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case explorer.UseDatabaseMsg:
		m.database = msg.Database
		return m, nil

	case explorer.OpenTableMsg:
		return m, m.openTable(msg)

	case editor.ExternalEditedMsg:
		if msg.Err != nil {
			m.results.SetError("external editor: " + msg.Err.Error())
		} else {
			m.editor.SetValue(msg.Text)
		}
		m.setFocus(focusEditor)
		return m, nil

	case RunConfirmedMsg:
		m.database = msg.Database
		return m, m.run(msg.SQL, msg.Offset, false)

	case ConfirmDeclinedMsg:
		m.results.SetMessage("Statement not executed.")
		return m, nil

	case tickMsg:
		if m.running && msg.req == m.req {
			return m, m.tick()
		}
		return m, nil

	case queryDoneMsg:
		return m.handleDone(msg)
	}

	// Anything else routed here belongs to the explorer (load results).
	var cmd tea.Cmd
	m.explorer, cmd = m.explorer.Update(msg)
	return m, cmd
}

func (m Model) handleKey(kp tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(kp, Keys.Execute):
		return m, m.execute()
	case key.Matches(kp, Keys.Cancel):
		if m.running && m.cancel != nil {
			m.cancel()
		}
		return m, nil
	case key.Matches(kp, Keys.External):
		return m, m.editor.OpenExternal()
	case key.Matches(kp, Keys.NextPane):
		m.setFocus((m.focus + 1) % focusCount)
		return m, nil
	case key.Matches(kp, Keys.PrevPane):
		m.setFocus((m.focus + focusCount - 1) % focusCount)
		return m, nil
	}

	var cmd tea.Cmd
	switch m.focus {
	case focusExplorer:
		m.explorer, cmd = m.explorer.Update(kp)
	case focusEditor:
		m.editor, cmd = m.editor.Update(kp)
	case focusResults:
		m.results, cmd = m.results.Update(kp)
	}
	return m, cmd
}

// CapturesEscape reports whether esc should stay inside the tab.
func (m Model) CapturesEscape() bool {
	return m.focus == focusResults && m.results.InDetail()
}

// EditorFocused reports whether keystrokes are text input.
func (m Model) EditorFocused() bool { return m.focus == focusEditor }

func (m *Model) execute() tea.Cmd {
	if m.running {
		return nil
	}
	sql, offset, ok := m.editor.Target()
	if !ok {
		m.results.SetMessage("Nothing to execute: the editor is empty.")
		return nil
	}
	return m.run(sql, offset, m.guarded())
}

func (m Model) guarded() bool { return m.conn.Production && !m.conn.ReadOnly }

func (m *Model) openTable(msg explorer.OpenTableMsg) tea.Cmd {
	if m.running {
		return nil
	}
	m.database = msg.Database
	sql := fmt.Sprintf("SELECT * FROM %s LIMIT %d;", pgx.Identifier{msg.Schema, msg.Table}.Sanitize(), TableRowLimit)
	prefix := strings.TrimRight(m.editor.Value(), " \t\n")
	if prefix != "" {
		prefix += "\n\n"
	}
	m.editor.SetValue(prefix + sql)
	m.focusOnResult = true
	return m.run(strings.TrimSuffix(sql, ";"), len([]rune(prefix)), m.guarded())
}

func (m *Model) run(sql string, offset int, guard bool) tea.Cmd {
	m.req++
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	m.started = time.Now()
	m.results.SetMessage("Running…")

	id, req, database, s, maxRows := m.id, m.req, m.database, m.db, m.maxRows
	exec := func() tea.Msg {
		defer cancel()
		res, err := s.Exec(ctx, database, sql, db.ExecOptions{MaxRows: maxRows, Guard: guard})
		if ctx.Err() != nil && err != nil {
			err = context.Canceled
		}
		return queryDoneMsg{ID: id, req: req, database: database, sql: sql, offset: offset, res: res, err: err}
	}
	return tea.Batch(exec, m.tick())
}

func (m Model) tick() tea.Cmd {
	id, req := m.id, m.req
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{ID: id, req: req} })
}

func (m Model) handleDone(msg queryDoneMsg) (Model, tea.Cmd) {
	if msg.req != m.req {
		return m, nil
	}
	m.running = false
	m.cancel = nil
	wantResults := m.focusOnResult
	m.focusOnResult = false

	switch {
	case errors.Is(msg.err, db.ErrWriteNeedsConfirmation):
		m.results.SetMessage("Waiting for confirmation…")
		prompt := fmt.Sprintf("%q is a PRODUCTION connection.\nThis statement writes data. Run it on database %q?\n\n%s",
			m.conn.Name, msg.database, preview(msg.sql))
		return m, func() tea.Msg {
			return RequestConfirmMsg{
				Prompt: prompt,
				OnYes:  RunConfirmedMsg{ID: m.id, Database: msg.database, SQL: msg.sql, Offset: msg.offset},
				OnNo:   ConfirmDeclinedMsg{ID: m.id},
			}
		}
	case errors.Is(msg.err, context.Canceled):
		m.results.SetError("Query canceled.")
	case msg.err != nil:
		m.results.SetError(FormatError(msg.err, msg.sql))
		var pgErr *pgconn.PgError
		if errors.As(msg.err, &pgErr) && pgErr.Position > 0 {
			m.editor.JumpTo(msg.offset + int(pgErr.Position) - 1)
		}
	default:
		m.results.SetResult(msg.res)
		if wantResults && msg.res.HasRows() {
			m.setFocus(focusResults)
		}
	}
	return m, nil
}

// FormatError renders a database error psql-style, with a caret under the
// error position when the server reports one.
func FormatError(err error, sql string) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "ERROR: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s", pgErr.Severity, pgErr.Code, pgErr.Message)
	if pgErr.Position > 0 {
		r := []rune(sql)
		pos := min(int(pgErr.Position)-1, len(r))
		lineStart, lineNo := 0, 1
		for i := 0; i < pos; i++ {
			if r[i] == '\n' {
				lineStart, lineNo = i+1, lineNo+1
			}
		}
		lineEnd := lineStart
		for lineEnd < len(r) && r[lineEnd] != '\n' {
			lineEnd++
		}
		prefix := fmt.Sprintf("LINE %d: ", lineNo)
		fmt.Fprintf(&b, "\n%s%s\n%s^", prefix, string(r[lineStart:lineEnd]), strings.Repeat(" ", len(prefix)+pos-lineStart))
	}
	if pgErr.Detail != "" {
		b.WriteString("\nDETAIL: " + pgErr.Detail)
	}
	if pgErr.Hint != "" {
		b.WriteString("\nHINT: " + pgErr.Hint)
	}
	return b.String()
}

func preview(sql string) string {
	lines := strings.Split(strings.TrimSpace(sql), "\n")
	if len(lines) > 6 {
		lines = append(lines[:6], "…")
	}
	return strings.Join(lines, "\n")
}

// Title is the label shown in the tab bar.
func (m Model) Title() string { return m.conn.Name }

// HelpKeys returns the bindings relevant to the focused pane.
func (m Model) HelpKeys() []key.Binding {
	keys := []key.Binding{Keys.NextPane, Keys.Execute}
	if m.running {
		keys = append(keys, Keys.Cancel)
	}
	switch m.focus {
	case focusExplorer:
		keys = append(keys, explorer.Keys.Open, explorer.Keys.Expand, explorer.Keys.Collapse, explorer.Keys.Refresh)
	case focusEditor:
		keys = append(keys, Keys.External)
	case focusResults:
		if m.results.InDetail() {
			keys = append(keys, results.Keys.Close, results.Keys.Copy)
		} else {
			keys = append(keys, results.Keys.Open, results.Keys.Copy, results.Keys.Top, results.Keys.Bottom)
		}
	}
	return keys
}

// View renders the tab.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	ew, eh := m.layout()
	var accent color.Color
	if m.conn.Production {
		accent = styles.Danger
	}

	explorerPanel := styles.Panel("Explorer", m.explorer.View(m.focus == focusExplorer), ew, m.height, m.focus == focusExplorer, accent)

	qTitle := "Query · " + m.database
	switch {
	case m.conn.ReadOnly:
		qTitle += " · read-only"
	case m.conn.Production:
		qTitle += " · PRODUCTION"
	}
	editorPanel := styles.Panel(qTitle, m.editor.View(), m.width-ew, eh, m.focus == focusEditor, accent)

	rTitle := m.results.Title()
	resultsBody := m.results.View()
	if m.running {
		rTitle = "Results"
		resultsBody = styles.Dim.Render(fmt.Sprintf("Running… %.1fs  (ctrl+c to cancel)", time.Since(m.started).Seconds()))
	}
	resultsPanel := styles.Panel(rTitle, resultsBody, m.width-ew, m.height-eh, m.focus == focusResults, accent)

	right := lipgloss.JoinVertical(lipgloss.Left, editorPanel, resultsPanel)
	return lipgloss.JoinHorizontal(lipgloss.Top, explorerPanel, right)
}

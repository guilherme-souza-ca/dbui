// Package editor wraps a textarea with what the SQL editor needs: picking the
// statement to run, jumping to error positions and editing in $EDITOR.
package editor

import (
	"os"
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/guilhermesouza/dbui/internal/sqlsplit"
	"github.com/guilhermesouza/dbui/internal/tui/routing"
)

// Model is the SQL editor.
type Model struct {
	id routing.ID
	ta textarea.Model
}

// ExternalEditedMsg carries the buffer back from $EDITOR.
type ExternalEditedMsg struct {
	routing.ID
	Text string
	Err  error
}

// New returns an empty editor for the given session.
func New(sessionID int) Model {
	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.Prompt = ""
	ta.Placeholder = "SELECT * FROM ...;  (ctrl+e runs the statement under the cursor or the selection)"
	ta.CharLimit = 0
	ta.MaxHeight = 0
	// ctrl+e is our "execute" key; keep End for end-of-line.
	ta.KeyMap.LineEnd.SetKeys("end")
	return Model{id: routing.ID(sessionID), ta: ta}
}

func (m *Model) SetSize(w, h int) {
	m.ta.SetWidth(w)
	m.ta.SetHeight(h)
}

func (m *Model) Focus() tea.Cmd { return m.ta.Focus() }
func (m *Model) Blur()          { m.ta.Blur() }
func (m Model) Value() string   { return m.ta.Value() }

// SetValue replaces the buffer and moves the cursor to the end.
func (m *Model) SetValue(s string) { m.ta.SetValue(s) }

// Update forwards messages to the textarea.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

// View renders the editor.
func (m Model) View() string { return m.ta.View() }

// Target returns the SQL to execute and its rune offset in the buffer: the
// selection when there is one, otherwise the statement under the cursor.
func (m Model) Target() (sql string, offset int, ok bool) {
	value := m.ta.Value()
	if m.ta.HasSelection() {
		start, _, _ := m.ta.Selection()
		sel := m.ta.SelectedText()
		if strings.TrimSpace(sel) != "" {
			return sel, offsetOf(value, start.Row, start.Col), true
		}
	}
	st, ok := sqlsplit.At(value, offsetOf(value, m.ta.Line(), m.ta.Column()))
	if !ok {
		return "", 0, false
	}
	return st.Text, st.Start, true
}

// CursorPosition returns the cursor row and column (in runes).
func (m Model) CursorPosition() (row, col int) { return m.ta.Line(), m.ta.Column() }

// JumpTo moves the cursor to a rune offset in the buffer.
func (m *Model) JumpTo(offset int) {
	row, col := position(m.ta.Value(), offset)
	m.ta.MoveToBegin()
	for i := 0; m.ta.Line() < row && i < 100000; i++ {
		m.ta.CursorDown()
	}
	m.ta.SetCursorColumn(col)
}

// OpenExternal edits the buffer in $VISUAL/$EDITOR (vi by default).
func (m Model) OpenExternal() tea.Cmd {
	id := m.id
	f, err := os.CreateTemp("", "dbui-*.sql")
	if err != nil {
		return func() tea.Msg { return ExternalEditedMsg{ID: id, Err: err} }
	}
	_, err = f.WriteString(m.ta.Value())
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return func() tea.Msg { return ExternalEditedMsg{ID: id, Err: err} }
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return ExternalEditedMsg{ID: id, Err: err}
		}
		data, rerr := os.ReadFile(f.Name())
		if rerr != nil {
			return ExternalEditedMsg{ID: id, Err: rerr}
		}
		return ExternalEditedMsg{ID: id, Text: strings.TrimRight(string(data), "\n")}
	})
}

// offsetOf converts a (row, col) rune position into a rune offset.
func offsetOf(value string, row, col int) int {
	lines := strings.Split(value, "\n")
	off := 0
	for i := 0; i < row && i < len(lines); i++ {
		off += len([]rune(lines[i])) + 1
	}
	return off + col
}

// position converts a rune offset into (row, col).
func position(value string, offset int) (row, col int) {
	for _, r := range []rune(value)[:min(max(offset, 0), len([]rune(value)))] {
		if r == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return row, col
}

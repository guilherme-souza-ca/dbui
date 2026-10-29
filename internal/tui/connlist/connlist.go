// Package connlist shows the saved connections.
package connlist

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/tui/styles"
)

// Messages emitted by the list.
type (
	OpenMsg   struct{ Name string }
	NewMsg    struct{}
	EditMsg   struct{ Name string }
	DeleteMsg struct{ Name string }
	CloseMsg  struct{}
)

var (
	keyUp     = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown   = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyOpen   = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "connect"))
	keyNew    = key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new"))
	keyEdit   = key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit"))
	keyDelete = key.NewBinding(key.WithKeys("d", "delete"), key.WithHelp("d", "delete"))
	keyClose  = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close"))
)

// Model is the connection list.
type Model struct {
	conns     []config.Connection
	open      map[string]bool
	cursor    int
	closeable bool
	message   string
}

// New lists conns; names in open are marked as connected. When closeable is
// false (no tab is open yet) esc does nothing.
func New(conns []config.Connection, open map[string]bool, closeable bool) Model {
	return Model{conns: conns, open: open, closeable: closeable}
}

// Select moves the cursor to the named connection, if present.
func (m *Model) Select(name string) {
	for i, c := range m.conns {
		if c.Name == name {
			m.cursor = i
		}
	}
}

// SetMessage shows a line (e.g. a connection error) under the list.
func (m *Model) SetMessage(s string) { m.message = s }

// Update handles keys.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	emit := func(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }
	selected := ""
	if m.cursor < len(m.conns) {
		selected = m.conns[m.cursor].Name
	}
	switch {
	case key.Matches(kp, keyUp):
		m.cursor = max(m.cursor-1, 0)
	case key.Matches(kp, keyDown):
		m.cursor = min(m.cursor+1, max(len(m.conns)-1, 0))
	case key.Matches(kp, keyNew):
		return m, emit(NewMsg{})
	case key.Matches(kp, keyClose):
		if m.closeable {
			return m, emit(CloseMsg{})
		}
	case selected == "":
	case key.Matches(kp, keyOpen):
		return m, emit(OpenMsg{Name: selected})
	case key.Matches(kp, keyEdit):
		return m, emit(EditMsg{Name: selected})
	case key.Matches(kp, keyDelete):
		return m, emit(DeleteMsg{Name: selected})
	}
	return m, nil
}

// View renders the list as a box.
func (m Model) View(maxWidth, maxHeight int) string {
	w := min(max(maxWidth-8, 40), 90)
	var b strings.Builder
	b.WriteString(styles.Title.Render("Connections") + "\n\n")
	if len(m.conns) == 0 {
		b.WriteString(styles.Dim.Render("No saved connections yet. Press n to add one.") + "\n")
	}
	visible := max(maxHeight-12, 3)
	start := max(0, m.cursor-visible+1)
	for i := start; i < len(m.conns) && i < start+visible; i++ {
		c := m.conns[i]
		dot := styles.Dim.Render("○")
		if m.open[c.Name] {
			dot = styles.Success.Render("●")
		}
		addr := fmt.Sprintf("%s@%s:%d/%s", c.User, c.Host, c.Port, c.DefaultDatabase())
		var tags []string
		if c.Production {
			tags = append(tags, styles.Error.Bold(true).Render("PROD"))
		}
		if c.ReadOnly {
			tags = append(tags, lipgloss.NewStyle().Foreground(styles.Warn).Render("RO"))
		}
		name := c.Name
		if i == m.cursor {
			name = styles.Selected.Render(" " + name + " ")
		} else {
			name = " " + name + " "
		}
		line := fmt.Sprintf("%s %s  %s %s", dot, name, styles.Dim.Render(addr), strings.Join(tags, " "))
		b.WriteString(styles.Truncate(line, w) + "\n")
	}
	if m.message != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Width(w).Foreground(styles.Danger).Render(m.message) + "\n")
	}
	help := []string{"enter connect", "n new", "e edit", "d delete"}
	if m.closeable {
		help = append(help, "esc close")
	}
	b.WriteString("\n" + styles.Dim.Render(strings.Join(help, " · ")))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(styles.Accent).Padding(1, 2).Width(w + 6).Render(b.String())
}

// Package confirm is a yes/no dialog.
package confirm

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/guilhermesouza/dbui/internal/tui/styles"
)

// AnsweredMsg is emitted when the dialog closes. Msg is the payload chosen
// by the user (OnYes or OnNo, may be nil).
type AnsweredMsg struct {
	Msg tea.Msg
}

var (
	keyYes    = key.NewBinding(key.WithKeys("y", "Y"))
	keyNo     = key.NewBinding(key.WithKeys("n", "N", "esc"))
	keyToggle = key.NewBinding(key.WithKeys("left", "right", "h", "l", "tab", "shift+tab"))
	keyEnter  = key.NewBinding(key.WithKeys("enter"))
)

// Model is the dialog.
type Model struct {
	title, prompt string
	onYes, onNo   tea.Msg
	yes           bool // focused button
	danger        bool
}

// New builds a dialog. "No" is focused by default so that enter is safe.
func New(title, prompt string, onYes, onNo tea.Msg, danger bool) Model {
	return Model{title: title, prompt: prompt, onYes: onYes, onNo: onNo, danger: danger}
}

func answer(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return AnsweredMsg{Msg: msg} }
}

// Update handles keys.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Matches(kp, keyYes):
		return m, answer(m.onYes)
	case key.Matches(kp, keyNo):
		return m, answer(m.onNo)
	case key.Matches(kp, keyToggle):
		m.yes = !m.yes
	case key.Matches(kp, keyEnter):
		if m.yes {
			return m, answer(m.onYes)
		}
		return m, answer(m.onNo)
	}
	return m, nil
}

// View renders the dialog box.
func (m Model) View(maxWidth int) string {
	accent := styles.Accent
	if m.danger {
		accent = styles.Danger
	}
	button := func(label string, active bool) string {
		s := lipgloss.NewStyle().Padding(0, 2)
		if active {
			s = s.Background(accent).Foreground(lipgloss.Color("0")).Bold(true)
		} else {
			s = s.Foreground(styles.Muted)
		}
		return s.Render(label)
	}
	w := min(max(maxWidth-8, 30), 72)
	body := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Foreground(accent).Render(m.title),
		"",
		lipgloss.NewStyle().Width(w).Render(m.prompt),
		"",
		button("Yes (y)", m.yes)+"  "+button("No (n)", !m.yes),
	)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 2).Render(body)
}

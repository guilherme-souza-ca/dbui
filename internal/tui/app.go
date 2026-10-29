// Package tui is the root Bubble Tea model: tab bar, overlays (connection
// list, forms, dialogs) and the status bar.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/secret"
	"github.com/guilhermesouza/dbui/internal/tui/confirm"
	"github.com/guilhermesouza/dbui/internal/tui/connform"
	"github.com/guilhermesouza/dbui/internal/tui/connlist"
	"github.com/guilhermesouza/dbui/internal/tui/explorer"
	"github.com/guilhermesouza/dbui/internal/tui/results"
	"github.com/guilhermesouza/dbui/internal/tui/routing"
	"github.com/guilhermesouza/dbui/internal/tui/session"
	"github.com/guilhermesouza/dbui/internal/tui/styles"
)

const connectTimeout = 15 * time.Second

// Deps are the side-effecting collaborators of the app (faked in tests).
type Deps struct {
	Store   *config.Store
	Secrets secret.Store
	Open    func(ctx context.Context, c config.Connection, password string) (db.Session, error)
}

// Global key bindings.
var keys = struct {
	Connections, CloseTab, NextTab, PrevTab, Help, Quit key.Binding
}{
	Connections: key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "connections")),
	CloseTab:    key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "close tab")),
	NextTab:     key.NewBinding(key.WithKeys("ctrl+pgdown", "alt+]"), key.WithHelp("alt+]", "next tab")),
	PrevTab:     key.NewBinding(key.WithKeys("ctrl+pgup", "alt+["), key.WithHelp("alt+[", "prev tab")),
	Help:        key.NewBinding(key.WithKeys("f1"), key.WithHelp("f1", "help")),
	Quit:        key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}

// overlay is a modal shown on top of the tabs.
type overlay interface {
	update(tea.Msg) (overlay, tea.Cmd)
	view(w, h int) string
}

type listOverlay struct{ m connlist.Model }
type formOverlay struct{ m connform.Model }
type confirmOverlay struct{ m confirm.Model }
type helpOverlay struct{}

func (o listOverlay) update(msg tea.Msg) (overlay, tea.Cmd) {
	m, cmd := o.m.Update(msg)
	return listOverlay{m}, cmd
}
func (o listOverlay) view(w, h int) string { return o.m.View(w, h) }

func (o formOverlay) update(msg tea.Msg) (overlay, tea.Cmd) {
	m, cmd := o.m.Update(msg)
	return formOverlay{m}, cmd
}
func (o formOverlay) view(int, int) string { return o.m.View() }

func (o confirmOverlay) update(msg tea.Msg) (overlay, tea.Cmd) {
	m, cmd := o.m.Update(msg)
	return confirmOverlay{m}, cmd
}
func (o confirmOverlay) view(w, _ int) string { return o.m.View(w) }

type closeHelpMsg struct{}

func (helpOverlay) update(msg tea.Msg) (overlay, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		return helpOverlay{}, func() tea.Msg { return closeHelpMsg{} }
	}
	return helpOverlay{}, nil
}
func (helpOverlay) view(int, int) string { return helpText() }

// Internal messages.
type (
	sessionOpenedMsg struct {
		conn     config.Connection
		password string
		prompted bool // password came from the prompt
		sess     db.Session
		err      error
	}
	deleteConfirmedMsg struct{ name string }
	quitConfirmedMsg   struct{}
	clearStatusMsg     struct{ seq int }
)

// Model is the root model.
type Model struct {
	deps Deps

	tabs   []session.Model
	active int
	nextID int

	overlays   []overlay // stack, top is last
	connecting string    // name of the connection being opened

	status    string
	statusErr bool
	statusSeq int

	help          help.Model
	width, height int
}

// New builds the root model. When initial names a saved connection it is
// opened right away.
func New(deps Deps, initial string) Model {
	m := Model{deps: deps, help: help.New(), nextID: 1}
	m.pushList()
	if initial != "" {
		m.connecting = initial
	}
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	if m.connecting != "" {
		if c, ok := m.deps.Store.Get(m.connecting); ok {
			return m.open(c)
		}
	}
	return nil
}

func (m *Model) pushList() {
	open := map[string]bool{}
	for _, t := range m.tabs {
		open[t.Connection().Name] = true
	}
	m.overlays = append(m.overlays, listOverlay{connlist.New(m.deps.Store.Connections, open, len(m.tabs) > 0)})
}

func (m *Model) pop() {
	if len(m.overlays) > 0 {
		m.overlays = m.overlays[:len(m.overlays)-1]
	}
	if len(m.overlays) == 0 && len(m.tabs) == 0 {
		m.pushList()
	}
}

// refreshList rebuilds the list overlay (if it is on the stack) after the
// saved connections or the open tabs changed, keeping the selection.
func (m *Model) refreshList(selectName, message string) {
	for i, o := range m.overlays {
		if _, ok := o.(listOverlay); ok {
			open := map[string]bool{}
			for _, t := range m.tabs {
				open[t.Connection().Name] = true
			}
			l := connlist.New(m.deps.Store.Connections, open, len(m.tabs) > 0)
			l.Select(selectName)
			l.SetMessage(message)
			m.overlays[i] = listOverlay{l}
		}
	}
}

func (m *Model) setStatus(s string, isErr bool) tea.Cmd {
	m.status, m.statusErr = s, isErr
	m.statusSeq++
	seq := m.statusSeq
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{seq: seq} })
}

func (m Model) contentSize() (int, int) { return m.width, max(m.height-2, 0) }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.SetWidth(msg.Width)
		w, h := m.contentSize()
		for i := range m.tabs {
			m.tabs[i].SetSize(w, h)
		}
		return m, m.forwardToOverlay(msg)

	case clearStatusMsg:
		if msg.seq == m.statusSeq {
			m.status = ""
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	// Connection list.
	case connlist.OpenMsg:
		c, ok := m.deps.Store.Get(msg.Name)
		if !ok {
			return m, nil
		}
		for i, t := range m.tabs {
			if t.Connection().Name == c.Name {
				m.active = i
				m.overlays = nil
				return m, nil
			}
		}
		return m, m.open(c)
	case connlist.NewMsg:
		return m, m.pushForm(nil)
	case connlist.EditMsg:
		c, ok := m.deps.Store.Get(msg.Name)
		if !ok {
			return m, nil
		}
		return m, m.pushForm(&c)
	case connlist.DeleteMsg:
		m.overlays = append(m.overlays, confirmOverlay{confirm.New(
			"Delete connection",
			fmt.Sprintf("Delete %q and its stored password?", msg.Name),
			deleteConfirmedMsg{msg.Name}, nil, true)})
		return m, nil
	case connlist.CloseMsg:
		m.pop()
		return m, nil

	// Forms.
	case connform.CancelMsg:
		m.pop()
		return m, nil
	case connform.SubmitMsg:
		return m, m.saveConnection(msg)
	case connform.PasswordMsg:
		m.pop()
		c, ok := m.deps.Store.Get(msg.Name)
		if !ok {
			return m, nil
		}
		return m, m.openWith(c, msg.Password, true)

	// Dialogs.
	case confirm.AnsweredMsg:
		m.pop()
		if msg.Msg == nil {
			return m, nil
		}
		next := msg.Msg
		return m, func() tea.Msg { return next }
	case deleteConfirmedMsg:
		return m, m.deleteConnection(msg.name)
	case quitConfirmedMsg:
		return m, m.quit()
	case closeHelpMsg:
		m.pop()
		return m, nil
	case session.RequestConfirmMsg:
		m.overlays = append(m.overlays, confirmOverlay{confirm.New("Production write", msg.Prompt, msg.OnYes, msg.OnNo, true)})
		return m, nil

	case sessionOpenedMsg:
		return m.handleOpened(msg)
	}

	// Messages addressed to a tab.
	if rm, ok := msg.(routing.Msg); ok {
		for i := range m.tabs {
			if m.tabs[i].ID() == rm.SessionID() {
				var cmd tea.Cmd
				m.tabs[i], cmd = m.tabs[i].Update(msg)
				return m, cmd
			}
		}
		return m, nil
	}

	// Anything else (cursor blink, form internals, …) goes to the top overlay
	// and the active tab.
	return m, tea.Batch(m.forwardToOverlay(msg), m.forwardToActive(msg))
}

func (m *Model) forwardToOverlay(msg tea.Msg) tea.Cmd {
	if len(m.overlays) == 0 {
		return nil
	}
	top := len(m.overlays) - 1
	var cmd tea.Cmd
	m.overlays[top], cmd = m.overlays[top].update(msg)
	return cmd
}

func (m *Model) forwardToActive(msg tea.Msg) tea.Cmd {
	if len(m.tabs) == 0 {
		return nil
	}
	var cmd tea.Cmd
	m.tabs[m.active], cmd = m.tabs[m.active].Update(msg)
	return cmd
}

func (m Model) handleKey(kp tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c cancels a running query first, otherwise quits.
	if key.Matches(kp, keys.Quit) {
		if len(m.overlays) == 0 && len(m.tabs) > 0 && m.tabs[m.active].Running() {
			return m, m.forwardToActive(kp)
		}
		if len(m.tabs) == 0 {
			return m, m.quit()
		}
		if _, ok := m.top().(confirmOverlay); ok {
			return m, m.quit() // second ctrl+c
		}
		m.overlays = append(m.overlays, confirmOverlay{confirm.New("Quit",
			fmt.Sprintf("Quit dbui? %d open connection(s) will be closed.", len(m.tabs)),
			quitConfirmedMsg{}, nil, false)})
		return m, nil
	}

	if len(m.overlays) > 0 {
		return m, m.forwardToOverlay(kp)
	}

	switch {
	case key.Matches(kp, keys.Connections):
		m.pushList()
		return m, nil
	case key.Matches(kp, keys.Help):
		m.overlays = append(m.overlays, helpOverlay{})
		return m, nil
	case key.Matches(kp, keys.CloseTab):
		return m, m.closeTab(m.active)
	case key.Matches(kp, keys.NextTab):
		if len(m.tabs) > 0 {
			m.active = (m.active + 1) % len(m.tabs)
		}
		return m, nil
	case key.Matches(kp, keys.PrevTab):
		if len(m.tabs) > 0 {
			m.active = (m.active + len(m.tabs) - 1) % len(m.tabs)
		}
		return m, nil
	}
	// alt+1..9 jumps to a tab.
	if s := kp.String(); strings.HasPrefix(s, "alt+") && len(s) == 5 && s[4] >= '1' && s[4] <= '9' {
		if i := int(s[4] - '1'); i < len(m.tabs) {
			m.active = i
		}
		return m, nil
	}
	return m, m.forwardToActive(kp)
}

func (m Model) top() overlay {
	if len(m.overlays) == 0 {
		return nil
	}
	return m.overlays[len(m.overlays)-1]
}

func (m *Model) quit() tea.Cmd {
	for i := range m.tabs {
		m.tabs[i].Close()() // close synchronously so connections end cleanly
	}
	return tea.Quit
}

func (m *Model) closeTab(i int) tea.Cmd {
	if i < 0 || i >= len(m.tabs) {
		return nil
	}
	cmd := m.tabs[i].Close()
	name := m.tabs[i].Connection().Name
	m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
	if m.active >= len(m.tabs) {
		m.active = max(len(m.tabs)-1, 0)
	}
	if len(m.tabs) == 0 {
		m.overlays = nil
		m.pushList()
	}
	return tea.Batch(cmd, m.setStatus("Closed "+name, false))
}

func (m *Model) pushForm(existing *config.Connection) tea.Cmd {
	store := m.deps.Store
	f := connform.New(existing, func(name string) bool { _, ok := store.Get(name); return ok })
	m.overlays = append(m.overlays, formOverlay{f})
	return f.Init()
}

func (m *Model) saveConnection(msg connform.SubmitMsg) tea.Cmd {
	store := m.deps.Store
	if err := store.Upsert(msg.OldName, msg.Conn); err != nil {
		return m.setStatus(err.Error(), true)
	}
	if err := store.Save(); err != nil {
		return m.setStatus("save failed: "+err.Error(), true)
	}
	m.pop()

	var warn string
	if msg.OldName != "" && msg.OldName != msg.Conn.Name {
		// Move the stored password to the new name.
		if pw, err := m.deps.Secrets.Get(msg.OldName); err == nil && !msg.PasswordChanged {
			msg.Password, msg.PasswordChanged = pw, true
		}
		_ = m.deps.Secrets.Delete(msg.OldName)
	}
	if msg.PasswordChanged && msg.Password != "" {
		if err := m.deps.Secrets.Set(msg.Conn.Name, msg.Password); err != nil {
			warn = "password not saved in the keychain: " + err.Error()
		}
	}
	m.refreshList(msg.Conn.Name, "")
	if warn != "" {
		return m.setStatus(warn, true)
	}
	return m.setStatus("Saved "+msg.Conn.Name, false)
}

func (m *Model) deleteConnection(name string) tea.Cmd {
	m.deps.Store.Delete(name)
	if err := m.deps.Store.Save(); err != nil {
		return m.setStatus("save failed: "+err.Error(), true)
	}
	_ = m.deps.Secrets.Delete(name)
	m.refreshList("", "")
	return m.setStatus("Deleted "+name, false)
}

// open connects using the stored password (or none, so that ~/.pgpass and
// trust auth still work).
func (m *Model) open(c config.Connection) tea.Cmd {
	pw, err := m.deps.Secrets.Get(c.Name)
	if err != nil && !errors.Is(err, secret.ErrNotFound) {
		form := connform.NewPasswordPrompt(c.Name, "The keychain is unavailable: "+err.Error())
		m.overlays = append(m.overlays, formOverlay{form})
		return form.Init()
	}
	return m.openWith(c, pw, false)
}

func (m *Model) openWith(c config.Connection, password string, prompted bool) tea.Cmd {
	m.connecting = c.Name
	opener := m.deps.Open
	return tea.Batch(
		m.setStatus("Connecting to "+c.Name+"…", false),
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
			defer cancel()
			s, err := opener(ctx, c, password)
			return sessionOpenedMsg{conn: c, password: password, prompted: prompted, sess: s, err: err}
		},
	)
}

func (m Model) handleOpened(msg sessionOpenedMsg) (tea.Model, tea.Cmd) {
	m.connecting = ""
	if msg.err != nil {
		if isAuthError(msg.err) {
			reason := "Authentication failed."
			if msg.password == "" {
				reason = "No password is stored for this connection."
			}
			form := connform.NewPasswordPrompt(msg.conn.Name, reason)
			m.overlays = append(m.overlays, formOverlay{form})
			return m, tea.Batch(form.Init(), m.setStatus(msg.err.Error(), true))
		}
		m.refreshList(msg.conn.Name, "Could not connect to "+msg.conn.Name+": "+msg.err.Error())
		return m, m.setStatus("connection failed", true)
	}

	var cmds []tea.Cmd
	if msg.prompted && msg.password != "" {
		if err := m.deps.Secrets.Set(msg.conn.Name, msg.password); err != nil {
			cmds = append(cmds, m.setStatus("connected (password not saved: "+err.Error()+")", true))
		}
	}
	if len(cmds) == 0 {
		cmds = append(cmds, m.setStatus("Connected to "+msg.conn.Name, false))
	}

	t := session.New(m.nextID, msg.conn, msg.sess)
	m.nextID++
	w, h := m.contentSize()
	t.SetSize(w, h)
	cmds = append(cmds, t.Init())
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.overlays = nil
	return m, tea.Batch(cmds...)
}

func isAuthError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "28P01" || pgErr.Code == "28000"
	}
	return strings.Contains(err.Error(), "password authentication failed") ||
		strings.Contains(err.Error(), "no password")
}

// View implements tea.Model.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "dbui"
	if len(m.tabs) > 0 {
		v.WindowTitle = "dbui · " + m.tabs[m.active].Title()
	}
	return v
}

func (m Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	w, h := m.contentSize()
	var content string
	if len(m.tabs) > 0 {
		content = m.tabs[m.active].View()
	} else {
		content = lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, styles.Dim.Render("dbui"))
	}
	content = lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(content)

	if top := m.top(); top != nil {
		box := top.view(w, h)
		bw, bh := lipgloss.Width(box), lipgloss.Height(box)
		x, y := max((w-bw)/2, 0), max((h-bh)/2, 0)
		content = lipgloss.NewCompositor(
			lipgloss.NewLayer(content),
			lipgloss.NewLayer(box).X(x).Y(y).Z(1),
		).Render()
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.tabBar(), content, m.statusBar())
}

func (m Model) tabBar() string {
	parts := []string{styles.Title.Render(" dbui ")}
	for i, t := range m.tabs {
		label := fmt.Sprintf(" %d %s ", i+1, t.Title())
		if t.Running() {
			label = fmt.Sprintf(" %d %s ⋯ ", i+1, t.Title())
		}
		s := lipgloss.NewStyle()
		if t.Connection().Production {
			s = s.Foreground(styles.Danger)
		}
		if i == m.active {
			bg := styles.Accent
			if t.Connection().Production {
				bg = styles.Danger
			}
			s = s.Background(bg).Foreground(lipgloss.Color("0")).Bold(true)
		}
		parts = append(parts, s.Render(label))
	}
	if m.connecting != "" {
		parts = append(parts, styles.Dim.Render(" connecting to "+m.connecting+"…"))
	}
	return styles.Truncate(strings.Join(parts, " "), m.width)
}

func (m Model) statusBar() string {
	var bindings []key.Binding
	if len(m.overlays) == 0 && len(m.tabs) > 0 {
		bindings = append(m.tabs[m.active].HelpKeys(), keys.Connections, keys.CloseTab, keys.Help)
	} else {
		bindings = []key.Binding{keys.Help, keys.Quit}
	}
	right := m.help.ShortHelpView(bindings)
	if m.status == "" {
		return right
	}
	st := styles.Success
	if m.statusErr {
		st = styles.Error
	}
	left := st.Render(styles.Truncate(m.status, max(m.width/2, 20)))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		return styles.Truncate(left+"  "+right, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func helpText() string {
	section := func(title string, bs ...key.Binding) string {
		var b strings.Builder
		b.WriteString(styles.Title.Render(title) + "\n")
		for _, k := range bs {
			h := k.Help()
			b.WriteString(fmt.Sprintf("  %-14s %s\n", h.Key, h.Desc))
		}
		return b.String()
	}
	tabs := key.NewBinding(key.WithHelp("alt+1…9", "go to tab"))
	sel := key.NewBinding(key.WithHelp("shift+arrows", "select text"))
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		section("Global", keys.Connections, keys.CloseTab, keys.NextTab, keys.PrevTab, tabs, keys.Help, keys.Quit)+"\n"+
			section("Tab", session.Keys.NextPane, session.Keys.PrevPane, session.Keys.Execute, session.Keys.Cancel, session.Keys.External, sel),
		"    ",
		section("Explorer", explorer.Keys.Up, explorer.Keys.Down, explorer.Keys.Expand, explorer.Keys.Collapse, explorer.Keys.Open, explorer.Keys.Refresh)+"\n"+
			section("Results", results.Keys.Up, results.Keys.Down, results.Keys.Left, results.Keys.Right, results.Keys.PageDown,
				results.Keys.Top, results.Keys.Bottom, results.Keys.Home, results.Keys.End, results.Keys.Open, results.Keys.Copy),
	)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(styles.Accent).Padding(1, 2).
		Render(body + "\n" + styles.Dim.Render("press any key to close"))
}

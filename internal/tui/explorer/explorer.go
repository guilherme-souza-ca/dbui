// Package explorer is the lazy-loading tree of databases, schemas and tables.
package explorer

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/tui/routing"
	"github.com/guilhermesouza/dbui/internal/tui/styles"
)

const loadTimeout = 30 * time.Second

// Loader is the catalog subset of db.Session the tree needs.
type Loader interface {
	ListDatabases(ctx context.Context) ([]string, error)
	ListSchemas(ctx context.Context, database string) ([]string, error)
	ListTables(ctx context.Context, database, schema string) ([]db.Table, error)
}

// Kind is the type of a tree node.
type Kind int

const (
	KindDatabase Kind = iota
	KindSchema
	KindTable
)

type node struct {
	kind     Kind
	name     string
	database string
	schema   string
	table    db.Table
	parent   *node
	children []*node

	expanded, loading, loaded bool
	err                       error
}

func (n *node) depth() int {
	d := 0
	for p := n.parent; p != nil; p = p.parent {
		d++
	}
	return d
}

// OpenTableMsg asks the session to browse a table.
type OpenTableMsg struct {
	routing.ID
	Database, Schema, Table string
}

// UseDatabaseMsg tells the session which database the editor should target.
type UseDatabaseMsg struct {
	routing.ID
	Database string
}

type loadedMsg struct {
	routing.ID
	target *node // nil = root (database list)
	names  []string
	tables []db.Table
	err    error
}

// KeyMap lists the explorer bindings.
type KeyMap struct {
	Up, Down, Expand, Collapse, Open, Refresh key.Binding
}

var Keys = KeyMap{
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Expand:   key.NewBinding(key.WithKeys("right", "l", "space"), key.WithHelp("→/l", "expand")),
	Collapse: key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "collapse")),
	Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open/use")),
	Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
}

// Model is the explorer tree.
type Model struct {
	id     routing.ID
	loader Loader

	roots       []*node
	rootLoading bool
	rootErr     error

	cursor, offset int
	width, height  int
}

// New returns a tree for the session with the given id.
func New(sessionID int, loader Loader) Model {
	return Model{id: routing.ID(sessionID), loader: loader}
}

// Init loads the database list.
func (m *Model) Init() tea.Cmd { return m.load(nil) }

// SetSize sets the inner size.
func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	m.scroll()
}

func (m *Model) load(target *node) tea.Cmd {
	id, loader := m.id, m.loader
	if target == nil {
		m.rootLoading, m.rootErr = true, nil
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			names, err := loader.ListDatabases(ctx)
			return loadedMsg{ID: id, names: names, err: err}
		}
	}
	target.loading, target.err = true, nil
	kind, database, schema := target.kind, target.database, target.schema
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := loadedMsg{ID: id, target: target}
		if kind == KindDatabase {
			msg.names, msg.err = loader.ListSchemas(ctx, database)
		} else {
			msg.tables, msg.err = loader.ListTables(ctx, database, schema)
		}
		return msg
	}
}

// visible returns the nodes currently shown, in order.
func (m *Model) visible() []*node {
	var out []*node
	var walk func(ns []*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			out = append(out, n)
			if n.expanded {
				walk(n.children)
			}
		}
	}
	walk(m.roots)
	return out
}

func (m *Model) selected() *node {
	v := m.visible()
	if m.cursor >= 0 && m.cursor < len(v) {
		return v[m.cursor]
	}
	return nil
}

// Selected describes the node under the cursor (database, schema, table).
func (m *Model) Selected() (kind Kind, database, schema, table string, ok bool) {
	n := m.selected()
	if n == nil {
		return 0, "", "", "", false
	}
	return n.kind, n.database, n.schema, n.table.Name, true
}

// Update handles load results and, when focused, keys.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loadedMsg:
		m.applyLoaded(msg)
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) applyLoaded(msg loadedMsg) {
	if msg.target == nil {
		m.rootLoading = false
		m.rootErr = msg.err
		if msg.err == nil {
			m.roots = m.roots[:0]
			for _, name := range msg.names {
				m.roots = append(m.roots, &node{kind: KindDatabase, name: name, database: name})
			}
		}
		m.scroll()
		return
	}
	n := msg.target
	n.loading = false
	n.err = msg.err
	if msg.err != nil {
		return
	}
	n.loaded = true
	n.children = nil
	for _, name := range msg.names {
		n.children = append(n.children, &node{kind: KindSchema, name: name, database: n.database, schema: name, parent: n})
	}
	for _, t := range msg.tables {
		n.children = append(n.children, &node{kind: KindTable, name: t.Name, database: n.database, schema: n.schema, table: t, parent: n})
	}
	m.scroll()
}

func (m Model) handleKey(kp tea.KeyPressMsg) (Model, tea.Cmd) {
	vis := m.visible()
	n := m.selected()
	switch {
	case key.Matches(kp, Keys.Up):
		m.cursor = max(m.cursor-1, 0)
	case key.Matches(kp, Keys.Down):
		m.cursor = min(m.cursor+1, max(len(vis)-1, 0))
	case key.Matches(kp, Keys.Refresh):
		switch {
		case n == nil:
			return m, m.load(nil)
		case n.kind == KindTable:
			return m, m.load(n.parent)
		default:
			n.expanded = true
			return m, m.load(n)
		}
	case n == nil:
		return m, nil
	case key.Matches(kp, Keys.Open):
		if n.kind == KindTable {
			id := m.id
			return m, func() tea.Msg {
				return OpenTableMsg{ID: id, Database: n.database, Schema: n.schema, Table: n.name}
			}
		}
		cmd := m.toggle(n)
		if n.kind == KindDatabase {
			return m, tea.Batch(cmd, m.useDatabase(n.database))
		}
		return m, cmd
	case key.Matches(kp, Keys.Expand):
		if n.kind != KindTable && !n.expanded {
			return m, m.toggle(n)
		}
	case key.Matches(kp, Keys.Collapse):
		if n.expanded {
			n.expanded = false
		} else if n.parent != nil {
			for i, v := range vis {
				if v == n.parent {
					m.cursor = i
				}
			}
		}
	}
	m.scroll()
	return m, nil
}

func (m *Model) useDatabase(name string) tea.Cmd {
	id := m.id
	return func() tea.Msg { return UseDatabaseMsg{ID: id, Database: name} }
}

func (m *Model) toggle(n *node) tea.Cmd {
	if n.kind == KindTable {
		return nil
	}
	n.expanded = !n.expanded
	if n.expanded && !n.loaded && !n.loading {
		return m.load(n)
	}
	return nil
}

func (m *Model) scroll() {
	total := len(m.visible())
	m.cursor = min(max(m.cursor, 0), max(total-1, 0))
	h := max(m.height, 1)
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

// View renders the tree.
func (m Model) View(focused bool) string {
	if m.rootErr != nil {
		return styles.Error.Render("✗ "+m.rootErr.Error()) + "\n\n" + styles.Dim.Render("press r to retry")
	}
	if m.rootLoading && len(m.roots) == 0 {
		return styles.Dim.Render("loading databases…")
	}
	vis := m.visible()
	end := min(m.offset+max(m.height, 1), len(vis))
	lines := make([]string, 0, end-m.offset)
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.renderNode(vis[i], i == m.cursor, focused))
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderNode(n *node, selected, focused bool) string {
	indent := strings.Repeat("  ", n.depth())
	icon := "  "
	if n.kind != KindTable {
		icon = "▸ "
		if n.expanded {
			icon = "▾ "
		}
	}
	label := n.name
	suffix := ""
	switch {
	case n.loading:
		suffix = " loading…"
	case n.err != nil:
		suffix = " ✗ " + n.err.Error()
	case n.loaded && len(n.children) == 0 && n.expanded:
		suffix = " (empty)"
	case n.kind == KindTable && n.table.Kind != db.KindTable:
		suffix = " " + string(n.table.Kind)
	}

	text := styles.Truncate(indent+icon+label, m.width)
	if selected {
		style := styles.Bold
		if focused {
			style = styles.Selected
		}
		text = style.Render(text)
	}
	rest := m.width - len([]rune(indent+icon+label))
	if suffix != "" && rest > 2 {
		s := styles.Truncate(suffix, rest)
		if n.err != nil {
			text += styles.Error.Render(s)
		} else {
			text += styles.Dim.Render(s)
		}
	}
	return text
}

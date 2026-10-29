// Package results renders query output: a scrollable grid for tabular
// results, or a message / error for everything else.
package results

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/tui/styles"
)

const (
	maxColWidth = 40
	minColWidth = 4
	widthSample = 500
	sepWidth    = 3 // " │ "
)

type mode int

const (
	modeEmpty mode = iota
	modeMessage
	modeError
	modeGrid
)

// KeyMap lists the grid bindings (also used for the help bar).
type KeyMap struct {
	Up, Down, Left, Right, PageUp, PageDown, Top, Bottom, Home, End, Open, Copy, Close key.Binding
}

var Keys = KeyMap{
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
	Right:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
	PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
	PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
	Top:      key.NewBinding(key.WithKeys("g", "ctrl+home"), key.WithHelp("g", "first row")),
	Bottom:   key.NewBinding(key.WithKeys("G", "ctrl+end"), key.WithHelp("G", "last row")),
	Home:     key.NewBinding(key.WithKeys("home", "0"), key.WithHelp("0", "first column")),
	End:      key.NewBinding(key.WithKeys("end", "$"), key.WithHelp("$", "last column")),
	Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "view cell")),
	Copy:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy cell")),
	Close:    key.NewBinding(key.WithKeys("esc", "q", "enter"), key.WithHelp("esc", "close")),
}

// Model is the results pane.
type Model struct {
	mode    mode
	message string
	result  *db.Result
	widths  []int

	row, col       int // cursor
	rowOff, colOff int // scroll offsets

	detail     bool
	detailView viewport.Model

	width, height int
}

// New returns an empty results pane.
func New() Model {
	return Model{detailView: viewport.New()}
}

// SetSize sets the inner size of the pane.
func (m *Model) SetSize(w, h int) {
	m.width, m.height = w, h
	m.detailView.SetWidth(w)
	m.detailView.SetHeight(max(h-1, 1))
	m.clamp()
}

// SetResult shows a query result.
func (m *Model) SetResult(r *db.Result) {
	m.result = r
	m.detail = false
	m.row, m.col, m.rowOff, m.colOff = 0, 0, 0, 0
	if !r.HasRows() {
		m.mode = modeMessage
		m.message = r.CommandTag
		return
	}
	m.mode = modeGrid
	m.widths = columnWidths(r)
}

// SetMessage shows a plain informational message.
func (m *Model) SetMessage(s string) {
	m.mode, m.message, m.result, m.detail = modeMessage, s, nil, false
}

// SetError shows an error message.
func (m *Model) SetError(s string) {
	m.mode, m.message, m.result, m.detail = modeError, s, nil, false
}

// Result returns the result being displayed, if any.
func (m Model) Result() *db.Result { return m.result }

// InDetail reports whether the cell viewer is open (it captures esc).
func (m Model) InDetail() bool { return m.detail }

// Title is the panel title, e.g. "Results · 42 rows · 12ms".
func (m Model) Title() string {
	r := m.result
	if r == nil {
		return "Results"
	}
	parts := []string{"Results"}
	if r.HasRows() {
		n := fmt.Sprintf("%d rows", r.TotalRows)
		if r.Truncated {
			n = fmt.Sprintf("showing %d of %d rows", len(r.Rows), r.TotalRows)
		}
		parts = append(parts, n)
	}
	parts = append(parts, formatDuration(r.Duration))
	return strings.Join(parts, " · ")
}

// Update handles navigation keys. The session only forwards keys when the
// pane is focused.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok || m.mode != modeGrid {
		return m, nil
	}
	if m.detail {
		if key.Matches(kp, Keys.Close) {
			m.detail = false
			return m, nil
		}
		if key.Matches(kp, Keys.Copy) {
			return m, tea.SetClipboard(m.cell().Value)
		}
		var cmd tea.Cmd
		m.detailView, cmd = m.detailView.Update(msg)
		return m, cmd
	}

	rows := len(m.result.Rows)
	page := max(m.visibleRows()-1, 1)
	switch {
	case key.Matches(kp, Keys.Up):
		m.row--
	case key.Matches(kp, Keys.Down):
		m.row++
	case key.Matches(kp, Keys.Left):
		m.col--
	case key.Matches(kp, Keys.Right):
		m.col++
	case key.Matches(kp, Keys.PageUp):
		m.row -= page
	case key.Matches(kp, Keys.PageDown):
		m.row += page
	case key.Matches(kp, Keys.Top):
		m.row = 0
	case key.Matches(kp, Keys.Bottom):
		m.row = rows - 1
	case key.Matches(kp, Keys.Home):
		m.col = 0
	case key.Matches(kp, Keys.End):
		m.col = len(m.result.Columns) - 1
	case key.Matches(kp, Keys.Open):
		if rows > 0 {
			m.detail = true
			m.detailView.SetContent(pretty(m.cell()))
			m.detailView.GotoTop()
		}
	case key.Matches(kp, Keys.Copy):
		if rows > 0 {
			return m, tea.SetClipboard(m.cell().Value)
		}
	}
	m.clamp()
	return m, nil
}

func (m Model) cell() db.Cell {
	if m.result == nil || m.row >= len(m.result.Rows) || m.col >= len(m.result.Columns) {
		return db.Cell{}
	}
	return m.result.Rows[m.row][m.col]
}

func (m *Model) visibleRows() int { return max(m.height-2, 1) } // 2 header lines

func (m *Model) clamp() {
	if m.mode != modeGrid || m.result == nil {
		return
	}
	m.row = min(max(m.row, 0), max(len(m.result.Rows)-1, 0))
	m.col = min(max(m.col, 0), len(m.result.Columns)-1)

	vis := m.visibleRows()
	if m.row < m.rowOff {
		m.rowOff = m.row
	}
	if m.row >= m.rowOff+vis {
		m.rowOff = m.row - vis + 1
	}

	if m.col < m.colOff {
		m.colOff = m.col
	}
	for m.colOff < m.col && !m.colFits(m.colOff, m.col) {
		m.colOff++
	}
}

// colFits reports whether columns from..to fit in the pane width.
func (m *Model) colFits(from, to int) bool {
	w := m.gutterWidth()
	for i := from; i <= to; i++ {
		w += m.widths[i]
		if i > from {
			w += sepWidth
		}
	}
	return w <= m.width
}

func (m *Model) gutterWidth() int {
	return len(strconv.Itoa(len(m.result.Rows))) + 1
}

// View renders the pane content (without the border).
func (m Model) View() string {
	switch m.mode {
	case modeEmpty:
		return styles.Dim.Render("Run a query with ctrl+e (or F5) to see results here.")
	case modeMessage:
		return styles.Success.Render(m.message)
	case modeError:
		return styles.Error.Render(m.message)
	}
	if m.detail {
		c := m.result.Columns[m.col]
		head := styles.Title.Render(fmt.Sprintf("%s (%s) · row %d", c.Name, c.Type, m.row+1)) +
			styles.Dim.Render("  esc close · y copy")
		return head + "\n" + m.detailView.View()
	}
	return m.grid()
}

func (m Model) grid() string {
	r := m.result
	gw := m.gutterWidth()
	sep := " " + styles.Dim.Render("│") + " "

	// Which columns are visible.
	last := m.colOff
	for last+1 < len(r.Columns) && m.colFits(m.colOff, last+1) {
		last++
	}

	var b strings.Builder
	header := func(text func(c db.Column) string, style lipgloss.Style) {
		b.WriteString(strings.Repeat(" ", gw))
		for i := m.colOff; i <= last; i++ {
			if i > m.colOff {
				b.WriteString(sep)
			}
			b.WriteString(style.Render(pad(styles.Truncate(text(r.Columns[i]), m.widths[i]), m.widths[i])))
		}
		b.WriteString("\n")
	}
	header(func(c db.Column) string { return c.Name }, styles.Bold)
	header(func(c db.Column) string { return c.Type }, styles.Dim)

	end := min(m.rowOff+m.visibleRows(), len(r.Rows))
	for ri := m.rowOff; ri < end; ri++ {
		num := styles.Dim.Render(pad(strconv.Itoa(ri+1), gw-1) + " ")
		var line strings.Builder
		for i := m.colOff; i <= last; i++ {
			if i > m.colOff {
				line.WriteString(sep)
			}
			cell := r.Rows[ri][i]
			text := pad(styles.Truncate(display(cell), m.widths[i]), m.widths[i])
			switch {
			case ri == m.row && i == m.col:
				text = styles.Cursor.Render(text)
			case cell.Null:
				text = styles.Null.Render(text)
			}
			line.WriteString(text)
		}
		row := line.String()
		if ri == m.row {
			row = lipgloss.NewStyle().Background(styles.Highlight).Render(row)
		}
		b.WriteString(num + row)
		if ri < end-1 {
			b.WriteString("\n")
		}
	}
	if len(r.Rows) == 0 {
		b.WriteString(styles.Dim.Render("(no rows)"))
	}
	return b.String()
}

func columnWidths(r *db.Result) []int {
	w := make([]int, len(r.Columns))
	for i, c := range r.Columns {
		w[i] = max(lipgloss.Width(c.Name), lipgloss.Width(c.Type), minColWidth)
	}
	for ri := 0; ri < len(r.Rows) && ri < widthSample; ri++ {
		for i, cell := range r.Rows[ri] {
			w[i] = max(w[i], lipgloss.Width(display(cell)))
		}
	}
	for i := range w {
		w[i] = min(w[i], maxColWidth)
	}
	return w
}

// display renders a cell on a single line.
func display(c db.Cell) string {
	if c.Null {
		return "∅"
	}
	v := c.Value
	if r := []rune(v); len(r) > 4*maxColWidth {
		v = string(r[:4*maxColWidth])
	}
	return strings.NewReplacer("\r\n", "↵", "\n", "↵", "\t", " ").Replace(v)
}

// pretty renders the full value for the cell viewer, indenting JSON.
func pretty(c db.Cell) string {
	if c.Null {
		return "NULL"
	}
	v := strings.TrimSpace(c.Value)
	if (strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")) && json.Valid([]byte(v)) {
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(v), "", "  ") == nil {
			return buf.String()
		}
	}
	return c.Value
}

func pad(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

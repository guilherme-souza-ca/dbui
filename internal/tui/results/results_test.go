package results

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermesouza/dbui/internal/db"
)

func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func wide(cols, rows int) *db.Result {
	r := &db.Result{TotalRows: rows}
	for c := range cols {
		r.Columns = append(r.Columns, db.Column{Name: "column_" + string(rune('a'+c)), Type: "text"})
	}
	for i := range rows {
		row := make([]db.Cell, cols)
		for c := range cols {
			row[c] = db.Cell{Value: strings.Repeat(string(rune('a'+c)), 12) + string(rune('0'+i%10))}
		}
		r.Rows = append(r.Rows, row)
	}
	return r
}

func TestHorizontalScrollKeepsCursorVisible(t *testing.T) {
	m := New()
	m.SetSize(50, 10)
	m.SetResult(wide(8, 3))
	if strings.Contains(ansi.Strip(m.View()), "column_h") {
		t.Fatal("last column should not fit initially")
	}
	m, _ = m.Update(press("$"))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "column_h") || strings.Contains(view, "column_a") {
		t.Fatalf("after $ the last column should be visible:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 50 {
			t.Fatalf("line wider than pane: %q", line)
		}
	}
}

func TestVerticalScroll(t *testing.T) {
	m := New()
	m.SetSize(80, 6) // 4 visible rows
	m.SetResult(wide(2, 20))
	m, _ = m.Update(press("G"))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "20 ") || strings.Contains(view, " 1 ") {
		t.Fatalf("last row should be visible:\n%s", view)
	}
}

func TestCellDetailPrettyPrintsJSON(t *testing.T) {
	m := New()
	m.SetSize(80, 10)
	m.SetResult(&db.Result{
		Columns:   []db.Column{{Name: "doc", Type: "jsonb"}},
		Rows:      [][]db.Cell{{{Value: `{"a": 1, "b": [1, 2]}`}}},
		TotalRows: 1,
	})
	m, _ = m.Update(press("enter"))
	if !m.InDetail() {
		t.Fatal("enter should open the cell viewer")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "\"b\": [\n") && !strings.Contains(view, "  \"a\": 1,") {
		t.Fatalf("json not indented:\n%s", view)
	}
	m, _ = m.Update(press("esc"))
	if m.InDetail() {
		t.Fatal("esc should close the viewer")
	}
}

func TestTitleAndModes(t *testing.T) {
	m := New()
	m.SetSize(80, 10)
	r := wide(1, 5)
	r.TotalRows, r.Truncated = 1500, true
	m.SetResult(r)
	if got := m.Title(); !strings.HasPrefix(got, "Results · showing 5 of 1500 rows") {
		t.Fatalf("title = %q", got)
	}
	m.SetResult(&db.Result{CommandTag: "UPDATE 3"})
	if ansi.Strip(m.View()) != "UPDATE 3" {
		t.Fatalf("command tag view = %q", m.View())
	}
	m.SetError("ERROR: boom")
	if !strings.Contains(ansi.Strip(m.View()), "boom") {
		t.Fatal("error not rendered")
	}
}

func TestDisplayFlattensNewlinesAndNull(t *testing.T) {
	if display(db.Cell{Null: true}) != "∅" {
		t.Fatal("null marker")
	}
	if display(db.Cell{Value: "a\nb\tc"}) != "a↵b c" {
		t.Fatal("newlines/tabs should be flattened")
	}
}

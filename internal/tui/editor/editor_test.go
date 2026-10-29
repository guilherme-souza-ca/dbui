package editor

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestOffsetAndPosition(t *testing.T) {
	v := "ab\nçd\n\nx"
	cases := []struct{ row, col, off int }{{0, 0, 0}, {0, 2, 2}, {1, 1, 4}, {2, 0, 6}, {3, 1, 8}}
	for _, c := range cases {
		if got := offsetOf(v, c.row, c.col); got != c.off {
			t.Errorf("offsetOf(%d,%d) = %d, want %d", c.row, c.col, got, c.off)
		}
		if r, col := position(v, c.off); r != c.row || col != c.col {
			t.Errorf("position(%d) = %d,%d", c.off, r, col)
		}
	}
}

func TestTargetAndJump(t *testing.T) {
	m := New(1)
	m.SetSize(80, 10)
	m.Focus()
	m.SetValue("select 1;\nselect 2;\nselect 3;")

	m.JumpTo(12) // inside "select 2"
	if sql, off, ok := m.Target(); !ok || sql != "select 2" || off != 10 {
		t.Fatalf("Target() = %q, %d, %v", sql, off, ok)
	}
	if row, col := m.CursorPosition(); row != 1 || col != 2 {
		t.Fatalf("cursor at %d:%d", row, col)
	}

	// Select "select 3" with shift+right from the start of line 3.
	m.JumpTo(20)
	for range 8 {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	}
	if sql, off, ok := m.Target(); !ok || sql != "select 3" || off != 20 {
		t.Fatalf("selection Target() = %q, %d, %v", sql, off, ok)
	}
}

func TestTargetEmpty(t *testing.T) {
	m := New(1)
	if _, _, ok := m.Target(); ok {
		t.Fatal("empty editor has no target")
	}
}

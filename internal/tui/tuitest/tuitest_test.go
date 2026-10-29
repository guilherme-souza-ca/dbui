package tuitest

import "testing"

func TestKeyStrings(t *testing.T) {
	for _, k := range []string{"enter", "tab", "shift+tab", "esc", "up", "f5", "ctrl+e", "ctrl+c", "ctrl+o", "alt+2", "alt+]", "j", "G", "y"} {
		if got := Key(k).String(); got != k {
			t.Errorf("Key(%q).String() = %q", k, got)
		}
	}
}

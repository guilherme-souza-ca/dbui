package sqlsplit

import "testing"

func texts(stmts []Statement) []string {
	out := make([]string, len(stmts))
	for i, s := range stmts {
		out[i] = s.Text
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single without semicolon", "select 1", []string{"select 1"}},
		{"two statements", "select 1;\nselect 2;", []string{"select 1", "select 2"}},
		{"empty statements skipped", " ; ;select 1;; ", []string{"select 1"}},
		{"string with semicolon", "select 'a;b'; select 2", []string{"select 'a;b'", "select 2"}},
		{"escaped quote", "select 'it''s;'; select 2", []string{"select 'it''s;'", "select 2"}},
		{"quoted identifier", `select "a;b" from t; select 2`, []string{`select "a;b" from t`, "select 2"}},
		{"line comment", "select 1 -- x;y\n; select 2", []string{"select 1 -- x;y", "select 2"}},
		{"nested block comment", "select /* a /* ; */ ; */ 1; select 2", []string{"select /* a /* ; */ ; */ 1", "select 2"}},
		{"dollar quote", "do $$ begin; end $$; select 2", []string{"do $$ begin; end $$", "select 2"}},
		{"tagged dollar quote", "create function f() returns int as $fn$ select 1; $fn$ language sql; select 2",
			[]string{"create function f() returns int as $fn$ select 1; $fn$ language sql", "select 2"}},
		{"positional param is not a quote", "select $1; select 2", []string{"select $1", "select 2"}},
		{"unicode", "select 'çã'; select 2", []string{"select 'çã'", "select 2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := texts(Split(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("stmt %d: got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestSplitOffsets(t *testing.T) {
	src := "  select 'é';\n\nselect 2"
	stmts := Split(src)
	if len(stmts) != 2 {
		t.Fatalf("got %d statements", len(stmts))
	}
	r := []rune(src)
	for _, st := range stmts {
		if got := string(r[st.Start : st.Start+len([]rune(st.Text))]); got != st.Text {
			t.Fatalf("offset mismatch: %q vs %q", got, st.Text)
		}
	}
	if stmts[0].Start != 2 {
		t.Fatalf("start = %d, want 2", stmts[0].Start)
	}
}

func TestKeyword(t *testing.T) {
	cases := map[string]string{
		"select 1":                  "SELECT",
		"  -- c\n /* x */ begin;":   "BEGIN",
		"(select 1) union select 2": "SELECT",
		"":                          "",
		"-- only a comment":         "",
		"\tStart transaction":       "START",
	}
	for in, want := range cases {
		if got := Keyword(in); got != want {
			t.Errorf("Keyword(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAt(t *testing.T) {
	src := "select 1;\nselect 2;\n\n"
	cases := []struct {
		pos  int
		want string
	}{
		{0, "select 1"},
		{4, "select 1"},
		{9, "select 1"}, // right after the semicolon
		{12, "select 2"},
		{len([]rune(src)), "select 2"}, // trailing blank lines pick the previous one
	}
	for _, c := range cases {
		st, ok := At(src, c.pos)
		if !ok || st.Text != c.want {
			t.Errorf("At(%d) = %q, %v; want %q", c.pos, st.Text, ok, c.want)
		}
	}
	if _, ok := At("  ", 0); ok {
		t.Error("At on blank buffer should report false")
	}
}

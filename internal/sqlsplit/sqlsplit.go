// Package sqlsplit splits a SQL buffer into individual statements, honoring
// PostgreSQL quoting rules so that semicolons inside strings, quoted
// identifiers, comments and dollar-quoted bodies are not treated as separators.
//
// All offsets are rune (character) offsets, which is also what PostgreSQL uses
// for error positions.
package sqlsplit

import "strings"

// Statement is a single statement found in a buffer.
type Statement struct {
	Text  string // statement text, trimmed, without the trailing semicolon
	Start int    // rune offset of the first character of Text in the buffer
	End   int    // rune offset just past the terminating semicolon (or buffer end)
}

// Split returns the non-empty statements in src in order.
func Split(src string) []Statement {
	r := []rune(src)
	var out []Statement
	start := 0
	emit := func(end, next int) {
		if st, ok := trimmed(r, start, end); ok {
			st.End = next
			out = append(out, st)
		}
		start = next
	}

	for i := 0; i < len(r); {
		switch {
		case r[i] == '\'':
			i = skipQuoted(r, i, '\'')
		case r[i] == '"':
			i = skipQuoted(r, i, '"')
		case r[i] == '-' && at(r, i+1) == '-':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		case r[i] == '/' && at(r, i+1) == '*':
			i = skipBlockComment(r, i)
		case r[i] == '$':
			if tag, ok := dollarTag(r, i); ok {
				i = skipDollar(r, i, tag)
			} else {
				i++
			}
		case r[i] == ';':
			emit(i, i+1)
			i++
		default:
			i++
		}
	}
	emit(len(r), len(r))
	return out
}

// At returns the statement that contains the rune offset pos. When pos is in
// the whitespace between statements (or after the last one), the closest
// preceding statement is returned, falling back to the following one.
func At(src string, pos int) (Statement, bool) {
	stmts := Split(src)
	if len(stmts) == 0 {
		return Statement{}, false
	}
	best := -1
	for i, st := range stmts {
		if pos >= st.Start && pos <= st.End {
			return st, true
		}
		if st.Start <= pos {
			best = i
		}
	}
	if best < 0 {
		best = 0
	}
	return stmts[best], true
}

// Keyword returns the first word of a statement in upper case, skipping
// leading whitespace and comments ("" if there is none).
func Keyword(stmt string) string {
	r := []rune(stmt)
	i := 0
	for i < len(r) {
		switch {
		case r[i] == ' ' || r[i] == '\t' || r[i] == '\n' || r[i] == '\r' || r[i] == '(':
			i++
		case r[i] == '-' && at(r, i+1) == '-':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		case r[i] == '/' && at(r, i+1) == '*':
			i = skipBlockComment(r, i)
		default:
			j := i
			for j < len(r) && isIdent(r[j]) {
				j++
			}
			return strings.ToUpper(string(r[i:j]))
		}
	}
	return ""
}

func at(r []rune, i int) rune {
	if i < len(r) {
		return r[i]
	}
	return 0
}

// trimmed returns the statement in r[start:end] without surrounding
// whitespace. Statements made only of whitespace are reported as empty;
// comments are kept because they are part of what the user wrote.
func trimmed(r []rune, start, end int) (Statement, bool) {
	text := string(r[start:end])
	t := strings.TrimSpace(text)
	if t == "" {
		return Statement{}, false
	}
	lead := len([]rune(text)) - len([]rune(strings.TrimLeft(text, " \t\r\n\f\v")))
	return Statement{Text: t, Start: start + lead}, true
}

// skipQuoted skips a '...' or "..." token starting at i. Doubled quote
// characters are escapes. Returns the index just past the closing quote.
func skipQuoted(r []rune, i int, q rune) int {
	i++
	for i < len(r) {
		if r[i] == q {
			if at(r, i+1) == q {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

// skipBlockComment skips a (possibly nested) /* */ comment starting at i.
func skipBlockComment(r []rune, i int) int {
	depth := 0
	for i < len(r) {
		switch {
		case r[i] == '/' && at(r, i+1) == '*':
			depth++
			i += 2
		case r[i] == '*' && at(r, i+1) == '/':
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return i
}

// dollarTag reports whether a dollar-quote opener ($$ or $tag$) starts at i.
func dollarTag(r []rune, i int) (string, bool) {
	// A '$' directly after an identifier character is part of the identifier
	// (e.g. "a$b") or a positional parameter ($1), not a quote.
	if i > 0 && isIdent(r[i-1]) {
		return "", false
	}
	j := i + 1
	for j < len(r) && r[j] != '$' {
		if !isIdent(r[j]) || (j == i+1 && r[j] >= '0' && r[j] <= '9') {
			return "", false
		}
		j++
	}
	if j >= len(r) {
		return "", false
	}
	return string(r[i : j+1]), true
}

func skipDollar(r []rune, i int, tag string) int {
	tr := []rune(tag)
	i += len(tr)
	for i < len(r) {
		if r[i] == '$' && hasPrefix(r[i:], tr) {
			return i + len(tr)
		}
		i++
	}
	return i
}

func hasPrefix(r, p []rune) bool {
	if len(r) < len(p) {
		return false
	}
	for k := range p {
		if r[k] != p[k] {
			return false
		}
	}
	return true
}

func isIdent(c rune) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c > 127
}

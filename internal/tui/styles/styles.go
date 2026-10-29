// Package styles holds the shared Lip Gloss theme.
package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	Accent    = lipgloss.Color("12") // bright blue
	Danger    = lipgloss.Color("9")  // bright red
	Warn      = lipgloss.Color("11") // yellow
	OK        = lipgloss.Color("10") // green
	Muted     = lipgloss.Color("8")  // grey
	Subtle    = lipgloss.Color("7")
	Highlight = lipgloss.Color("237")

	Bold     = lipgloss.NewStyle().Bold(true)
	Dim      = lipgloss.NewStyle().Foreground(Muted)
	Error    = lipgloss.NewStyle().Foreground(Danger)
	Success  = lipgloss.NewStyle().Foreground(OK)
	Title    = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	Selected = lipgloss.NewStyle().Background(Highlight).Bold(true)
	Cursor   = lipgloss.NewStyle().Reverse(true)
	Null     = lipgloss.NewStyle().Foreground(Muted).Italic(true)
)

// Panel renders content inside a rounded border with a title embedded in
// the top edge. width/height are the outer dimensions. A non-nil accent
// overrides the focused border color (used to flag production connections).
func Panel(title, content string, width, height int, focused bool, accent color.Color) string {
	if width < 4 || height < 3 {
		return ""
	}
	border := lipgloss.RoundedBorder()
	var c color.Color = Muted
	if focused {
		c = Accent
	}
	if focused && accent != nil {
		c = accent
	}
	bs := lipgloss.NewStyle().Foreground(c)

	innerW, innerH := width-2, height-2
	body := lipgloss.NewStyle().Width(innerW).Height(innerH).MaxWidth(innerW).MaxHeight(innerH).Render(content)

	t := " " + title + " "
	if focused {
		t = lipgloss.NewStyle().Bold(true).Foreground(c).Render(t)
	}
	tw := lipgloss.Width(t)
	if tw > innerW-1 {
		t = Truncate(" "+title+" ", innerW-1)
		tw = lipgloss.Width(t)
	}
	top := bs.Render(border.TopLeft+border.Top) + t + bs.Render(repeat(border.Top, innerW-1-tw)+border.TopRight)

	lines := splitLines(body)
	out := make([]string, 0, height)
	out = append(out, top)
	side := bs.Render(border.Left)
	sideR := bs.Render(border.Right)
	for _, l := range lines {
		out = append(out, side+l+sideR)
	}
	out = append(out, bs.Render(border.BottomLeft+repeat(border.Bottom, innerW)+border.BottomRight))
	return lipgloss.JoinVertical(lipgloss.Left, out...)
}

// Truncate cuts s to at most w display cells, adding an ellipsis.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

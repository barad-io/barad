package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Seg is a run of text in one style. Layout works on segments so a surface
// colour can be applied to every run: an ANSI reset inside a pre-styled string
// would otherwise punch holes in filled backgrounds.
type Seg struct {
	Text  string
	Style lipgloss.Style
}

// Line is one row of segments.
type Line []Seg

func S(text string, style lipgloss.Style) Seg { return Seg{text, style} }

func (l Line) Width() int {
	w := 0
	for _, s := range l {
		w += ansi.StringWidth(s.Text)
	}
	return w
}

// Paint renders a line on bg (nil keeps the terminal's background, which the
// app sets to the theme's canvas), padded or cut to width when width > 0.
func (l Line) Paint(bg color.Color, width int) string {
	var b strings.Builder
	used := 0
	for _, s := range l {
		text := s.Text
		if width > 0 {
			room := width - used
			if room <= 0 {
				break
			}
			if ansi.StringWidth(text) > room {
				text = ansi.Truncate(text, room, "")
			}
		}
		used += ansi.StringWidth(text)
		if strings.Contains(text, "\x1b") { // drawn already (a picture in half blocks)
			b.WriteString(text)
			continue
		}
		st := s.Style
		if _, none := st.GetBackground().(lipgloss.NoColor); none && bg != nil {
			st = st.Background(bg)
		}
		b.WriteString(st.Render(text))
	}
	if width > used {
		pad := lipgloss.NewStyle()
		if bg != nil {
			pad = pad.Background(bg)
		}
		b.WriteString(pad.Render(strings.Repeat(" ", width-used)))
	}
	return b.String()
}

// Wrap word-wraps segments to width, keeping each word's style. Newlines in
// segment text break lines.
func Wrap(segs []Seg, width int) []Line {
	width = max(width, 1)
	var lines []Line
	var cur Line
	curW := 0
	flush := func() {
		lines = append(lines, cur)
		cur, curW = nil, 0
	}
	for _, s := range segs {
		for pi, part := range strings.Split(s.Text, "\n") {
			if pi > 0 {
				flush()
			}
			for _, tok := range splitKeepSpaces(part) {
				w := ansi.StringWidth(tok)
				if tok == " " && curW == 0 {
					continue
				}
				if curW+w > width && curW > 0 {
					flush()
					if tok == " " {
						continue
					}
				}
				for w > width { // a word longer than the line
					head := ansi.Truncate(tok, width, "")
					cur = append(cur, Seg{head, s.Style})
					flush()
					tok = tok[len(head):]
					w = ansi.StringWidth(tok)
				}
				cur = append(cur, Seg{tok, s.Style})
				curW += w
			}
		}
	}
	if len(cur) > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

func splitKeepSpaces(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if r == ' ' {
			if i > start {
				out = append(out, s[start:i])
			}
			out = append(out, " ")
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Indent prefixes every line with n spaces.
func Indent(lines []Line, n int) []Line {
	out := make([]Line, len(lines))
	for i, l := range lines {
		out[i] = append(Line{S(strings.Repeat(" ", n), lipgloss.NewStyle())}, l...)
	}
	return out
}

// PadRight pads s to w cells, or cuts it with an ellipsis.
func PadRight(s string, w int) string {
	if d := w - ansi.StringWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return ansi.Truncate(s, w, "…")
}

// on paints a line's unstyled backgrounds onto a surface.
func on(l Line, bg color.Color) Line {
	out := make(Line, len(l))
	for i, s := range l {
		st := s.Style
		if _, none := st.GetBackground().(lipgloss.NoColor); none {
			st = st.Background(bg)
		}
		out[i] = S(s.Text, st)
	}
	return out
}

// gap is spaces that keep the canvas.
func gap(n int) Seg { return S(strings.Repeat(" ", max(0, n)), lipgloss.NewStyle()) }

// spread puts right at the far end of a line w cells wide.
func spread(left, right Line, w int) Line {
	return append(append(left, gap(w-left.Width()-right.Width())), right...)
}

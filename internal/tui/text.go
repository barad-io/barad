package tui

import (
	"image/color"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Seg is a run of text in one style.
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

// Paint renders a line on bg (nil keeps the terminal's native background),
// padded or cut to width when width > 0.
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
		if strings.Contains(text, "\x1b") {
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

// Wrap word-wraps segments to width, preserving each word's style.
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
				for w > width {
					head := ansi.Truncate(tok, width, "")
					if head == "" {
						break
					}
					cur = append(cur, Seg{head, s.Style})
					flush()
					tok = tok[len(head):]
					w = ansi.StringWidth(tok)
				}
				if tok != "" {
					cur = append(cur, Seg{tok, s.Style})
					curW += w
				}
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
	pad := strings.Repeat(" ", max(0, n))
	for i, l := range lines {
		out[i] = append(Line{S(pad, lipgloss.NewStyle())}, l...)
	}
	return out
}

// PadRight pads s to w cells, or truncates it with an ellipsis.
func PadRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if d := w - ansi.StringWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return ansi.Truncate(s, w, "…")
}

// ruled prefixes lines with a thin vertical accent bar.
func ruled(lines []Line, c color.Color) []Line {
	out := make([]Line, len(lines))
	for i, l := range lines {
		out[i] = append(Line{S("▏ ", Fg(c))}, l...)
	}
	return out
}

func gap(n int) Seg { return S(strings.Repeat(" ", max(0, n)), lipgloss.NewStyle()) }

// spread puts right at the far end of a line w cells wide.
func spread(left, right Line, w int) Line {
	g := w - left.Width() - right.Width()
	if g < 1 {
		g = 1
	}
	return append(append(left, gap(g)), right...)
}

// linked attaches an OSC 8 hyperlink when address is a safe web or mailto URL.
func linked(st lipgloss.Style, address string) lipgloss.Style {
	if !openable(address) {
		return st
	}
	return st.Hyperlink(address)
}

func openable(address string) bool {
	u, err := url.Parse(address)
	return err == nil && (u.Scheme == "https" || (u.Scheme == "http" && u.Host != "") || u.Scheme == "mailto")
}

var bareURL = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+[^\s<>"'.,;:!?)\]}` + "`" + `]`)

// linkify converts bare URLs in plain text into OSC 8 hyperlinks.
func linkify(text string, st lipgloss.Style) []Seg {
	var out []Seg
	last := 0
	for _, m := range bareURL.FindAllStringIndex(text, -1) {
		out = append(out, S(text[last:m[0]], st), S(text[m[0]:m[1]], linked(st.Underline(true), text[m[0]:m[1]])))
		last = m[1]
	}
	return append(out, S(text[last:], st))
}

// OpenURL hands a URL to the system's browser. Tests replace it.
var OpenURL = func(address string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", address).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address).Start()
	}
	return exec.Command("xdg-open", address).Start()
}

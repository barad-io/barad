package tui

import (
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// ---------------------------------------------------------------- links

// linked makes text in st open address when clicked: an OSC 8 hyperlink,
// which the terminal shows and the app's own click (below) follows. Only web
// and mail addresses link; an agent can write anything.
func linked(st lipgloss.Style, address string) lipgloss.Style {
	if !openable(address) {
		return st
	}
	return st.Hyperlink(address)
}

func openable(address string) bool {
	u, err := url.Parse(address)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http" && u.Host != "" || u.Scheme == "mailto")
}

var bareURL = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+[^\s<>"'.,;:!?)\]}` + "`" + `]`)

// linkify is plain text as segments in st, its web addresses linked.
func linkify(text string, st lipgloss.Style) []Seg {
	var out []Seg
	last := 0
	for _, m := range bareURL.FindAllStringIndex(text, -1) {
		out = append(out, S(text[last:m[0]], st), S(text[m[0]:m[1]], linked(st.Underline(true), text[m[0]:m[1]])))
		last = m[1]
	}
	return append(out, S(text[last:], st))
}

// openURL hands a web address to the system's browser.
func openURL(address string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", address).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address).Start()
	}
	return exec.Command("xdg-open", address).Start()
}

// ---------------------------------------------------------------- selection
//
// The app takes the mouse (for the wheel), so the terminal cannot select;
// the app selects instead. Drag across the screen to select, as in a
// terminal: the selection runs from where you pressed to where you let go,
// and letting go copies it (OSC 52). A click without a drag follows the link
// under it. Any key or scroll clears the selection.

// span is a selection, in screen cells: from where the drag began to where
// it is now.
type span struct {
	ax, ay, bx, by int
	held           bool // the button is still down
}

// ordered is the span from its first cell to its last.
func (s span) ordered() (x0, y0, x1, y1 int) {
	if s.by < s.ay || s.by == s.ay && s.bx < s.ax {
		return s.bx, s.by, s.ax, s.ay
	}
	return s.ax, s.ay, s.bx, s.by
}

// grid is the frame as the terminal will show it: cells with their text,
// style and link.
func (a *app) grid(content string) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(a.w, a.h)
	uv.NewStyledString(content).Draw(buf, buf.Bounds())
	return buf
}

// mouse takes the left button's press, drag and release.
func (a *app) mouse(msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if m.Button != tea.MouseLeft {
			return nil
		}
		a.span = &span{ax: m.X, ay: m.Y, bx: m.X, by: m.Y, held: true}
	case tea.MouseMotionMsg:
		if a.span != nil && a.span.held {
			a.span.bx, a.span.by = m.X, m.Y
		}
	case tea.MouseReleaseMsg:
		if a.span == nil || !a.span.held {
			return nil
		}
		s := *a.span
		s.bx, s.by = m.X, m.Y
		grid := a.grid(a.compose())
		if s.ax == s.bx && s.ay == s.by { // a click
			a.span = nil
			if c := grid.CellAt(m.X, m.Y); c != nil && openable(c.Link.URL) {
				if err := openURL(c.Link.URL); err != nil {
					return a.fail("Could not open the link", err)
				}
				a.say("Opened " + c.Link.URL)
			}
			return nil
		}
		s.held = false
		a.span = &s
		text := selected(grid, s)
		if text == "" {
			a.span = nil
			return nil
		}
		lines := strings.Count(text, "\n") + 1
		if lines == 1 {
			a.say(fmt.Sprintf("Copied %d characters", len([]rune(text))))
		} else {
			a.say(fmt.Sprintf("Copied %d lines", lines))
		}
		return tea.SetClipboard(text)
	}
	return nil
}

// highlight paints the selection over a frame.
func (a *app) highlight(content string) string {
	grid := a.grid(content)
	x0, y0, x1, y1 := a.span.ordered()
	for y := y0; y <= y1 && y < a.h; y++ {
		from, to := 0, a.w-1
		if y == y0 {
			from = x0
		}
		if y == y1 {
			to = x1
		}
		for x := from; x <= to && x < a.w; x++ {
			c := grid.CellAt(x, y)
			if c == nil {
				continue
			}
			if strings.ContainsAny(c.Content, "▄▀▐▌") { // a surface's edge: the whole cell is selected
				c.Content = " "
			}
			c.Style.Fg, c.Style.Bg = Text, Selection
		}
	}
	return grid.Render()
}

// selected is the text a selection covers, the way you would want it
// pasted: surfaces' edges and pictures dropped, each line's end trimmed,
// the indent all its lines share removed, and runs of blank lines folded.
func selected(grid uv.ScreenBuffer, s span) string {
	x0, y0, x1, y1 := s.ordered()
	w, h := grid.Width(), grid.Height()
	var lines []string
	for y := max(0, y0); y <= y1 && y < h; y++ {
		from, to := 0, w-1
		if y == y0 {
			from = x0
		}
		if y == y1 {
			to = x1
		}
		var b strings.Builder
		for x := max(0, from); x <= to && x < w; x++ {
			c := grid.CellAt(x, y)
			switch {
			case c == nil:
				b.WriteByte(' ')
			case c.Content == "":
			case strings.ContainsRune(c.Content, 0x10EEEE) || strings.ContainsAny(c.Content, "▄▀▐▌▍"):
				b.WriteByte(' ')
			default:
				b.WriteString(c.Content)
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	indent := -1
	for i, l := range lines {
		if i == 0 || strings.TrimSpace(l) == "" { // the first line can begin mid-row: it sets no indent
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	var out []string
	for i, l := range lines {
		if i == 0 {
			l = strings.TrimLeft(l, " ")
		} else if len(l) >= indent && indent > 0 {
			l = l[indent:]
		}
		if l == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

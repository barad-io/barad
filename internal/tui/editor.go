package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// Editor is a multi-line text field: the message box, filters and names.
type Editor struct {
	Text []rune
	Pos  int
}

// Key applies a key press to a field width cells wide (up and down move by
// the rows it shows). It returns true when Enter submits non-empty text.
func (e *Editor) Key(k tea.KeyPressMsg, width int) (submit bool) {
	switch k.String() {
	case "enter":
		return strings.TrimSpace(string(e.Text)) != ""
	case "shift+enter", "alt+enter", "ctrl+j":
		e.Insert("\n")
	case "backspace":
		if e.Pos > 0 {
			e.Text = append(e.Text[:e.Pos-1], e.Text[e.Pos:]...)
			e.Pos--
		}
	case "delete", "ctrl+d":
		if e.Pos < len(e.Text) {
			e.Text = append(e.Text[:e.Pos], e.Text[e.Pos+1:]...)
		}
	case "alt+backspace", "ctrl+w":
		start := e.wordLeft()
		e.Text = append(e.Text[:start], e.Text[e.Pos:]...)
		e.Pos = start
	case "left", "ctrl+b":
		e.Pos = max(0, e.Pos-1)
	case "right", "ctrl+f":
		e.Pos = min(len(e.Text), e.Pos+1)
	case "up":
		e.vertical(-1, width)
	case "down":
		e.vertical(1, width)
	case "alt+left", "alt+b", "ctrl+left":
		e.Pos = e.wordLeft()
	case "alt+right", "alt+f", "ctrl+right":
		for e.Pos < len(e.Text) && unicode.IsSpace(e.Text[e.Pos]) {
			e.Pos++
		}
		for e.Pos < len(e.Text) && !unicode.IsSpace(e.Text[e.Pos]) {
			e.Pos++
		}
	case "home", "ctrl+a", "super+left": // the start of the line, as in a shell
		e.Pos = e.lineStart()
	case "end", "ctrl+e", "super+right":
		for e.Pos < len(e.Text) && e.Text[e.Pos] != '\n' {
			e.Pos++
		}
	case "ctrl+home", "super+up":
		e.Pos = 0
	case "ctrl+end", "super+down":
		e.Pos = len(e.Text)
	case "ctrl+u", "super+backspace": // delete to the start of the line
		start := e.lineStart()
		e.Text = append(e.Text[:start], e.Text[e.Pos:]...)
		e.Pos = start
	case "space":
		e.Insert(" ")
	default:
		if k.Text != "" {
			e.Insert(k.Text)
		}
	}
	return false
}

func (e *Editor) wordLeft() int {
	i := e.Pos
	for i > 0 && unicode.IsSpace(e.Text[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.Text[i-1]) {
		i--
	}
	return i
}

func (e *Editor) lineStart() int {
	i := e.Pos
	for i > 0 && e.Text[i-1] != '\n' {
		i--
	}
	return i
}

// vertical moves the cursor dir rows, to the column nearest the one it is
// in; past the first or last row it goes to the start or the end.
func (e *Editor) vertical(dir, width int) {
	rows, cur := e.rows(width)
	to := cur + dir
	switch {
	case to < 0:
		e.Pos = 0
		return
	case to >= len(rows):
		e.Pos = len(e.Text)
		return
	}
	col := cells(e.Text[rows[cur][0]:e.Pos])
	r := rows[to]
	e.Pos = r[0]
	for x := 0; e.Pos < r[1]; e.Pos++ {
		w := cellWidth(e.Text[e.Pos])
		if x+w > col {
			break
		}
		x += w
	}
	if e.Pos == r[1] && to < len(rows)-1 && r[1] == rows[to+1][0] && r[1] > r[0] {
		e.Pos-- // the end of a wrapped row is the start of the next: stay on this one
	}
}

// Insert puts s at the cursor (typing, or a paste).
func (e *Editor) Insert(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	r := []rune(strings.ReplaceAll(s, "\r", "\n"))
	e.Text = append(e.Text[:e.Pos], append(r, e.Text[e.Pos:]...)...)
	e.Pos += len(r)
}

func (e *Editor) Value() string { return string(e.Text) }
func (e *Editor) Reset()        { e.Text, e.Pos = nil, 0 }

// cellWidth is how many cells a rune of the field takes; a tab shows as two.
func cellWidth(r rune) int {
	if r == '\t' {
		return 2
	}
	return runewidth.RuneWidth(r)
}

func cells(rs []rune) int {
	w := 0
	for _, r := range rs {
		w += cellWidth(r)
	}
	return w
}

// rows lays the text out in rows of at most width-1 cells (the last cell
// keeps room for the cursor): broken at newlines and, when a line is too
// long, after its last space that fits. Unlike Wrap it keeps every rune, so
// what shows is what you typed. Each row is a range of Text, without its
// newline; cur is the cursor's row.
func (e *Editor) rows(width int) (rows [][2]int, cur int) {
	width = max(width, 2)
	for start := 0; start <= len(e.Text); {
		end := start
		for end < len(e.Text) && e.Text[end] != '\n' {
			end++
		}
		for s := start; ; {
			i, w, brk := s, 0, -1
			for ; i < end && w+cellWidth(e.Text[i]) <= width-1; i++ {
				w += cellWidth(e.Text[i])
				if e.Text[i] == ' ' {
					brk = i + 1
				}
			}
			if i >= end {
				rows = append(rows, [2]int{s, end})
				break
			}
			switch {
			case brk > s:
				i = brk
			case i == s: // a rune wider than the row
				i++
			}
			rows = append(rows, [2]int{s, i})
			s = i
		}
		start = end + 1
	}
	for r, row := range rows {
		if row[0] <= e.Pos {
			cur = r
		}
	}
	return rows, cur
}

// Lines lays the text out width cells wide with a block cursor, showing at
// most most rows: those around the cursor. placeholder shows when empty.
func (e *Editor) Lines(width, most int, base lipgloss.Style, placeholder string, focused bool) []Line {
	if len(e.Text) == 0 {
		if focused {
			return []Line{{S(" ", SCursor), S(placeholder, SFaint)}}
		}
		return []Line{{S(placeholder, SFaint)}}
	}
	rows, cur := e.rows(width)
	first := 0
	if most > 0 && len(rows) > most {
		first = min(max(0, cur-most+1), len(rows)-most)
		rows = rows[first : first+most]
	}
	show := func(rs []rune) string { return strings.ReplaceAll(string(rs), "\t", "  ") }
	out := make([]Line, len(rows))
	for i, row := range rows {
		text := e.Text[row[0]:row[1]]
		if !focused || first+i != cur {
			out[i] = Line{S(show(text), base)}
			continue
		}
		p := e.Pos - row[0]
		at, after := " ", ""
		if p < len(text) {
			at, after = show(text[p:p+1]), show(text[p+1:])
		}
		out[i] = Line{S(show(text[:p]), base), S(at, SCursor), S(after, base)}
	}
	return out
}

// Fuzzy reports whether every rune of q appears in s in order.
func Fuzzy(q, s string) bool {
	if q == "" {
		return true
	}
	qr := []rune(strings.ToLower(q))
	j := 0
	for _, r := range s {
		if j < len(qr) && unicode.ToLower(r) == qr[j] {
			j++
		}
	}
	return j == len(qr)
}

package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Editor is a multi-line text field: the message box, filters and names.
type Editor struct {
	Text []rune
	Pos  int
}

// Key applies a key press. It returns true when Enter submits non-empty text.
func (e *Editor) Key(k tea.KeyPressMsg) (submit bool) {
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
	case "alt+left", "alt+b":
		e.Pos = e.wordLeft()
	case "alt+right", "alt+f":
		for e.Pos < len(e.Text) && unicode.IsSpace(e.Text[e.Pos]) {
			e.Pos++
		}
		for e.Pos < len(e.Text) && !unicode.IsSpace(e.Text[e.Pos]) {
			e.Pos++
		}
	case "home", "ctrl+a":
		e.Pos = 0
	case "end", "ctrl+e":
		e.Pos = len(e.Text)
	case "ctrl+u":
		e.Text, e.Pos = nil, 0
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

// Insert puts s at the cursor (typing, or a paste).
func (e *Editor) Insert(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	r := []rune(strings.ReplaceAll(s, "\r", "\n"))
	e.Text = append(e.Text[:e.Pos], append(r, e.Text[e.Pos:]...)...)
	e.Pos += len(r)
}

func (e *Editor) Value() string { return string(e.Text) }
func (e *Editor) Reset()        { e.Text, e.Pos = nil, 0 }

// Lines lays the text out at width with a block cursor; placeholder shows
// when empty.
func (e *Editor) Lines(width int, base lipgloss.Style, placeholder string, focused bool) []Line {
	if len(e.Text) == 0 {
		if focused {
			return []Line{{S(" ", SCursor), S(placeholder, SFaint)}}
		}
		return []Line{{S(placeholder, SFaint)}}
	}
	before, after := string(e.Text[:e.Pos]), string(e.Text[e.Pos:])
	at := " "
	if after != "" {
		r := []rune(after)
		if r[0] != '\n' {
			at, after = string(r[0]), string(r[1:])
		}
	}
	segs := []Seg{S(before, base)}
	if focused {
		segs = append(segs, S(at, SCursor))
	} else if at != " " {
		segs = append(segs, S(at, base))
	}
	segs = append(segs, S(after, base))
	return Wrap(segs, width)
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

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/wire"
)

func (a *app) homeKey(k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	if a.filter != nil { // searching the list
		switch ks {
		case "esc":
			a.filter = nil
			return nil
		case "enter":
			o := a.order()
			a.filter = nil
			if len(o) > 0 {
				return a.open(o[a.pos(o)].SessionID)
			}
			return nil
		case "up", "down":
		default:
			a.filter.Key(k, 0)
			if o := a.order(); len(o) > 0 {
				a.sel = o[0].SessionID
			}
			return nil
		}
	}
	o := a.order()
	switch ks {
	case "n":
		return a.newSession("")
	case "/":
		a.filter = &Editor{}
		return nil
	case "?", "ctrl+p", "ctrl+k":
		a.menu = a.homeMenu()
		return nil
	case "q":
		return tea.Quit
	}
	if len(o) == 0 {
		return nil
	}
	switch ks {
	case "up", "k":
		a.sel = o[max(0, a.pos(o)-1)].SessionID
	case "down", "j":
		a.sel = o[min(len(o)-1, a.pos(o)+1)].SessionID
	case "home", "g":
		a.sel = o[0].SessionID
	case "end", "G":
		a.sel = o[len(o)-1].SessionID
	case "enter", "right", "l":
		return a.open(o[a.pos(o)].SessionID)
	}
	return nil
}

// open shows a session's conversation and syncs it.
func (a *app) open(id string) tea.Cmd {
	same := a.conv != nil && a.conv.id == id
	a.openSession(id)
	if same {
		return a.sync() // catch up on what came while you were away
	}
	return tea.Batch(a.sync(), a.loadCatalog(a.conv.session.ProviderResourceID))
}

// pos is the selected session's row in o (the first row if it is not listed).
func (a *app) pos(o []wire.Session) int {
	for p, s := range o {
		if s.SessionID == a.sel {
			return p
		}
	}
	return 0
}

// glyph is a session's mark on the list: an unread dot, a failure, the
// spinner, or a resting dot.
func (a *app) glyph(st standing) Seg {
	switch st.word {
	case "working", "waking":
		return S(Spinner[a.frame%10], SAgent)
	case "ready":
		return S("●", SAccent)
	case "failed":
		return S("✕", SDanger)
	}
	return S("·", SFaint)
}

func (a *app) homeView() ([]string, string) {
	w := a.width()
	var head []string

	// the cover: the landscape, with the wordmark on its shore
	right := Line{S(a.projectName(a.projectID()), Fg(hex("#999fa6")))}
	if n := a.count(groupNeedsYou); n > 0 {
		right = append(right, gap(4), S("● ", SAccent), S(fmt.Sprintf("%d need you", n), Fg(groundSec)))
	}
	if n := a.count(groupRunning); n > 0 {
		right = append(right, gap(4), S(Spinner[a.frame%10]+" ", Fg(groundAgent)), S(fmt.Sprintf("%d working", n), Fg(groundSec)))
	}
	if n := a.elsewhere(); n > 0 {
		right = append(right, gap(4), S(fmt.Sprintf("%d elsewhere", n), Fg(groundAccent)))
	}
	if rows := a.mastRows(); rows > 0 {
		if a.mast != nil && a.mastKey == fmt.Sprintf("%s/%dx%d", a.prefs.Theme, a.w, rows) {
			head = append(head, masthead(a.mast, a.prefs.Theme, a.w, a.gfx, right)...)
		} else {
			for range rows {
				head = append(head, blank())
			}
			head = append(head, spread(Line{gap(margin), S("Barad", SText.Bold(true))}, append(right, gap(margin)), a.w).Paint(nil, 0))
		}
	} else {
		head = append(head, spread(Line{gap(margin), S("Barad", Fg(groundText).Bold(true))}, append(right, gap(margin)), a.w).Paint(lands[a.prefs.Theme].ground, a.w))
	}
	head = append(head, blank())

	order := a.order()
	if a.filter != nil {
		field := append(Line{S("/ ", SAccent.Bold(true))}, a.filter.Lines(w-24, 1, SText, "search sessions", true)[0]...)
		head = append(head, block([]Line{spread(field, Line{S(fmt.Sprintf("%d found", len(order)), SMuted)}, w-4)}, Raised, margin, w, 2)...)
		head = append(head, blank())
	}

	// the index: groups of sessions, each a name over one line about it. The
	// selected one sits on a raised surface whose edges take the spacer rows,
	// so nothing moves when the selection does.
	var out []string
	selLine := 0
	selRow := a.pos(order)
	current := group(-1)
	capBelow := false
	spacer := func(top bool) string {
		edge := Fg(Raised)
		ind := strings.Repeat(" ", margin)
		switch {
		case top:
			return ind + edge.Render(strings.Repeat("▄", w))
		case capBelow:
			capBelow = false
			return ind + edge.Render(strings.Repeat("▀", w))
		}
		return blank()
	}
	for p, s := range order {
		st := a.standing(s)
		sel := p == selRow && a.start == nil
		if st.group != current {
			if current >= 0 {
				out = append(out, spacer(false))
			}
			current = st.group
			label := groupLabel[st.group]
			out = append(out, at(Line{gap(2), S(label, SFaint.Bold(true)), gap(2), S(strings.Repeat("─", max(0, w-len(label)-4)), Fg(BorderSubtle))}))
		}
		out = append(out, spacer(sel))
		when := PadRight(ago(activityAt(s), a.now), 4)
		if st.group == groupRunning {
			when = "    "
		}
		name := SText
		if sel || st.group == groupNeedsYou {
			name = SText.Bold(true)
		}
		word := map[string]Seg{"ready": S("ready", SOk), "failed": S("failed", SDanger), "working": S("working", SAgent), "waking": S("waking", SAgent), "asleep": S("asleep", SFaint)}[st.word]
		title := spread(Line{a.glyph(st), gap(2), S(ansi.Truncate(sessionName(s), w-30, "…"), name)}, Line{word, gap(3), S(when, SFaint)}, w-4)
		mark, mc := providerMark(string(s.Provider))
		dek := Line{gap(3), S(mark+" ", Fg(mc)), S(modelName(s), SMuted)}
		if s.ReasoningEffort != "" {
			dek = append(dek, S("  ·  "+string(s.ReasoningEffort), SFaint))
		}
		if s.MachineStatus == wire.MachineStatusHydrated {
			dek = append(dek, S("  ·  awake", SFaint))
		}
		if sel {
			selLine = len(out)
		}
		for _, l := range []Line{title, dek} {
			l = append(Line{gap(2)}, l...)
			if sel {
				out = append(out, strings.Repeat(" ", margin)+on(l, Raised).Paint(Raised, w))
			} else {
				out = append(out, at(l))
			}
		}
		capBelow = sel
	}
	out = append(out, spacer(false))
	switch {
	case !a.listed:
		out = append(out, at(Line{gap(2), S(Spinner[a.frame%10]+"  ", SAgent), S("Loading your sessions", SMuted)}))
	case len(order) == 0 && a.filter != nil:
		out = append(out, at(Line{gap(2), S("Nothing matches.", SMuted)}))
	case len(order) == 0:
		out = append(out, at(Line{gap(2), S("No sessions here yet. Press ", SMuted), S("n", SSec.Bold(true)), S(" to start one.", SMuted)}))
	}
	// a long list scrolls under the cover, keeping the selection in view
	if avail := a.h - 1 - len(head); len(out) > avail && avail > 0 {
		start := min(max(0, selLine-avail/3), len(out)-avail)
		out = out[start : start+avail]
	}
	var foot string
	switch {
	case a.start != nil:
		foot = a.footer("tab", "next field", "enter", "start", "esc", "cancel")
	case a.filter != nil:
		foot = a.footer("enter", "open", "esc", "clear")
	default:
		foot = a.footer("↑↓", "select", "enter", "open", "n", "new", "/", "search", "?", "menu")
	}
	return append(head, out...), foot
}

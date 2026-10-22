package tui

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

const margin = 4

func (a *app) width() int { return a.w - 2*margin }

// at indents a line by the margin; every screen is one full-width column.
func at(l Line) string { return append(Line{gap(margin)}, l...).Paint(nil, 0) }

func blank() string { return "" }

// ---------------------------------------------------------------- keys

func (a *app) key(k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	if ks == "ctrl+c" {
		return tea.Quit
	}
	switch {
	case a.menu != nil:
		return a.menuKey(k)
	case a.start != nil:
		return a.startKey(k)
	}
	switch a.screen {
	case screenHome:
		return a.homeKey(k)
	case screenChat:
		return a.chatKey(k)
	case screenImages:
		return a.imagesKey(k)
	case screenUsage:
		if ks == "esc" || ks == "q" || ks == "left" {
			a.screen = screenHome
		}
	}
	return nil
}

func (a *app) chatKey(k tea.KeyPressMsg) tea.Cmd {
	empty := a.comp.Value() == "" && len(a.attach) == 0
	switch k.String() {
	case "esc":
		a.screen, a.sel = screenHome, a.conv.id
		return nil
	case "tab":
		a.prefs.Work = (a.prefs.Work + 1) % 3
		a.savePrefs()
		return nil
	case "ctrl+p", "ctrl+k":
		a.menu = a.chatMenu()
		return nil
	case "?":
		if empty {
			a.menu = a.chatMenu()
			return nil
		}
	case "ctrl+v":
		return func() tea.Msg {
			data, err := clipboardImage()
			return clipboardMsg{data, err}
		}
	case "pgup":
		return a.scrollBy(a.h / 2)
	case "pgdown":
		return a.scrollBy(-a.h / 2)
	case "ctrl+x":
		return a.cancelTurn()
	case "backspace":
		if a.comp.Value() == "" && len(a.attach) > 0 {
			a.attach = a.attach[:len(a.attach)-1]
			a.note = ""
			return nil
		}
	case "enter":
		if strings.TrimSpace(a.comp.Value()) != "" || len(a.attach) > 0 {
			return a.send()
		}
		return nil
	}
	a.comp.Key(k)
	return nil
}

// scrollBy moves the transcript by n rows (up is positive), and loads
// earlier messages when you reach the top.
func (a *app) scrollBy(n int) tea.Cmd {
	lines := len(a.transcript())
	top := max(0, lines-a.chatRows())
	a.scroll = min(max(0, a.scroll+n), top)
	if a.scroll >= top-a.h/2 {
		return a.loadOlder()
	}
	return nil
}

// send uploads the attachments, then sends the message. It shows as
// "sending" until the server holds it.
func (a *app) send() tea.Cmd {
	c := a.conv
	text := strings.TrimSpace(a.comp.Value())
	atts := a.attach
	requestID := randomID("msg_", 8)
	pending := api.PendingMessage{RequestID: requestID, Text: text, Kind: "sending", CreatedAt: a.now}
	for _, at := range atts {
		pending.Media = append(pending.Media, wire.PromptMediaRef{Filename: at.name, MediaType: at.mediaType})
	}
	c.sending = append(c.sending, pending)
	a.comp.Reset()
	a.attach, a.note, a.scroll = nil, "", 0
	id := c.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var ids []string
		for _, at := range atts {
			mid, err := a.api.UploadMedia(ctx, id, requestID, randomID("media_", 12), at.name, at.mediaType, at.data)
			if err != nil {
				return sentMsg{id, requestID, text, atts, err}
			}
			ids = append(ids, mid)
		}
		_, err := a.api.SendMessage(ctx, id, requestID, text, ids)
		return sentMsg{id, requestID, text, atts, err}
	}
}

func (a *app) cancelTurn() tea.Cmd {
	c := a.conv
	if c == nil || c.session.CurrentTurnID == "" || a.cancelRequestedOn == c.session.CurrentTurnID {
		return nil
	}
	a.cancelRequestedOn = c.session.CurrentTurnID
	id := c.id
	return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.Cancel(ctx, id) },
		func(_ struct{}, err error) tea.Msg {
			return doneMsg{say: "Stopping after this step", err: err, resync: true}
		})
}

// paste takes a paste into the composer. A path to an image (what a
// terminal sends when a file is dropped on it) attaches the image; text that
// would take the message past what it carries itself goes as a text
// attachment, as the web app does.
func (a *app) paste(text string) {
	if p, ok := pastedPath(text); ok {
		data, err := os.ReadFile(p)
		if err != nil {
			a.note = err.Error()
			return
		}
		a.attachImage(filepath.Base(p), data)
		return
	}
	if len(a.comp.Value())+len(text) > wire.MessageTextMaximumBytes {
		if len(text) > wire.PromptTextMaximumBytes {
			a.note = "Pasted text is larger than 1 MB"
			return
		}
		a.attachFile(attachment{name: "pasted-text.txt", data: []byte(text), mediaType: wire.PromptTextType})
		return
	}
	a.comp.Insert(text)
}

// attachImage adds an image to the next message.
func (a *app) attachImage(name string, data []byte) {
	mediaType := http.DetectContentType(data)
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		a.note = name + " is not a PNG, JPEG, WebP or GIF image"
		return
	}
	if len(data) > wire.PromptMediaMaximumBytes {
		a.note = name + " is larger than 20 MB"
		return
	}
	pic, err := NewPicture(name, data)
	if err != nil {
		a.note = err.Error()
		return
	}
	a.attachFile(attachment{name: name, data: data, mediaType: mediaType, pic: pic})
}

// attachFile adds a file to the next message, within the API's limits.
func (a *app) attachFile(at attachment) {
	total := len(at.data)
	for _, b := range a.attach {
		total += len(b.data)
	}
	switch {
	case len(a.attach) >= wire.PromptMediaMaximumItems:
		a.note = fmt.Sprintf("A message takes at most %d attachments", wire.PromptMediaMaximumItems)
		return
	case total > wire.PromptMediaMaximumTotalBytes:
		a.note = "A message's attachments take at most 50 MB together"
		return
	}
	a.attach = append(a.attach, at)
	a.note = "Attached " + at.name + " — it uploads when you send"
}

// ---------------------------------------------------------------- the conversation screen

func (a *app) chatView() ([]string, string) {
	w := a.width()
	top := append(a.sessionBar(), blank())
	bottom := a.composer()
	lines := a.transcript()
	vh := a.chatRows()
	end := len(lines) - min(a.scroll, max(0, len(lines)-vh))
	view := lines[max(0, end-vh):end]
	out := top
	for i := 0; i < vh-len(view); i++ {
		out = append(out, blank())
	}
	out = append(out, view...)
	if a.scroll > 0 {
		out[len(out)-1] = at(spread(nil, pill(Line{S("↓ newer below · pgdn", SSec)}, Muted), w))
	}
	out = append(out, blank())
	out = append(out, bottom...)
	keys := []string{"enter", "send", "esc", "sessions", "tab", [...]string{"show steps", "full output", "fold steps"}[a.prefs.Work], "?", "menu"}
	if a.conv.session.CurrentTurnID != "" {
		keys = []string{"enter", "send", "esc", "sessions", "tab", [...]string{"show steps", "full output", "fold steps"}[a.prefs.Work], "ctrl+x", "stop", "?", "menu"}
	}
	return out, a.footer(keys...)
}

// chatRows is how many rows the transcript has between the bar and the composer.
func (a *app) chatRows() int {
	return max(1, a.h-1-len(a.sessionBar())-1-len(a.composer())-1)
}

// composer is the message box: a raised block, the caret in vermilion, the
// images going with it above.
func (a *app) composer() []string {
	w := a.width()
	var comp []Line
	if len(a.attach) > 0 {
		var chips Line
		for _, at := range a.attach {
			size := fmt.Sprintf("%.0f KB", float64(len(at.data))/1024)
			if at.pic != nil {
				size = at.pic.Size()
			}
			chips = append(chips, S(attachMark(at.mediaType), SAccent), S(at.name, SText), S(" · "+size, SFaint), gap(4))
		}
		comp = append(comp, chips)
	}
	if a.note != "" {
		comp = append(comp, Line{S(a.note, SMuted)})
	}
	if len(comp) > 0 {
		comp = append(comp, Line{})
	}
	placeholder := "Message"
	switch st := a.standing(a.conv.session).word; {
	case a.conv.session.CurrentTurnID != "":
		placeholder = "Message — the agent reads it at its next step"
	case st == "asleep":
		placeholder = "Message — sending wakes its computer"
	}
	field := a.comp.Lines(w-8, SText, placeholder, a.menu == nil)
	if len(field) > 8 { // a long message scrolls in the box, keeping the caret's end in view
		field = field[len(field)-8:]
	}
	for i, l := range field {
		lead := gap(2)
		if i == 0 {
			lead = S("› ", SAccent.Bold(true))
		}
		comp = append(comp, append(Line{lead}, l...))
	}
	return block(comp, Raised, margin, w, 2)
}

// footer is the only help the app needs: the keys that work right now, and
// on the right what just happened.
func (a *app) footer(keys ...string) string {
	l := Line{}
	for i := 0; i+1 < len(keys); i += 2 {
		if i > 0 {
			l = append(l, gap(4))
		}
		l = append(l, S(keys[i], SSec.Bold(true)), S(" "+keys[i+1], SMuted))
	}
	var right Line
	switch {
	case a.toast != "" && a.now.Sub(a.toastAt) < 4*time.Second:
		mark, st := S("✓  ", SOk), SText
		if a.toastBad {
			mark = S("✕  ", SDanger)
		}
		room := a.width() - l.Width() - 10
		right = pill(Line{mark, S(ansi.Truncate(a.toast, max(10, room), "…"), st)}, Raised)
	case !a.online && a.listed:
		right = Line{S("○ ", SFaint), S("reconnecting", SMuted)}
	}
	return at(spread(l, right, a.width()))
}

// ---------------------------------------------------------------- the bar

// statusPill is a session's state on the bar: a pill in its hue. The pills
// use the night tints in both themes, because the ground is dark in both.
func (a *app) statusPill(s wire.Session) Line {
	quiet := Fg(lands[a.prefs.Theme].quiet)
	st := a.standing(s)
	word := st.word
	if a.conv != nil && a.conv.id == s.SessionID && st.group != groupRunning {
		if _, ok := a.conv.openProposal(); ok {
			word = "asks you"
		}
	}
	switch word {
	case "working", "waking":
		el := ""
		if t, ok := a.conv.turns[s.CurrentTurnID]; ok && word == "working" {
			el = "  " + dur(a.now.Sub(t.CreatedAt).Truncate(time.Second))
		}
		return pill(Line{S(Spinner[a.frame%10]+" ", Fg(groundAgent)), S(word, Fg(groundAgent).Bold(true)), S(el, Fg(groundSec))}, hex("#132b3d"))
	case "ready":
		return pill(Line{S("● ", Fg(groundOk)), S(word, Fg(groundOk).Bold(true))}, hex("#152c1e"))
	case "asks you":
		return pill(Line{S("◆ ", Fg(groundAccent)), S(word, Fg(groundAccent).Bold(true))}, hex("#3e2015"))
	case "failed":
		return pill(Line{S("✕ ", Fg(groundDanger)), S(word, Fg(groundDanger).Bold(true))}, hex("#3a1b24"))
	}
	return Line{S("○ "+word, quiet)}
}

// sessionBar is the top of every screen inside a session: where you are,
// its state, and a line about its agent, repositories, context, machine and
// recent activity. screen names a page within the session; none is the
// conversation.
func (a *app) sessionBar(screen ...string) []string {
	s := a.conv.session
	quiet := Fg(lands[a.prefs.Theme].quiet)
	crumbs, title := []string{a.projectName(projectOf(s))}, sessionName(s)
	if len(screen) > 0 {
		crumbs = append(crumbs, title)
		title = screen[0]
	}
	mark, mc := providerMark(string(s.Provider))
	detail := Line{S(mark+" ", Fg(mc)), S(modelName(s), Fg(groundText))}
	if s.ReasoningEffort != "" {
		detail = append(detail, S("  ·  "+string(s.ReasoningEffort), quiet))
	}
	if repos := a.sessionRepos[s.SessionID]; len(repos) > 0 {
		detail = append(detail, gap(5), S("repo  ", quiet), S(strings.Join(repos, ", "), Fg(groundSec)))
	}
	if total := s.ContextWindowTokens; total > 0 {
		u := s.LatestProviderUsage
		used := u.ContextTokens
		if used == 0 {
			used = u.InputTokens + u.OutputTokens
		}
		frac := math.Min(1, float64(used)/float64(total))
		fill := groundSec
		if limit := s.AutoCompactTokenLimit; used >= limit && limit > 0 || frac >= 1 {
			fill = groundAccent
		}
		detail = append(detail, gap(5), S("context  ", quiet))
		detail = append(detail, gauge(a.prefs.Theme, frac, 12, fill)...)
		detail = append(detail, S(fmt.Sprintf("  %d%%", int(frac*100+0.5)), Fg(groundSec)))
	}
	machine := map[wire.MachineStatus]string{
		wire.MachineStatusHydrated: "awake", wire.MachineStatusHydrating: "waking", wire.MachineStatusDehydrating: "falling asleep",
		wire.MachineStatusDehydrated: "asleep", wire.MachineStatusBroken: "broken",
	}[s.MachineStatus]
	switch machine {
	case "awake":
		detail = append(detail, gap(5), S("● ", Fg(groundOk)), S("awake", Fg(groundSec)))
	case "":
	default:
		detail = append(detail, gap(5), S("○ "+machine, quiet))
	}
	if act := a.conv.activity(a.now, 16); act != nil {
		detail = spread(detail, Line{S("activity  ", quiet), S(spark(act), Fg(groundAgent))}, a.width())
	}
	sweep := -1
	if a.standing(s).group == groupRunning {
		sweep = a.frame
	}
	return bar(a.prefs.Theme, a.w, crumbs, ansi.Truncate(title, a.width()/2, "…"), a.statusPill(s), detail, sweep)
}

// modelName is the provider and model, said once: "Claude opus-4", "Grok 4".
func modelName(s wire.Session) string {
	p := providerName(string(s.Provider))
	if s.Model == "" {
		return p
	}
	if strings.HasPrefix(strings.ToLower(s.Model), strings.ToLower(p)) {
		return s.Model
	}
	return p + " " + s.Model
}

// ---------------------------------------------------------------- the transcript

// transcript lays out the whole conversation, painted, oldest first.
func (a *app) transcript() []string {
	c := a.conv
	w := a.width()
	var out []string
	switch {
	case !c.initialized:
		return []string{at(Line{gap(2), S(Spinner[a.frame%10]+"  ", SAgent), S("Loading the conversation", SMuted)})}
	case c.loadingOlder:
		out = append(out, at(Line{gap(2), S(Spinner[a.frame%10]+"  ", SAgent), S("Loading earlier messages", SMuted)}), blank())
	case c.hasOlder:
		out = append(out, at(Line{gap(2), S("↑ ", SFaint), S("earlier messages above — scroll up", SFaint)}), blank())
	}
	exs := c.exchanges()
	if len(exs) == 0 {
		out = append(out, at(Line{gap(2), S("Nothing here yet. Say what this session should work on.", SMuted)}))
	}
	for _, ex := range exs {
		for _, l := range a.exchange(ex, w) {
			out = append(out, at(l))
		}
	}
	return out
}

// exchange lays out one message and the agent's answer. Your message sits
// on the right on a raised block, only as wide as it needs, up to two thirds
// of the screen. The agent answers on the left: its name, its runs of work
// (open while it works, folded to a pill when they end; Tab unfolds them),
// its reply a little short of the right edge, then a failure or a proposal.
func (a *app) exchange(ex exchange, w int) []Line {
	c := a.conv
	var rows []Line
	add := func(x int, ls ...Line) {
		for _, l := range ls {
			rows = append(rows, append(Line{gap(x)}, l...))
		}
	}

	if u := ex.user; u != nil {
		cw := w*2/3 - 4
		meta := Line{S(clock(u.at, a.now), SMuted)}
		switch u.status {
		case "sending":
			meta = Line{S(Spinner[a.frame%10]+" sending", SAgent)}
		case "queued":
			meta = Line{S("queued", SAgent)}
		case "steering":
			meta = Line{S("reaches the agent at its next step", SAgent)}
		}
		if len(u.media) == 0 {
			widest := 0
			for _, l := range strings.Split(u.text, "\n") {
				widest = max(widest, ansi.StringWidth(l))
			}
			cw = min(cw, max(widest, 6+meta.Width()))
		}
		you := []Line{spread(Line{S("you", Fg(AccentText).Bold(true))}, meta, cw)}
		if u.text != "" {
			you = append(you, Wrap([]Seg{S(u.text, SText)}, cw)...)
		}
		if len(u.media) > 0 {
			you = append(you, Line{})
			you = append(you, a.gallery(u.media, cw)...)
		}
		add(w-cw-4, slab(you, Raised, cw+4, 2)...)
	}

	if len(ex.parts) > 0 {
		if ex.user != nil {
			add(0, Line{})
		}
		s := c.session
		mark, mc := providerMark(string(s.Provider))
		add(0, Line{S(mark+" ", Fg(mc)), S(strings.ToLower(providerName(string(s.Provider))), SAgent.Bold(true)), S("  "+s.Model, SMuted)})
	}
	lastRun := map[string]int{}
	for i, p := range ex.parts {
		if p.kind == partRun {
			lastRun[p.turnID] = i
		}
	}
	aw := w * 6 / 7 // the agent's prose stops short of the right edge
	for i, p := range ex.parts {
		switch p.kind {
		case partRun:
			add(0, a.run(p, lastRun[p.turnID] == i, w)...)
		case partText:
			md := a.markdown(p.text, aw)
			if p.live && len(md) > 0 {
				md[len(md)-1] = append(md[len(md)-1], S("▍", SAccent))
			}
			add(0, Line{})
			add(0, md...)
		case partCompaction:
			add(0, Line{}, Line{S("── ", Fg(BorderDefault)), S(strings.ToLower(p.text), SFaint), S(" ──", Fg(BorderDefault))})
		case partFailure:
			title, msg := "The run stopped with an error", "The agent could not finish."
			if f := p.turn.Failure; f != nil {
				msg = f.Message
				if f.Code != "" {
					msg += "  (" + f.Code + ")"
				}
			}
			body := []Line{{S("✕  ", SDanger), S(title, SDanger.Bold(true))}}
			body = append(body, Indent(Wrap([]Seg{S(msg, SSec)}, aw-9), 3)...)
			add(0, Line{})
			add(0, slab(body, DangerSurf, aw, 3)...)
		case partCancelled:
			add(0, Line{}, Line{S("■  ", SFaint), S("stopped at your request", SMuted)})
		case partProposal:
			add(0, Line{})
			add(0, slab(a.proposal(p, aw-6), AccentSurface, aw, 3)...)
		}
	}
	add(0, Line{}, Line{}, Line{})
	return rows
}

// proposal is a session the agent wants to start, and what became of it.
func (a *app) proposal(p part, w int) []Line {
	var view wire.SessionProposalView
	known := false
	if p.result != nil {
		view, known = a.conv.proposals[p.result.Sequence]
	}
	title, reason := view.Proposal.Title, view.Proposal.Reason
	if !known {
		title, _ = p.steps[0].args["title"].(string)
		reason, _ = p.steps[0].args["reason"].(string)
	}
	body := []Line{{S("◆  ", SAccent), S("proposes a new session", Fg(AccentText).Bold(true))}, {}, {gap(3), S(title, SText.Bold(true))}}
	if reason != "" {
		body = append(body, Indent(Wrap([]Seg{S(reason, SSec)}, w-3), 3)...)
	}
	if len(view.Proposal.Repositories) > 0 {
		var names []string
		for _, r := range view.Proposal.Repositories {
			names = append(names, r.Repository)
		}
		body = append(body, Line{gap(3), S("repositories  ", SFaint), S(strings.Join(names, ", "), SSec)})
	}
	body = append(body, Line{})
	switch {
	case !known && p.result != nil:
		body = append(body, Line{gap(3), S(readResult(p.result).failure("new_session"), SMuted)})
	case !known:
		body = append(body, Line{gap(3), S(Spinner[a.frame%10]+" ", SAgent), S("proposing", SMuted)})
	case view.Decision == nil:
		body = append(body, Line{gap(3), S("Approve or dismiss it from the menu: ", SMuted), S("?", SSec.Bold(true))})
	case view.Decision.Status == "approved":
		body = append(body, Line{gap(3), S("✓  ", SOk), S("Approved — the new session started", SSec)})
	default:
		body = append(body, Line{gap(3), S("Dismissed", SMuted)})
	}
	return body
}

// markdown lays out agent prose, remembering the layout: View runs every frame.
func (a *app) markdown(text string, w int) []Line {
	k := mdKey{text, w}
	if l, ok := a.md[k]; ok {
		return append([]Line(nil), l...)
	}
	if len(a.md) > 4000 {
		clear(a.md)
	}
	l := Markdown(text, w)
	a.md[k] = l
	return append([]Line(nil), l...)
}

// gallery lays out images you attached: one large, several side by side,
// each with its name under it.
func (a *app) gallery(media []wire.PromptMediaRef, w int) []Line {
	cols := 24
	if len(media) == 1 {
		cols = min(w, 36)
	}
	var cells [][]Line
	for _, m := range media {
		var cell []Line
		ref := mediaRef(m)
		p := a.pics[ref.key]
		switch {
		case p != nil:
			cell = append(p.Lines(cols, a.gfx), Line{S(ansi.Truncate(m.Filename, cols, "…"), SMuted)})
		case m.MediaID == "" || a.picErr[ref.key] != "" || !isImage(m.MediaType):
			cell = []Line{{S(attachMark(m.MediaType), SAccent), S(ansi.Truncate(m.Filename, cols-2, "…"), SSec)}}
		default:
			cell = []Line{{S(Spinner[a.frame%10]+" ", SAgent), S(ansi.Truncate(m.Filename, cols-2, "…"), SMuted)}}
		}
		cells = append(cells, cell)
	}
	per := max(1, (w+2)/(cols+2))
	var out []Line
	for i := 0; i < len(cells); i += per {
		row := cells[i:min(len(cells), i+per)]
		h := 0
		for _, c := range row {
			h = max(h, len(c))
		}
		for r := 0; r < h; r++ {
			var l Line
			for j, c := range row {
				if j > 0 {
					l = append(l, gap(2))
				}
				var cl Line
				if r < len(c) {
					cl = c[r]
				}
				l = append(append(l, cl...), gap(cols-cl.Width()))
			}
			out = append(out, l)
		}
		out = append(out, Line{})
	}
	return out[:len(out)-1]
}

// run lays out a run of work: a pill once it is done and folded, or a
// table of its steps on the document surface.
func (a *app) run(p part, last bool, w int) []Line {
	c := a.conv
	t, hasTurn := c.turns[p.turnID]
	working := p.working
	var start, end time.Time
	calls := 0
	for _, s := range p.steps {
		if s.call != nil {
			if start.IsZero() {
				start = s.call.CreatedAt
			}
			end = s.call.CreatedAt
		}
		if s.result != nil {
			end = s.result.CreatedAt
		}
		if s.kind != "reasoning" {
			calls++
		}
	}
	if hasTurn && (start.IsZero() || working) { // a working run counts from its turn, as the bar does
		start = t.CreatedAt
	}
	took := end.Sub(start)
	if working {
		took = a.now.Sub(start)
	}
	if start.IsZero() {
		took = 0
	}
	took = took.Truncate(time.Second)
	count := fmt.Sprintf("%d steps", calls)
	if calls == 1 {
		count = "1 step"
	}
	failed := last && hasTurn && t.Status == wire.TurnStatusFailed
	cancelled := last && hasTurn && t.Status == wire.TurnStatusCancelled
	var row Line
	switch {
	case working:
		word := "working"
		switch {
		case a.cancelRequestedOn == p.turnID || hasTurn && t.CancelRequested:
			word = "stopping"
		case c.session.MachineStatus == wire.MachineStatusHydrating:
			word = "waking its computer"
		case hasTurn && t.Status == wire.TurnStatusCompacting:
			word = "compacting its context"
		}
		row = Line{S(Spinner[a.frame%10]+" ", SAgent), S(word, SAgent.Bold(true)), S(" · "+dur(took), SAgent)}
		if calls > 0 {
			row = append(row, S(" · "+count, SAgent))
		}
	case failed:
		row = Line{S("▸ ", SMuted), S("stopped", SDanger), S(" after "+dur(took)+" · "+count, SMuted)}
	case cancelled:
		row = Line{S("▸ ", SMuted), S("cancelled", SSec), S(" after "+dur(took)+" · "+count, SMuted)}
	default:
		row = Line{S("▸ ", SMuted), S("worked", SSec), S(" for "+dur(took)+" · "+count, SMuted)}
	}
	if !working && a.prefs.Work == workFolded {
		bg := Document
		if failed {
			bg = DangerSurf
		}
		return []Line{pill(row, bg)}
	}
	if !working {
		row[0] = S("▾ ", SMuted)
	}
	body := []Line{row}
	steps := p.steps
	if working && a.prefs.Work == workFolded && len(steps) > 6 { // a long run shows its newest steps while it works
		body = append(body, Line{S(fmt.Sprintf("… %d earlier steps", len(steps)-6), SFaint)})
		steps = steps[len(steps)-6:]
	}
	if len(steps) > 0 {
		body = append(body, Line{})
	}
	for i, s := range steps {
		running := working && i == len(steps)-1 && s.result == nil && s.kind != "reasoning"
		body = append(body, a.step(s, running, w-6)...)
	}
	return slab(body, Document, w, 3)
}

// verbs are a tool's past and present tense.
var verbs = map[string][2]string{
	"bash": {"ran", "running"}, "read": {"read", "reading"}, "edit": {"edited", "editing"},
	"write": {"wrote", "writing"}, "web_search": {"searched", "searching"},
	"screenshot": {"captured", "capturing"}, "publish": {"published", "publishing"},
	"reasoning": {"thought", "thinking"}, "fs_list": {"listed", "listing"}, "fs_read": {"read", "reading"},
}

const labelW = 11

// toolSummary is what a call works on, as the web app summarizes it.
func toolSummary(kind string, args map[string]any) string {
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch kind {
	case "web_search":
		return str("query")
	case "bash":
		return str("command")
	case "read", "write", "edit":
		return str("file_path")
	case "fs_list", "fs_read", "publish":
		return str("path")
	}
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}

// step lays out one step as a row of a table: the operation, its target
// and its outcome on the right, the result beneath the target.
func (a *app) step(s *step, running bool, w int) []Line {
	v, ok := verbs[s.kind]
	if !ok {
		v = [2]string{strings.ReplaceAll(s.kind, "_", " "), strings.ReplaceAll(s.kind, "_", " ")}
	}
	lab := S(PadRight(v[0], labelW), SMuted)
	if running {
		lab = S(PadRight(v[1], labelW), SAgent)
	}
	tw := w - labelW
	under := func(out []Line, ls ...Line) []Line {
		for _, l := range ls {
			out = append(out, append(Line{gap(labelW)}, l...))
		}
		return out
	}
	if s.kind == "reasoning" {
		lines := Wrap([]Seg{S(strings.TrimSpace(s.think), SSec.Italic(true))}, tw)
		if a.prefs.Work != workFull && len(lines) > 2 {
			lines = append(lines[:2:2], Line{S("…", SFaint)})
		}
		out := []Line{append(Line{lab}, lines[0]...)}
		return under(out, lines[1:]...)
	}

	r := readResult(s.result)
	failure := ""
	if s.result != nil {
		failure = r.failure(s.kind)
	}
	var outcome Line
	switch {
	case running:
		outcome = Line{S(Spinner[a.frame%10], SAgent)}
	case s.result == nil:
		outcome = Line{S("·", SFaint)}
	case r.cancelled:
		outcome = Line{S("cancelled", SMuted)}
	case failure != "":
		outcome = Line{S("✕", SDanger)}
	case s.kind == "edit":
		add, del := editCounts(s.args)
		outcome = Line{S(fmt.Sprintf("+%d", add), SOk), S(" ", SText), S(fmt.Sprintf("−%d", del), SDanger)}
	default:
		outcome = Line{S("✓", SOk)}
	}
	if s.call != nil && s.result != nil {
		outcome = append(Line{S(dur(s.result.CreatedAt.Sub(s.call.CreatedAt).Truncate(100*time.Millisecond))+"  ", SFaint)}, outcome...)
	}
	target := strings.TrimSpace(toolSummary(s.kind, s.args))
	if i := strings.IndexByte(target, '\n'); i >= 0 {
		target = target[:i] + " …"
	}
	out := []Line{spread(Line{lab, S(ansi.Truncate(target, max(4, tw-outcome.Width()-2), "…"), SSec)}, outcome, w)}
	if a.prefs.Work == workFolded && !running || s.result == nil || r.cancelled {
		return out
	}

	clip := func(o string) string { return ansi.Truncate(strings.ReplaceAll(o, "\t", "  "), tw-1, "…") }
	text := strings.TrimRight(r.output(), "\n")
	if failure != "" && text == "" { // a failure with output shows the output; one without, why
		lines := Wrap([]Seg{S(failure, SDanger)}, tw-1)
		if a.prefs.Work != workFull && len(lines) > 3 {
			lines = lines[:3]
		}
		return under(out, lines...)
	}
	switch {
	case s.screenshot():
		ref := shotRef(s.result)
		out = append(out, Line{})
		switch p := a.pics[ref.key]; {
		case p != nil:
			out = under(out, p.Lines(shotWidth(tw), a.gfx)...)
		case a.picErr[ref.key] != "":
			out = under(out, Line{S("▣ screenshot unavailable", SFaint)})
		default:
			out = under(out, Line{S(Spinner[a.frame%10]+" loading the screenshot", SFaint)})
		}
		return append(out, Line{})
	case s.kind == "edit" && a.prefs.Work == workFull:
		edits, _ := s.args["edits"].([]any)
		for _, e := range edits {
			m, _ := e.(map[string]any)
			oldText, _ := m["old_text"].(string)
			newText, _ := m["new_text"].(string)
			for _, d := range [][2]string{{"-", oldText}, {"+", newText}} {
				if d[1] == "" {
					continue
				}
				fg, bg := SDanger, DangerSurf
				if d[0] == "+" {
					fg, bg = SOk, SuccessSurf
				}
				for _, l := range strings.Split(strings.TrimSuffix(d[1], "\n"), "\n") {
					text := clip(d[0] + " " + l)
					out = under(out, Line{S(text+strings.Repeat(" ", max(0, tw-ansi.StringWidth(text))), fg.Background(bg))})
				}
			}
		}
		return out
	case s.kind == "write" && a.prefs.Work == workFull:
		content, _ := s.args["content"].(string)
		return under(out, codeLines(content, filepath.Ext(target), tw)...)
	case (s.kind == "read" || s.kind == "fs_read") && text != "" && failure == "":
		if a.prefs.Work == workFull {
			return under(out, codeLines(text, filepath.Ext(target), tw)...)
		}
		return under(out, Line{S(fmt.Sprintf("%d lines", strings.Count(text, "\n")+1), SFaint)})
	}
	if text == "" {
		return out
	}
	output := strings.Split(text, "\n")
	tone := func(o string) lipgloss.Style {
		switch {
		case strings.Contains(o, "FAIL") || strings.Contains(o, "panic:") || strings.Contains(o, "Error"):
			return SDanger.Bold(true)
		case strings.HasPrefix(o, "ok ") || strings.HasPrefix(o, "PASS"):
			return SOk
		}
		return SMuted
	}
	if a.prefs.Work == workFull {
		const most = 400
		for i, o := range output {
			if i == most {
				out = under(out, Line{S(fmt.Sprintf("… %d more lines", len(output)-most), SFaint)})
				break
			}
			out = under(out, Line{S(clip(o), tone(o))})
		}
		return out
	}
	last := telling(output)
	l := Line{S(ansi.Truncate(strings.ReplaceAll(last, "\t", "  "), tw-14, "…"), tone(last))}
	switch more := len(output) - 1; {
	case more == 1:
		l = append(l, S("   +1 line", SFaint))
	case more > 1:
		l = append(l, S(fmt.Sprintf("   +%d lines", more), SFaint))
	}
	return under(out, l)
}

// codeLines is a file's text highlighted on the code well, at most 200 lines.
func codeLines(content, ext string, w int) []Line {
	lines := strings.Split(content, "\n")
	more := ""
	if len(lines) > 200 {
		more = fmt.Sprintf("… %d more lines", len(lines)-200)
		lines = lines[:200]
	}
	out := CodeBlock(strings.Join(lines, "\n"), strings.TrimPrefix(ext, "."), w, Subtle)
	if more != "" {
		out = append(out, Line{S(more, SFaint)})
	}
	return out
}

// telling is the line of a result worth showing when it is folded: a
// failure if there is one, else how it ended.
func telling(output []string) string {
	for _, o := range output {
		if strings.Contains(o, "FAIL") || strings.Contains(o, "panic:") {
			return o
		}
	}
	for i := len(output) - 1; i >= 0; i-- {
		if strings.TrimSpace(output[i]) != "" {
			return output[i]
		}
	}
	return ""
}

// editCounts is how many lines an edit adds and removes: the lines of its
// old and new text that the other does not have.
func editCounts(args map[string]any) (add, del int) {
	edits, _ := args["edits"].([]any)
	for _, e := range edits {
		m, _ := e.(map[string]any)
		oldText, _ := m["old_text"].(string)
		newText, _ := m["new_text"].(string)
		have := map[string]int{}
		for _, l := range strings.Split(oldText, "\n") {
			have[l]++
		}
		for _, l := range strings.Split(newText, "\n") {
			if have[l] > 0 {
				have[l]--
				continue
			}
			add++
		}
		for _, n := range have {
			del += n
		}
	}
	return add, del
}

func isImage(mediaType string) bool { return strings.HasPrefix(mediaType, "image/") }

// attachMark is an attachment's glyph: a picture, or a page of text.
func attachMark(mediaType string) string {
	if isImage(mediaType) {
		return "▣ "
	}
	return "▤ "
}

// shotWidth is how wide the transcript draws a screenshot.
func shotWidth(w int) int {
	switch {
	case w >= 150:
		return 96
	case w >= 74:
		return 64
	}
	return max(10, min(w, 40))
}

func dur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second && d%time.Second != 0:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// clock is a time of day, with the day when it is not today.
func clock(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	switch {
	case t.YearDay() == now.YearDay() && t.Year() == now.Year():
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

// ago is how long since t, in one short word.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

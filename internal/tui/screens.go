package tui

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/barad-io/barad/internal/api"
	"github.com/charmbracelet/x/ansi"
)

func (a *app) View() tea.View {
	if a.w == 0 {
		return tea.NewView("")
	}
	if a.w < 60 || a.h < 16 { // the smallest window every screen and sheet fits in
		v := tea.NewView(a.canvas(at(Line{S("Make the window a little larger.", SMuted)})))
		v.AltScreen, v.BackgroundColor, v.ForegroundColor = true, Canvas, Text
		return v
	}
	v := tea.NewView(a.canvas(a.compose()))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor, v.ForegroundColor = Canvas, Text
	v.WindowTitle = "Barad"
	if a.conv != nil && a.screen != screenHome && a.screen != screenUsage {
		v.WindowTitle = sessionName(a.conv.session) + " — Barad"
	}
	v.ProgramStatus = &tea.ProgramStatus{App: "barad", Title: "Barad", State: "idle"}
	if n := a.countAll(api.Running); n > 0 {
		v.ProgramStatus.State = "working"
		v.ProgramStatus.Message = fmt.Sprintf("%d working", n)
	}
	return v
}

// compose draws the screen: the current one, its footer, and a sheet over it.
func (a *app) compose() string {
	var body []string
	var foot string
	switch {
	case a.screen == screenChat && a.conv != nil:
		body, foot = a.chatView()
	case a.screen == screenImages && a.conv != nil:
		body, foot = a.imagesView(), a.footer("← →", "previous, next", "o", "open in your viewer", "esc", "back")
	case a.screen == screenUsage:
		body, foot = a.usageView(), a.footer("tab", [2]string{"inference", "barad"}[a.usageTab], "← →", "7 · 30 · 90 days", "esc", "back")
	default:
		body, foot = a.homeView()
	}
	if a.menu != nil {
		foot = a.footer("↑↓", "select", "enter", "choose", "esc", "back")
		switch {
		case a.menu.confirm != nil:
			foot = a.footer("enter", a.menu.confirm.label, "esc", "cancel")
		case a.menu.edit != nil:
			foot = a.footer("enter", "save", "esc", "cancel")
		}
	}
	for len(body) < a.h-1 {
		body = append(body, blank())
	}
	screen := strings.Join(body[:a.h-1], "\n")
	var card string
	switch {
	case a.menu != nil:
		card = a.menuView()
	case a.start != nil:
		card = a.startView()
	}
	layers := []*lipgloss.Layer{lipgloss.NewLayer(screen + "\n" + foot)}
	if card != "" { // the screen fades to an outline behind a sheet; the keys stay readable
		layers = []*lipgloss.Layer{lipgloss.NewLayer(ghost(screen) + "\n" + foot),
			lipgloss.NewLayer(card).X((a.w - lipgloss.Width(card)) / 2).Y(max(1, a.h/6)).Z(2)}
	}
	return lipgloss.NewCompositor(layers...).Render()
}

// ghost turns the screen behind a sheet into a faint outline of its text:
// colours, surfaces and pictures drop away so the sheet stands alone.
func ghost(screen string) string {
	var b strings.Builder
	st := Fg(BorderDefault)
	for i, line := range strings.Split(screen, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		var t strings.Builder
		for _, r := range ansi.Strip(line) {
			switch {
			case unicode.Is(unicode.Mn, r): // a picture's row and column marks
			case r == 0x10EEEE || strings.ContainsRune("▀▄▐▌▍▁▂▃▅▆▇█━", r):
				t.WriteByte(' ')
			default:
				t.WriteRune(r)
			}
		}
		b.WriteString(st.Render(t.String()))
	}
	return b.String()
}

// ---------------------------------------------------------------- images

func (a *app) openImages() tea.Cmd {
	refs := a.conv.pictures()
	if len(refs) == 0 {
		a.say("No images in this session yet")
		return nil
	}
	a.imgIdx, a.screen = len(refs)-1, screenImages
	return a.fetchPictures()
}

func (a *app) imagesKey(k tea.KeyPressMsg) tea.Cmd {
	refs := a.conv.pictures()
	switch k.String() {
	case "esc", "q":
		a.screen = screenChat
	case "left", "h":
		a.imgIdx = max(0, a.imgIdx-1)
	case "right", "l":
		a.imgIdx = min(len(refs)-1, a.imgIdx+1)
	case "o":
		if a.imgIdx < len(refs) {
			if p := a.pics[refs[a.imgIdx].key]; p != nil {
				if err := openExternally(strings.TrimSuffix(p.Name, filepath.Ext(p.Name))+".png", p.Encoded()); err != nil {
					return a.fail("Could not open it", err)
				}
			}
		}
	}
	return nil
}

// imagesView shows one picture as large as the screen allows under the bar.
func (a *app) imagesView() []string {
	refs := a.conv.pictures()
	out := append(a.sessionBar("Images"), blank())
	if a.imgIdx >= len(refs) {
		return out
	}
	ref := refs[a.imgIdx]
	p := a.pics[ref.key]
	counter := Line{S(fmt.Sprintf("%d of %d", a.imgIdx+1, len(refs)), SMuted)}
	if p == nil {
		state := Line{S(Spinner[a.frame%10]+"  ", SAgent), S("Loading "+ref.name, SMuted)}
		if e := a.picErr[ref.key]; e != "" {
			state = Line{S("✕  ", SDanger), S(ref.name+" is unavailable: "+e, SMuted)}
		}
		return append(out, at(spread(state, counter, a.width())))
	}
	b := p.Img.Bounds()
	rows := a.h - len(out) - 4
	cols := max(10, min(a.w-8, int(float64(rows)*2*float64(b.Dx())/float64(b.Dy()))))
	pad := (a.w - cols) / 2
	for _, l := range p.Lines(cols, a.gfx) {
		out = append(out, append(Line{gap(pad)}, l...).Paint(nil, 0))
	}
	caption := spread(Line{S("▣ ", SAccent), S(p.Name, SText.Bold(true)), S("   "+p.Size(), SMuted)}, counter, cols)
	return append(out, blank(), append(Line{gap(pad)}, caption...).Paint(nil, 0))
}

// ---------------------------------------------------------------- usage

func money(nanos int64) string {
	return fmt.Sprintf("$%.2f", float64(nanos)/1e9)
}

func tokens(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// usageView is the account's last 30 days in the web app's two parts:
// Barad, the workspace time your credits pay for, and Inference, the tokens
// your model providers bill. Tab switches between them; the chart takes the
// height that is left.
func (a *app) usageView() []string {
	w := a.width()
	quiet := Fg(lands[a.prefs.Theme].quiet)
	detail := Line{S("loading", quiet)}
	if b := a.balance; b != nil {
		detail = Line{S("credits  ", quiet), S(money(b.BalanceUSDNanos), Fg(groundText).Bold(true)), S("  left", quiet)}
	}
	out := bar(a.prefs.Theme, a.w, []string{a.projectName(a.projectID())}, "Usage", Line{S(fmt.Sprintf("last %d days", a.days), quiet)}, detail, -1)
	out = append(out, blank())

	// the two parts, the open one underlined in vermilion
	names := [2][2]string{{"BARAD", "workspace time, paid with your credits"}, {"INFERENCE", "tokens, billed by your model providers"}}
	var tabs, under Line
	for i, n := range names {
		if i > 0 {
			tabs, under = append(tabs, gap(6)), append(under, S(strings.Repeat("─", 6), Fg(BorderSubtle)))
		}
		label := Line{S(n[0], SFaint.Bold(true))}
		rule := S(strings.Repeat("─", ansi.StringWidth(n[0])), Fg(BorderSubtle))
		if i == a.usageTab {
			label = Line{S(n[0], Fg(AccentText).Bold(true)), S("   "+n[1], SMuted)}
			rule = S(strings.Repeat("━", ansi.StringWidth(n[0])), SAccent)
		}
		tabs = append(tabs, label...)
		under = append(under, rule, S(strings.Repeat("─", label.Width()-ansi.StringWidth(rule.Text)), Fg(BorderSubtle)))
	}
	under = append(under, S(strings.Repeat("─", max(0, w-under.Width())), Fg(BorderSubtle)))
	out = append(out, at(tabs), at(under), blank())

	r, ws := a.usage, a.workspace
	if r == nil || ws == nil || a.balance == nil {
		return append(out, at(Line{gap(2), S(Spinner[a.frame%10]+"  ", SAgent), S("Loading usage", SMuted)}))
	}
	var cards [][3]string
	var dates []string
	var values []float64
	var title, note string
	var unit func(float64) string
	// two lists under the chart: what the totals are made of
	type entry struct{ label, value string }
	var parts [2]struct {
		title string
		rows  []entry
	}
	switch a.usageTab {
	case 0:
		t := ws.Totals
		cards = [][3]string{
			{"CREDITS LEFT", money(a.balance.BalanceUSDNanos), "they pay for workspace time"},
			{"RUNNING TIME", fmt.Sprintf("%dh %02dm", t.BilledMinutes/60, t.BilledMinutes%60), "billed by the started minute"},
			{"CREDITS USED", money(t.ChargedUSDNanos), money(t.ChargedUSDNanos/max(1, int64(len(ws.Daily)))) + " a day on average"},
			{"WORKSPACE RUNS", fmt.Sprint(t.Runs), "times a computer woke to work"},
		}
		for _, d := range ws.Daily {
			dates, values = append(dates, d.Date), append(values, float64(d.ChargedUSDNanos))
		}
		title, note = "CREDITS USED EACH DAY", "Sleeping workspaces are free."
		unit = func(v float64) string { return money(int64(v)) }
		parts[0].title, parts[1].title = "BY SESSION", "BALANCE ACTIVITY"
		for _, w := range ws.Workspaces {
			name := "a deleted session"
			if s, ok := a.session(w.SessionID); ok {
				name = sessionName(s)
			}
			parts[0].rows = append(parts[0].rows, entry{name, money(w.ChargedUSDNanos) + "  ·  " + fmt.Sprintf("%dh %02dm", w.BilledMinutes/60, w.BilledMinutes%60)})
		}
		for _, e := range ws.BalanceActivity {
			amount := money(e.AmountUSDNanos)
			if e.AmountUSDNanos > 0 {
				amount = "+" + amount
			}
			parts[1].rows = append(parts[1].rows, entry{strings.ReplaceAll(e.Kind, "_", " "), amount + "  ·  " + clock(e.EffectiveAt, a.now)})
		}
	default:
		t := r.Totals
		cached := 0.0
		if t.InputTokens > 0 {
			cached = float64(t.CachedInputTokens) / float64(t.InputTokens)
		}
		cards = [][3]string{
			{"PROCESSED TOKENS", tokens(t.ProcessedTokens), fmt.Sprintf("%d%% of input cached", int(cached*100+0.5))},
			{"DIRECT API COST", money(t.ActualCost.Nanodollars()), "reported by providers, or priced"},
			{"EQUIVALENT API VALUE", money(t.APIEquivalentCost.Nanodollars()), "an estimate, not what you paid"},
		}
		for _, d := range r.Daily {
			dates, values = append(dates, d.Date), append(values, float64(d.Metrics.ProcessedTokens))
		}
		title, note = "TOKENS EACH DAY", "Barad does not charge for inference; your model providers bill it."
		unit = func(v float64) string { return tokens(int64(v)) }
		parts[0].title, parts[1].title = "BY PROVIDER", "BY MODEL"
		for _, b := range r.Providers {
			name := b.ProviderResourceName
			if name == "" {
				name = providerName(string(b.Provider))
			}
			parts[0].rows = append(parts[0].rows, entry{name, tokens(b.Metrics.ProcessedTokens) + "  ·  " + money(b.Metrics.ActualCost.Nanodollars())})
		}
		for _, b := range r.Models {
			parts[1].rows = append(parts[1].rows, entry{b.Model, tokens(b.Metrics.ProcessedTokens) + "  ·  " + money(b.Metrics.ActualCost.Nanodollars())})
		}
	}
	listed := 0
	for _, p := range parts {
		listed = max(listed, min(5, len(p.rows)))
	}
	var lists []string // the two lists, side by side
	if listed > 0 {
		half := (w - 4) / 2
		row := func(i int) string {
			var l Line
			for c, p := range parts {
				cell := Line{}
				switch {
				case i < 0:
					cell = Line{S(p.title, SFaint.Bold(true))}
				case i < len(p.rows) && i < 5:
					e := p.rows[i]
					cell = spread(Line{S(ansi.Truncate(e.label, half-ansi.StringWidth(e.value)-3, "…"), SText)}, Line{S(e.value, SMuted)}, half)
				}
				if c > 0 {
					l = append(l, gap(4))
				}
				l = append(l, cell...)
				l = append(l, gap(half-cell.Width()))
			}
			return at(l)
		}
		lists = append(lists, blank(), row(-1))
		for i := range listed {
			lists = append(lists, row(i))
		}
	}

	// the figures, side by side, each on a rule
	cw := (w - 2*(len(cards)-1)) / len(cards)
	for row := range 3 {
		var l Line
		for i, c := range cards {
			cell := [3]Line{{S(c[0], SFaint.Bold(true))}, {S(c[1], SText.Bold(true))}, {S(ansi.Truncate(c[2], cw-4, "…"), SMuted)}}[row]
			if i > 0 {
				l = append(l, gap(2))
			}
			l = append(l, S(ruleMark, Fg(AccentText)))
			l = append(append(l, cell...), gap(cw-2-cell.Width()))
		}
		out = append(out, at(l))
	}
	out = append(out, blank(), blank())
	if len(values) == 0 {
		return append(out, at(Line{S("Nothing in these days.", SMuted)}))
	}
	top := 0.0
	for _, v := range values {
		top = math.Max(top, v)
	}
	out = append(out, at(spread(Line{S(title, SFaint.Bold(true))}, Line{S("peak  ", SFaint), S(unit(top), SSec)}, w)), blank())

	// the chart: one bar a day, as tall as the screen allows
	h := max(4, a.h-1-len(out)-4-len(lists))
	n := len(values)
	barW := func(i int) int { return max(1, (i+1)*(w+1)/n-i*(w+1)/n-1) } // the bars share the whole width
	for row := h - 1; row >= 0; row-- {
		var l Line
		for i, v := range values {
			eighths := 0
			if top > 0 {
				eighths = int(v / top * float64(h*8))
			}
			c := " "
			switch {
			case eighths >= (row+1)*8:
				c = "█"
			case eighths > row*8:
				c = string([]rune("▁▂▃▄▅▆▇█")[eighths-row*8-1])
			}
			st := SAgent
			if i == len(values)-1 {
				st = SAccent // today
			}
			l = append(l, S(strings.Repeat(c, barW(i)), st), gap(1))
		}
		out = append(out, at(l))
	}
	width := 0
	for i := range n {
		width += barW(i) + 1
	}
	width--
	axis := spread(Line{S(dates[0], SMuted)}, Line{S("today", Fg(AccentText))}, width)
	out = append(out, at(Line{S(strings.Repeat("─", width), Fg(BorderDefault))}), at(axis))
	out = append(out, lists...)
	return append(out, blank(), at(Line{S(note, SFaint)}))
}

package tui

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (a *app) View() tea.View {
	if a.w == 0 {
		return tea.NewView("")
	}
	if a.w < 60 || a.h < 16 { // the smallest window every screen and sheet fits in
		v := tea.NewView(at(Line{S("Make the window a little larger.", SMuted)}))
		v.AltScreen, v.BackgroundColor, v.ForegroundColor = true, Canvas, Text
		return v
	}
	var body []string
	var foot string
	switch {
	case a.screen == screenChat && a.conv != nil:
		body, foot = a.chatView()
	case a.screen == screenImages && a.conv != nil:
		body, foot = a.imagesView(), a.footer("← →", "previous, next", "o", "open in your viewer", "esc", "back")
	case a.screen == screenUsage:
		body, foot = a.usageView(), a.footer("esc", "back")
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
	v := tea.NewView(lipgloss.NewCompositor(layers...).Render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.BackgroundColor, v.ForegroundColor = Canvas, Text
	v.WindowTitle = "Barad"
	if a.conv != nil && a.screen != screenHome && a.screen != screenUsage {
		v.WindowTitle = sessionName(a.conv.session) + " — Barad"
	}
	v.ProgramStatus = &tea.ProgramStatus{App: "barad", Title: "Barad", State: "idle"}
	if n := a.count(groupRunning); n > 0 {
		v.ProgramStatus.State = "working"
		v.ProgramStatus.Message = fmt.Sprintf("%d working", n)
	}
	return v
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

// usageView is the account's last 30 days: four figures on cards, then the
// daily spend as a bar chart.
func (a *app) usageView() []string {
	w := a.width()
	quiet := Fg(lands[a.prefs.Theme].quiet)
	detail := Line{S("loading", quiet)}
	if b := a.balance; b != nil {
		detail = Line{S("balance ", quiet), S(money(b.BalanceUSDNanos), Fg(groundText).Bold(true))}
	}
	out := bar(a.prefs.Theme, a.w, []string{a.projectName(a.projectID())}, "Usage", Line{S("last 30 days", quiet)}, detail, -1)
	out = append(out, blank())
	r, ws := a.usage, a.workspace
	if r == nil || ws == nil {
		return append(out, at(Line{gap(2), S(Spinner[a.frame%10]+"  ", SAgent), S("Loading usage", SMuted)}))
	}
	t := r.Totals
	cached := 0.0
	if t.InputTokens > 0 {
		cached = float64(t.CachedInputTokens) / float64(t.InputTokens)
	}
	actual, list := t.ActualCost.Nanodollars(), t.APIEquivalentCost.Nanodollars()
	saved := ""
	if list > 0 {
		saved = fmt.Sprintf("list price, saved %d%%", int(100*(1-float64(actual)/float64(list))+0.5))
	}
	hours := ws.Totals.BilledMinutes / 60
	cards := [][3]string{
		{"TOKENS", tokens(t.ProcessedTokens), fmt.Sprintf("%d%% cached", int(cached*100+0.5))},
		{"WORKSPACE TIME", fmt.Sprintf("%dh %02dm", hours, ws.Totals.BilledMinutes%60), fmt.Sprintf("%s across %d runs", money(ws.Totals.ChargedUSDNanos), ws.Totals.Runs)},
		{"API COST", money(actual), "what you paid"},
		{"API EQUIVALENT", money(list), saved},
	}
	cw := (w - 3*2) / len(cards)
	var slabs [][]Line
	for _, c := range cards {
		slabs = append(slabs, slab([]Line{{S(c[0], SFaint.Bold(true))}, {}, {S(c[1], SText.Bold(true))}, {S(ansi.Truncate(c[2], cw-6, "…"), SMuted)}}, Raised, cw, 3))
	}
	for row := range slabs[0] {
		var l Line
		for i, sl := range slabs {
			if i > 0 {
				l = append(l, gap(2))
			}
			l = append(l, sl[row]...)
		}
		out = append(out, at(l))
	}
	out = append(out, blank(), blank(), at(Line{S("DAILY SPEND", SFaint.Bold(true)), gap(3), S("API cost and workspace time per day", SMuted)}), blank())
	days := make([]float64, len(r.Daily))
	for i, d := range r.Daily {
		days[i] = float64(d.Metrics.ActualCost.Nanodollars())
		if i < len(ws.Daily) && ws.Daily[i].Date == d.Date {
			days[i] += float64(ws.Daily[i].ChargedUSDNanos)
		}
	}
	if len(days) == 0 {
		return append(out, at(Line{S("Nothing spent in these days.", SMuted)}))
	}
	const h = 6
	bw := max(1, (w-6)/len(days)-1)
	top := 0.0
	for _, d := range days {
		top = math.Max(top, d)
	}
	for row := h - 1; row >= 0; row-- {
		var l Line
		for i, d := range days {
			eighths := 0
			if top > 0 {
				eighths = int(d / top * h * 8)
			}
			c := " "
			switch {
			case eighths >= (row+1)*8:
				c = "█"
			case eighths > row*8:
				c = string([]rune("▁▂▃▄▅▆▇█")[eighths-row*8-1])
			}
			st := SAgent
			if i == len(days)-1 {
				st = SAccent // today
			}
			l = append(l, S(strings.Repeat(c, bw), st), gap(1))
		}
		out = append(out, at(l))
	}
	width := len(days)*(bw+1) - 1
	axis := spread(Line{S(r.Daily[0].Date, SMuted)}, Line{S("today", Fg(AccentText))}, width)
	return append(out, at(Line{S(strings.Repeat("─", width), Fg(BorderDefault))}), at(axis), blank(),
		at(Line{S("peak ", SFaint), S(money(int64(top)), SSec), S(" a day", SFaint)}))
}

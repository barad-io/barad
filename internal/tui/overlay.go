package tui

import (
	"context"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/wire"
)

// overlay is any on-demand modal rendered inside the live bottom region
// without touching primary scrollback.
type overlay interface {
	Title() string
	Key(a *app, k tea.KeyPressMsg) tea.Cmd
	View(a *app, w, maxH int) []Line
}

// renderOverlayFrame wraps an overlay's lines in a crisp minimal top/bottom
// divider frame (no heavy box borders that break narrow Rex/tmux splits).
func renderOverlayFrame(ov overlay, a *app, w, maxH int) []Line {
	w = max(w, 28)
	title := ov.Title()
	head := Line{S("── ", Fg(BorderDefault)), S(title, SAccent.Bold(true)), S(" ", Fg(BorderDefault))}
	if rem := w - head.Width(); rem > 0 {
		head = append(head, S(strings.Repeat("─", rem), Fg(BorderDefault)))
	}
	body := ov.View(a, w, max(6, maxH-2))
	out := make([]Line, 0, len(body)+2)
	out = append(out, head)
	out = append(out, body...)
	return out
}

// ---------------------------------------------------------------- /sessions fuzzy picker

type pickerOverlay struct {
	filter      Editor
	projectIdx  int // 0 = All, 1..len(projects), last = Archived
	sel         int
	preselected bool
}

func (a *app) openPicker(initialQuery string) tea.Cmd {
	ov := &pickerOverlay{}
	if initialQuery != "" {
		ov.filter.Insert(initialQuery)
	}
	a.ov = ov
	return a.list()
}

func (p *pickerOverlay) Title() string { return "Sessions  (Ctrl+P)" }

func (p *pickerOverlay) projectTabs(a *app) []struct{ id, label string } {
	tabs := []struct{ id, label string }{{"", "All"}, {wire.DefaultProjectID, wire.DefaultProjectName}}
	for _, pr := range a.projects {
		if pr.ProjectID != wire.DefaultProjectID && pr.ClosingAt.IsZero() {
			tabs = append(tabs, struct{ id, label string }{pr.ProjectID, pr.Name})
		}
	}
	tabs = append(tabs, struct{ id, label string }{"__archived__", "Archived"})
	return tabs
}

func (p *pickerOverlay) filtered(a *app) []wire.Session {
	tabs := p.projectTabs(a)
	if p.projectIdx >= len(tabs) {
		p.projectIdx = 0
	}
	tab := tabs[p.projectIdx]
	src := a.sessions
	if tab.id == "__archived__" {
		src = a.archived
	}
	q := strings.TrimSpace(p.filter.Value())
	var out []wire.Session
	for _, s := range src {
		if tab.id != "" && tab.id != "__archived__" && projectOfSession(s) != tab.id {
			continue
		}
		if q != "" {
			hay := sessionTitle(s) + " " + s.HarnessID() + " " + s.Model + " " + s.SessionID
			if !Fuzzy(q, hay) {
				continue
			}
		}
		out = append(out, s)
	}
	if tab.id == "__archived__" {
		sort.SliceStable(out, func(i, j int) bool { return out[i].ArchivedAt.After(out[j].ArchivedAt) })
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		gi := api.StandingOf(out[i], a.prefs.Reviewed[out[i].SessionID], false).Group
		gj := api.StandingOf(out[j], a.prefs.Reviewed[out[j].SessionID], false).Group
		if gi != gj {
			return gi < gj
		}
		return sessionActivity(out[i]).After(sessionActivity(out[j]))
	})
	return out
}

func (p *pickerOverlay) Key(a *app, k tea.KeyPressMsg) tea.Cmd {
	rows := p.filtered(a)
	total := len(rows) + 1 // row 0..len-1 are sessions, last row is "+ New session"
	if !p.preselected && len(rows) > 0 {
		p.preselected = true
		target := a.prefs.LastSession
		if a.conv != nil {
			target = a.conv.id
		}
		for i, s := range rows {
			if s.SessionID == target {
				p.sel = i
				break
			}
		}
	}
	if p.sel >= total {
		p.sel = max(0, total-1)
	}

	toggleRead := func() tea.Cmd {
		if p.sel < 0 || p.sel >= len(rows) {
			return nil
		}
		s := rows[p.sel]
		if api.Unread(s, a.prefs.Reviewed[s.SessionID]) {
			a.prefs, _ = prefs.Update(func(pr *prefs.Prefs) { pr.Read([]wire.Session{s}) })
			a.flash("Marked "+sessionTitle(s)+" read", false)
		} else if s.LatestTerminalTurnID != "" {
			a.prefs, _ = prefs.Update(func(pr *prefs.Prefs) { pr.Unread(s.SessionID) })
			a.flash("Marked "+sessionTitle(s)+" unread", false)
		} else {
			a.flash("No finished reply to mark read/unread yet", false)
		}
		return nil
	}

	switch k.String() {
	case "esc":
		if a.conv != nil {
			a.ov = nil
			return nil
		}
		return a.quit()
	case "tab":
		tabs := p.projectTabs(a)
		p.projectIdx = (p.projectIdx + 1) % len(tabs)
		p.sel = 0
		return nil
	case "shift+tab":
		tabs := p.projectTabs(a)
		p.projectIdx = (p.projectIdx + len(tabs) - 1) % len(tabs)
		p.sel = 0
		return nil
	case "ctrl+n":
		return a.openNewSession("")
	case "space":
		// When not typing a filter query, Space toggles read/unread on the highlighted session.
		if strings.TrimSpace(p.filter.Value()) == "" && p.sel < len(rows) {
			return toggleRead()
		}
	case "ctrl+r":
		return toggleRead()
	case "alt+r":
		var unread []wire.Session
		for _, s := range rows {
			if api.Unread(s, a.prefs.Reviewed[s.SessionID]) {
				unread = append(unread, s)
			}
		}
		if len(unread) == 0 {
			a.flash("Everything here is already read", false)
			return nil
		}
		a.prefs, _ = prefs.Update(func(pr *prefs.Prefs) { pr.Read(unread) })
		a.flash("Marked all sessions read", false)
		return nil
	case "up", "ctrl+k":
		if p.sel > 0 {
			p.sel--
		}
		return nil
	case "down", "ctrl+j":
		if p.sel < total-1 {
			p.sel++
		}
		return nil
	case "enter":
		if p.sel == len(rows) {
			return a.openNewSession("")
		}
		chosen := rows[p.sel]
		a.ov = nil
		return a.attachSession(chosen)
	}
	before := p.filter.Value()
	p.filter.Key(k, max(20, a.w-10))
	if p.filter.Value() != before {
		p.sel = 0
	}
	return nil
}

func (p *pickerOverlay) View(a *app, w, maxH int) []Line {
	tabs := p.projectTabs(a)
	if p.projectIdx >= len(tabs) {
		p.projectIdx = 0
	}
	isArchived := tabs[p.projectIdx].id == "__archived__"
	rows := p.filtered(a)
	if !p.preselected && len(rows) > 0 {
		p.preselected = true
		target := a.prefs.LastSession
		if a.conv != nil {
			target = a.conv.id
		}
		for i, s := range rows {
			if s.SessionID == target {
				p.sel = i
				break
			}
		}
	}
	total := len(rows) + 1
	if p.sel >= total {
		p.sel = max(0, total-1)
	}

	var out []Line
	// Project filter tabs.
	var tabLine Line
	for i, t := range tabs {
		if i > 0 {
			tabLine = append(tabLine, S("  ", SFaint))
		}
		st := SFaint
		if i == p.projectIdx {
			st = SAccent.Bold(true)
		}
		tabLine = append(tabLine, S(t.label, st))
	}
	out = append(out, tabLine)

	ph := "Filter sessions… (Space read/unread · Alt+R read all · Tab project)"
	if w < 72 {
		ph = "Filter… (Space read · Tab project)"
	}
	ed := p.filter.Lines(max(12, w-4), 1, SText, ph, true)
	out = append(out, append(Line{S("/ ", SAccent)}, ed[0]...))

	if !a.listed {
		out = append(out, Line{S("  "+Spinner[a.frame%len(Spinner)]+" Loading sessions…", SMuted)})
		return out
	}

	// Build display rows with explicit section headers so sort order is obvious.
	type dispRow struct {
		header string
		selIdx int // -1 for header, 0..len(rows)-1 for session, len(rows) for "+ New session"
	}
	var disp []dispRow
	groupName := func(g api.Group) string {
		switch g {
		case api.NeedsYou:
			return "NEEDS YOU · unread, sign-in, or failed"
		case api.Running:
			return "RUNNING · active or waking"
		default:
			return "RESTING · recent first"
		}
	}
	lastGroup := api.Group(-1)
	if isArchived && len(rows) > 0 {
		disp = append(disp, dispRow{header: "ARCHIVED · newest first", selIdx: -1})
	}
	for i, s := range rows {
		if !isArchived {
			g := api.StandingOf(s, a.prefs.Reviewed[s.SessionID], false).Group
			if g != lastGroup {
				lastGroup = g
				disp = append(disp, dispRow{header: groupName(g), selIdx: -1})
			}
		}
		disp = append(disp, dispRow{selIdx: i})
	}
	disp = append(disp, dispRow{selIdx: len(rows)})

	// Find display row matching p.sel and keep it scrolled into view.
	selDisp := 0
	for i, d := range disp {
		if d.selIdx == p.sel {
			selDisp = i
			break
		}
	}
	listH := max(3, maxH-4)
	start := 0
	if selDisp >= listH {
		start = selDisp - listH + 1
	}
	// If the row immediately above start is the header for our first visible item, include it when possible.
	if start > 0 && disp[start-1].selIdx == -1 && selDisp-start < listH-1 {
		start--
	}
	end := min(len(disp), start+listH)

	for _, d := range disp[start:end] {
		if d.selIdx == -1 {
			out = append(out, Line{S("  "+d.header, SFaint)})
			continue
		}
		selected := d.selIdx == p.sel
		prefix := "  "
		if selected {
			prefix = "▸ "
		}
		if d.selIdx == len(rows) {
			st := SAccent
			if selected {
				st = SAccent.Bold(true)
			}
			out = append(out, Line{S(prefix, st), S("+ New session", st)})
			continue
		}
		s := rows[d.selIdx]
		standing := api.StandingOf(s, a.prefs.Reviewed[s.SessionID], false)
		unread := api.Unread(s, a.prefs.Reviewed[s.SessionID])

		dot := S("  ", SFaint)
		if unread {
			dot = S("● ", SAccent)
		} else if a.conv != nil && a.conv.id == s.SessionID {
			dot = S("◆ ", SOk)
		}

		statusSt := SFaint
		switch standing.Word {
		case "working", "waking":
			statusSt = SAgent
		case "sign-in required":
			statusSt = SWarn.Bold(true)
		case "failed":
			statusSt = SDanger
		case "ready":
			statusSt = SAccent
		}

		metaStr := standing.Word
		if w >= 66 {
			metaStr = harnessLabel(s.HarnessID()) + " · " + standing.Word + " · " + ago(sessionActivity(s), a.now)
		} else if w >= 48 {
			metaStr = standing.Word + " · " + ago(sessionActivity(s), a.now)
		}
		right := Line{S(metaStr, statusSt)}
		nameSt := SSec
		if selected {
			nameSt = SText.Bold(true)
		}
		leftW := max(10, w-right.Width()-6)
		left := Line{S(prefix, SAccent), dot, S(ansi.Truncate(sessionTitle(s), leftW, "…"), nameSt)}
		out = append(out, spread(left, right, w))
	}

	hint := "  Enter attach · Space toggle read · Alt+R mark all read · Ctrl+N new · Esc close"
	if strings.TrimSpace(p.filter.Value()) != "" {
		hint = "  Enter attach · Ctrl+R toggle read · Tab project · Esc close"
	} else if w < 82 {
		hint = "  Enter attach · Space read/unread · Tab project · Ctrl+N new"
	}
	out = append(out, Line{S(ansi.Truncate(hint, w, "…"), SFaint)})
	return out
}

// ---------------------------------------------------------------- /new modal

type newSessionOverlay struct {
	field       int // 0=project, 1=harness, 2=model, 3=effort, 4=fast(codex), 5=size, 6=name, 7=repos, 8=prompt, 9=create
	projectIdx  int
	agentIdx    int
	modelIdx    int
	customModel Editor
	editCustom  bool
	effortIdx   int
	fastMode    bool
	sizeIdx     int
	name        Editor
	prompt      Editor
	selectedRep map[string]bool
	editingText bool
	pickingRepo bool
	repoSel     int
	busy        bool
	errMsg      string
}

func (a *app) openNewSession(initialPrompt string) tea.Cmd {
	ov := &newSessionOverlay{
		selectedRep: map[string]bool{},
		sizeIdx:     2, // default "l"
	}
	if initialPrompt != "" {
		ov.prompt.Insert(initialPrompt)
	}
	// Default size index from catalog.
	for i, sz := range a.sizes {
		if sz.Default || sz.ID == "l" {
			ov.sizeIdx = i
			break
		}
	}
	// Apply current project defaults if available.
	ov.applyProjectDefaults(a)
	a.ov = ov
	cmds := []tea.Cmd{}
	if len(a.agents) == 0 {
		cmds = append(cmds, a.loadAgents())
	}
	if !a.reposLoaded {
		cmds = append(cmds, a.loadRepos())
	}
	return tea.Batch(cmds...)
}

func (n *newSessionOverlay) Title() string { return "New session" }

func (n *newSessionOverlay) projects(a *app) []wire.Project {
	out := []wire.Project{{ProjectID: wire.DefaultProjectID, Name: wire.DefaultProjectName}}
	for _, p := range a.projects {
		if p.ProjectID == wire.DefaultProjectID {
			out[0] = p
			continue
		}
		if p.ClosingAt.IsZero() {
			out = append(out, p)
		}
	}
	return out
}

func (n *newSessionOverlay) currentAgent(a *app) wire.AgentSpec {
	if len(a.agents) == 0 {
		return wire.AgentSpec{ID: "claude", Label: "Claude Code", DefaultModel: "default"}
	}
	if n.agentIdx >= len(a.agents) {
		n.agentIdx = 0
	}
	return a.agents[n.agentIdx]
}

func (n *newSessionOverlay) currentModel(a *app) wire.AgentModelSpec {
	ag := n.currentAgent(a)
	if n.modelIdx == len(ag.Models) {
		id := strings.TrimSpace(n.customModel.Value())
		if id == "" {
			id = "custom…"
		}
		// Inherit default effort options for custom model IDs.
		ms := wire.AgentModelSpec{ID: id, Label: id, SupportsReasoningEffort: true, Efforts: []wire.ReasoningEffort{"low", "medium", "high"}, DefaultEffort: "high"}
		for _, m := range ag.Models {
			if m.ID == ag.DefaultModel || m.ID == "default" {
				ms.SupportsReasoningEffort, ms.Efforts, ms.DefaultEffort = m.SupportsReasoningEffort, m.Efforts, m.DefaultEffort
				break
			}
		}
		return ms
	}
	if len(ag.Models) == 0 {
		return wire.AgentModelSpec{ID: "default", Label: "CLI Default"}
	}
	if n.modelIdx >= len(ag.Models) {
		n.modelIdx = 0
	}
	return ag.Models[n.modelIdx]
}

func (n *newSessionOverlay) applyProjectDefaults(a *app) {
	ps := n.projects(a)
	if n.projectIdx >= len(ps) {
		n.projectIdx = 0
	}
	p := ps[n.projectIdx]
	n.selectedRep = map[string]bool{}
	for _, r := range p.Repositories {
		n.selectedRep[strings.ToLower(r.Repository)] = true
	}
	if p.Agent != nil {
		if p.Agent.Agent != "" {
			for i, ag := range a.agents {
				if strings.EqualFold(ag.ID, p.Agent.Agent) {
					n.agentIdx = i
					break
				}
			}
		}
		ag := n.currentAgent(a)
		n.modelIdx = 0
		if p.Agent.Model != "" {
			found := false
			for i, m := range ag.Models {
				if m.ID == p.Agent.Model {
					n.modelIdx, found = i, true
					break
				}
			}
			if !found {
				n.modelIdx = len(ag.Models)
				n.customModel.Reset()
				n.customModel.Insert(p.Agent.Model)
			}
		}
		n.syncEffortDefault(a, p.Agent.ReasoningEffort)
		if p.Agent.Size != "" {
			for i, sz := range a.sizes {
				if sz.ID == p.Agent.Size {
					n.sizeIdx = i
					break
				}
			}
		}
	} else {
		n.syncEffortDefault(a, "")
	}
}

func (n *newSessionOverlay) syncEffortDefault(a *app, prefer wire.ReasoningEffort) {
	ms := n.currentModel(a)
	n.effortIdx = 0
	if !ms.SupportsReasoningEffort || len(ms.Efforts) == 0 {
		return
	}
	target := prefer
	if target == "" {
		target = ms.DefaultEffort
	}
	for i, e := range ms.Efforts {
		if e == target {
			n.effortIdx = i
			return
		}
	}
}

func (n *newSessionOverlay) Key(a *app, k tea.KeyPressMsg) tea.Cmd {
	if n.busy {
		return nil
	}
	ks := k.String()
	// Sub-mode: picking GitHub repositories.
	if n.pickingRepo {
		switch ks {
		case "esc", "enter":
			n.pickingRepo = false
		case "up", "k":
			if n.repoSel > 0 {
				n.repoSel--
			}
		case "down", "j":
			if n.repoSel < len(a.repos)-1 {
				n.repoSel++
			}
		case "space":
			if n.repoSel >= 0 && n.repoSel < len(a.repos) {
				key := strings.ToLower(a.repos[n.repoSel].FullName)
				n.selectedRep[key] = !n.selectedRep[key]
			}
		}
		return nil
	}
	// Sub-mode: editing inline text field (custom model / name / prompt).
	if n.editingText {
		switch ks {
		case "esc":
			n.editingText = false
			n.editCustom = false
			return nil
		case "enter":
			n.editingText = false
			n.editCustom = false
			return nil
		}
		switch {
		case n.editCustom:
			n.customModel.Key(k, max(16, a.w-22))
		case n.field == 6:
			n.name.Key(k, max(16, a.w-22))
		case n.field == 8:
			n.prompt.Key(k, max(16, a.w-22))
		}
		return nil
	}

	ag := n.currentAgent(a)
	ms := n.currentModel(a)
	ps := n.projects(a)

	switch ks {
	case "esc":
		a.ov = nil
		return nil
	case "up", "k":
		if n.field > 0 {
			n.field--
			if n.field == 4 && ag.ID != "codex" {
				n.field--
			}
			if n.field == 3 && (!ms.SupportsReasoningEffort || len(ms.Efforts) == 0) {
				n.field--
			}
		}
		return nil
	case "down", "j", "tab":
		if n.field < 9 {
			n.field++
			if n.field == 3 && (!ms.SupportsReasoningEffort || len(ms.Efforts) == 0) {
				n.field++
			}
			if n.field == 4 && ag.ID != "codex" {
				n.field++
			}
		}
		return nil
	case "left", "h":
		n.cycle(a, -1, ps, ag, ms)
		return nil
	case "right", "l", "space":
		n.cycle(a, 1, ps, ag, ms)
		return nil
	case "enter":
		switch n.field {
		case 2:
			if n.modelIdx == len(ag.Models) {
				n.editingText = true
				n.editCustom = true
			} else {
				n.cycle(a, 1, ps, ag, ms)
			}
		case 6, 8:
			n.editingText = true
		case 7:
			n.pickingRepo = true
			n.repoSel = 0
		case 9:
			return n.submit(a)
		default:
			n.cycle(a, 1, ps, ag, ms)
		}
		return nil
	}
	return nil
}

func (n *newSessionOverlay) cycle(a *app, dir int, ps []wire.Project, ag wire.AgentSpec, ms wire.AgentModelSpec) {
	switch n.field {
	case 0:
		if len(ps) > 0 {
			n.projectIdx = (n.projectIdx + dir + len(ps)) % len(ps)
			n.applyProjectDefaults(a)
		}
	case 1:
		if len(a.agents) > 0 {
			n.agentIdx = (n.agentIdx + dir + len(a.agents)) % len(a.agents)
			n.modelIdx = 0
			if a.agents[n.agentIdx].ID != "codex" {
				n.fastMode = false
			}
			n.syncEffortDefault(a, "")
		}
	case 2:
		total := len(ag.Models) + 1 // +1 for custom model ID
		n.modelIdx = (n.modelIdx + dir + total) % total
		n.syncEffortDefault(a, "")
	case 3:
		if len(ms.Efforts) > 0 {
			n.effortIdx = (n.effortIdx + dir + len(ms.Efforts)) % len(ms.Efforts)
		}
	case 4:
		n.fastMode = !n.fastMode
	case 5:
		if len(a.sizes) > 0 {
			n.sizeIdx = (n.sizeIdx + dir + len(a.sizes)) % len(a.sizes)
		}
	}
}

func (n *newSessionOverlay) submit(a *app) tea.Cmd {
	ag := n.currentAgent(a)
	ms := n.currentModel(a)
	modelID := ms.ID
	if n.modelIdx == len(ag.Models) {
		modelID = strings.TrimSpace(n.customModel.Value())
		if modelID == "" {
			n.errMsg = "Enter a custom model ID or pick one from the list"
			return nil
		}
	}
	var effort wire.ReasoningEffort
	if ms.SupportsReasoningEffort && len(ms.Efforts) > 0 && n.effortIdx < len(ms.Efforts) {
		effort = ms.Efforts[n.effortIdx]
	}
	sizeID := "l"
	if n.sizeIdx >= 0 && n.sizeIdx < len(a.sizes) {
		sizeID = a.sizes[n.sizeIdx].ID
	}
	ps := n.projects(a)
	projID := wire.DefaultProjectID
	if n.projectIdx < len(ps) {
		projID = ps[n.projectIdx].ProjectID
	}
	var repoReqs []api.RepositoryRequest
	for _, r := range a.repos {
		if n.selectedRep[strings.ToLower(r.FullName)] {
			repoReqs = append(repoReqs, api.RepositoryRequest{SourceID: r.SourceID, Repository: r.FullName})
		}
	}
	in := api.NewSession{
		Name:            strings.TrimSpace(n.name.Value()),
		Agent:           ag.ID,
		Model:           modelID,
		ReasoningEffort: effort,
		FastMode:        ag.ID == "codex" && n.fastMode,
		Size:            sizeID,
		ProjectID:       projID,
		Repositories:    repoReqs,
	}
	prompt := strings.TrimSpace(n.prompt.Value())
	n.busy = true
	n.errMsg = ""
	return call(func(ctx context.Context) (*wire.Session, error) {
		s, err := a.api.CreateSession(ctx, in)
		if err != nil {
			return nil, err
		}
		if prompt != "" {
			_, _ = a.api.SendMessage(ctx, s.SessionID, randomID("msg_", 8), prompt, nil)
		}
		return s, nil
	}, func(s *wire.Session, err error) tea.Msg {
		return createdMsg{session: s, err: err}
	})
}

func (n *newSessionOverlay) View(a *app, w, maxH int) []Line {
	if n.pickingRepo {
		var out []Line
		out = append(out, Line{S("Select GitHub repositories (Space toggle · Enter done)", SMuted)})
		if len(a.repos) == 0 {
			out = append(out, Line{S("  No GitHub repositories connected. Connect via barad git login.", SFaint)})
			return out
		}
		limit := max(4, maxH-2)
		start := 0
		if n.repoSel >= limit {
			start = n.repoSel - limit + 1
		}
		for i := start; i < min(len(a.repos), start+limit); i++ {
			r := a.repos[i]
			mark := "☐ "
			if n.selectedRep[strings.ToLower(r.FullName)] {
				mark = "☑ "
			}
			pref := "  "
			st := SSec
			if i == n.repoSel {
				pref = "▸ "
				st = SText.Bold(true)
			}
			out = append(out, Line{S(pref, SAccent), S(mark, SOk), S(r.FullName, st)})
		}
		return out
	}

	ps := n.projects(a)
	projName := wire.DefaultProjectName
	if n.projectIdx < len(ps) {
		projName = ps[n.projectIdx].Name
	}
	ag := n.currentAgent(a)
	ms := n.currentModel(a)
	modelVal := formatModelOptionLabel(ag, ms)
	if n.modelIdx == len(ag.Models) {
		if n.editCustom {
			ed := n.customModel.Lines(max(12, w-18), 1, SText, "model-id…", true)
			modelVal = "custom: " + ed[0].Paint(nil, 0)
		} else if v := strings.TrimSpace(n.customModel.Value()); v != "" {
			modelVal = "custom: " + v + " (Enter to edit)"
		} else {
			modelVal = "Custom model ID… (Enter to type)"
		}
	}
	effortVal := "—"
	if ms.SupportsReasoningEffort && len(ms.Efforts) > 0 && n.effortIdx < len(ms.Efforts) {
		effortVal = string(ms.Efforts[n.effortIdx])
	}
	sizeVal := "l"
	if n.sizeIdx < len(a.sizes) {
		sizeVal = a.sizes[n.sizeIdx].Label
	}
	var repoNames []string
	for k, v := range n.selectedRep {
		if v {
			repoNames = append(repoNames, k)
		}
	}
	sort.Strings(repoNames)
	repoVal := "none"
	if len(repoNames) > 0 {
		repoVal = strings.Join(repoNames, ", ")
	}
	nameVal := strings.TrimSpace(n.name.Value())
	if nameVal == "" {
		nameVal = "(auto-generated)"
	}
	promptVal := strings.TrimSpace(n.prompt.Value())
	if promptVal == "" {
		promptVal = "(optional)"
	}

	row := func(idx int, label, val string, editing bool, ed *Editor) Line {
		pref := "  "
		lblSt, valSt := SMuted, SSec
		if n.field == idx {
			pref = "▸ "
			lblSt = SAccent.Bold(true)
			valSt = SText.Bold(true)
		}
		l := Line{S(pref, SAccent), S(PadRight(label, 12), lblSt)}
		if editing && ed != nil {
			el := ed.Lines(max(12, w-16), 1, SText, "", true)
			return append(l, el[0]...)
		}
		return append(l, S(ansi.Truncate(val, max(10, w-16), "…"), valSt))
	}

	var out []Line
	out = append(out, row(0, "Project", "◂ "+projName+" ▸", false, nil))
	out = append(out, row(1, "Harness", "◂ "+ag.Label+" ("+ag.ID+") ▸", false, nil))
	out = append(out, row(2, "Model", "◂ "+modelVal+" ▸", false, nil))
	if ms.SupportsReasoningEffort && len(ms.Efforts) > 0 {
		out = append(out, row(3, "Effort", "◂ "+effortVal+" ▸", false, nil))
	}
	if ag.ID == "codex" {
		fm := "off"
		if n.fastMode {
			fm = "on (fast tier)"
		}
		out = append(out, row(4, "Fast mode", "◂ "+fm+" ▸", false, nil))
	}
	out = append(out, row(5, "Size", "◂ "+sizeVal+" ▸", false, nil))
	out = append(out, row(6, "Name", nameVal, n.editingText && n.field == 6, &n.name))
	out = append(out, row(7, "Repos", repoVal+" (Enter to pick)", false, nil))
	out = append(out, row(8, "Prompt", promptVal, n.editingText && n.field == 8, &n.prompt))

	btnSt := SAccent
	pref := "  "
	if n.field == 9 {
		pref = "▸ "
		btnSt = SAccent.Bold(true)
	}
	btnLabel := "[ Create session ]"
	if n.busy {
		btnLabel = Spinner[a.frame%len(Spinner)] + " Creating session…"
	}
	out = append(out, Line{S(pref, SAccent), S(btnLabel, btnSt)})
	if n.errMsg != "" {
		out = append(out, Line{S("  ✕ "+n.errMsg, SDanger)})
	} else {
		out = append(out, Line{S("  ↑/↓ move · ←/→ cycle · Enter edit/create · Esc cancel", SFaint)})
	}
	return out
}

// ---------------------------------------------------------------- /model overlay

type modelOverlay struct {
	sel        int // 0..len(models)-1, len(models)=custom, +1=effort, +2=fast (codex)
	modelID    string
	custom     Editor
	editCustom bool
	effort     wire.ReasoningEffort
	fastMode   bool
	saving     bool
	errMsg     string
}

func (a *app) openModelOverlay() tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first (/sessions)", true)
		return nil
	}
	s := a.conv.session
	ov := &modelOverlay{
		modelID:  s.Model,
		effort:   s.ReasoningEffort,
		fastMode: s.FastMode,
	}
	a.ov = ov
	if len(a.agents) == 0 {
		return a.loadAgents()
	}
	return nil
}

func (m *modelOverlay) Title() string { return "Model & Reasoning" }

func (m *modelOverlay) spec(a *app) wire.AgentSpec {
	id := ""
	if a.conv != nil {
		id = a.conv.session.HarnessID()
	}
	for _, ag := range a.agents {
		if strings.EqualFold(ag.ID, id) {
			return ag
		}
	}
	return wire.AgentSpec{ID: id, Label: harnessLabel(id), DefaultModel: "default"}
}

func (m *modelOverlay) effortsFor(ag wire.AgentSpec, modelID string) []wire.ReasoningEffort {
	for _, ms := range ag.Models {
		if ms.ID == modelID && ms.SupportsReasoningEffort {
			return ms.Efforts
		}
	}
	for _, ms := range ag.Models {
		if (ms.ID == ag.DefaultModel || ms.ID == "default") && ms.SupportsReasoningEffort {
			return ms.Efforts
		}
	}
	return nil
}

func (m *modelOverlay) Key(a *app, k tea.KeyPressMsg) tea.Cmd {
	if m.saving || a.conv == nil {
		return nil
	}
	ag := m.spec(a)
	isCodex := ag.ID == "codex"
	efforts := m.effortsFor(ag, m.modelID)
	turnActive := a.conv.session.CurrentTurnID != ""

	if m.editCustom {
		switch k.String() {
		case "esc":
			m.editCustom = false
		case "enter":
			if v := strings.TrimSpace(m.custom.Value()); v != "" {
				m.modelID = v
				m.editCustom = false
				return m.save(a)
			}
			m.editCustom = false
		default:
			m.custom.Key(k, max(16, a.w-18))
		}
		return nil
	}

	// Rows: models..., custom, [effort], [fast]
	customIdx := len(ag.Models)
	effortIdx := -1
	fastIdx := -1
	next := customIdx + 1
	if len(efforts) > 0 {
		effortIdx = next
		next++
	}
	if isCodex {
		fastIdx = next
		next++
	}
	total := next

	switch k.String() {
	case "esc":
		a.ov = nil
		return nil
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < total-1 {
			m.sel++
		}
	case "left", "right", "space":
		dir := 1
		if k.String() == "left" {
			dir = -1
		}
		if m.sel == effortIdx && len(efforts) > 0 {
			i := slices.Index(efforts, m.effort)
			if i < 0 {
				i = 0
			}
			m.effort = efforts[(i+dir+len(efforts))%len(efforts)]
			return m.save(a)
		}
		if m.sel == fastIdx {
			m.fastMode = !m.fastMode
			return m.save(a)
		}
	case "enter":
		switch {
		case m.sel < len(ag.Models):
			if turnActive && ag.Models[m.sel].ID != a.conv.session.Model {
				m.errMsg = "Model cannot be changed while a turn is active (press Esc in chat to stop)"
				return nil
			}
			m.modelID = ag.Models[m.sel].ID
			if effs := m.effortsFor(ag, m.modelID); len(effs) > 0 && !slices.Contains(effs, m.effort) {
				m.effort = ag.Models[m.sel].DefaultEffort
				if m.effort == "" {
					m.effort = effs[0]
				}
			}
			return m.save(a)
		case m.sel == customIdx:
			if turnActive {
				m.errMsg = "Model cannot be changed while a turn is active"
				return nil
			}
			m.editCustom = true
		case m.sel == effortIdx && len(efforts) > 0:
			i := slices.Index(efforts, m.effort)
			m.effort = efforts[(i+1)%len(efforts)]
			return m.save(a)
		case m.sel == fastIdx:
			m.fastMode = !m.fastMode
			return m.save(a)
		}
	}
	return nil
}

func (m *modelOverlay) save(a *app) tea.Cmd {
	s := a.conv.session
	st := api.SettingsOf(s)
	st.Model = m.modelID
	st.ReasoningEffort = m.effort
	st.FastMode = m.fastMode
	m.saving = true
	m.errMsg = ""
	return call(func(ctx context.Context) (*wire.Session, error) {
		return a.api.UpdateSettings(ctx, s.SessionID, st)
	}, func(updated *wire.Session, err error) tea.Msg {
		return modelSavedMsg{session: updated, err: err}
	})
}

func (m *modelOverlay) View(a *app, w, maxH int) []Line {
	ag := m.spec(a)
	isCodex := ag.ID == "codex"
	efforts := m.effortsFor(ag, m.modelID)
	turnActive := a.conv != nil && a.conv.session.CurrentTurnID != ""

	var out []Line
	if turnActive {
		out = append(out, Line{S("⚠ Turn active — model switching is locked until the turn ends", SWarn)})
	}

	idx := 0
	for _, ms := range ag.Models {
		pref := "  "
		st := SSec
		if m.sel == idx {
			pref = "▸ "
			st = SText.Bold(true)
		}
		cur := ""
		if ms.ID == m.modelID {
			cur = "  ✓"
		}
		row := Line{S(pref, SAccent), S(formatModelOptionLabel(ag, ms), st)}
		if !strings.EqualFold(ms.ID, "default") && !strings.EqualFold(ms.Label, ms.ID) {
			row = append(row, S(" ("+ms.ID+")", SFaint))
		}
		row = append(row, S(cur, SOk))
		out = append(out, row)
		idx++
	}
	// Custom model row
	inCatalog := false
	for _, ms := range ag.Models {
		if ms.ID == m.modelID {
			inCatalog = true
			break
		}
	}
	pref := "  "
	st := SSec
	if m.sel == idx {
		pref = "▸ "
		st = SText.Bold(true)
	}
	if m.editCustom {
		ed := m.custom.Lines(max(12, w-18), 1, SText, "type model ID…", true)
		out = append(out, append(Line{S("▸ Custom ID: ", SAccent)}, ed[0]...))
	} else if !inCatalog && m.modelID != "" {
		out = append(out, Line{S(pref, SAccent), S("Custom model ID: "+m.modelID, st), S("  ✓", SOk)})
	} else {
		out = append(out, Line{S(pref, SAccent), S("Custom model ID…", st)})
	}
	idx++

	if len(efforts) > 0 {
		pref = "  "
		st = SSec
		if m.sel == idx {
			pref = "▸ "
			st = SText.Bold(true)
		}
		out = append(out, Line{S(pref, SAccent), S("Reasoning effort: ", SMuted), S("◂ "+string(m.effort)+" ▸", st)})
		idx++
	}
	if isCodex {
		pref = "  "
		st = SSec
		if m.sel == idx {
			pref = "▸ "
			st = SText.Bold(true)
		}
		fm := "off"
		if m.fastMode {
			fm = "on (fast service tier)"
		}
		out = append(out, Line{S(pref, SAccent), S("Codex fast mode:  ", SMuted), S("◂ "+fm+" ▸", st)})
	}
	if m.errMsg != "" {
		out = append(out, Line{S("  ✕ "+m.errMsg, SDanger)})
	} else {
		out = append(out, Line{S("  ↑/↓ move · Enter apply · ←/→ cycle effort/fast · Esc close", SFaint)})
	}
	return out
}

func projectOfSession(s wire.Session) string {
	if s.ProjectID == "" {
		return wire.DefaultProjectID
	}
	return s.ProjectID
}

// resolvedDefaultModel returns the concrete default model name for a harness
// from GET /agents when available (either a non-"default" DefaultModel or the
// first concrete live-discovered model in Models).
func resolvedDefaultModel(ag wire.AgentSpec) string {
	if dm := strings.TrimSpace(ag.DefaultModel); dm != "" && !strings.EqualFold(dm, "default") {
		for _, m := range ag.Models {
			if m.ID == dm {
				if m.Label != "" {
					return m.Label
				}
				return m.ID
			}
		}
		return dm
	}
	for _, m := range ag.Models {
		id := strings.ToLower(strings.TrimSpace(m.ID))
		if id == "" || id == "default" || id == "opus" || id == "sonnet" || id == "haiku" || id == "fable" {
			continue
		}
		if m.Label != "" {
			return m.Label
		}
		return m.ID
	}
	return ""
}

// formatModelOptionLabel renders a catalog model option, annotating "default"
// with the actual resolved model name in parentheses when known
// (e.g. "CLI Default (gemini-4-neon)").
func formatModelOptionLabel(ag wire.AgentSpec, ms wire.AgentModelSpec) string {
	lbl := strings.TrimSpace(ms.Label)
	if lbl == "" {
		lbl = ms.ID
	}
	if strings.EqualFold(ms.ID, "default") {
		if resolved := resolvedDefaultModel(ag); resolved != "" {
			return lbl + " (" + resolved + ")"
		}
	}
	return lbl
}

// displayModel formats a session's model for headers and status footers,
// showing "default (<resolved>)" when using the harness default and the actual
// model name is known from GET /agents.
func (a *app) displayModel(s wire.Session) string {
	m := strings.TrimSpace(s.Model)
	if m != "" && !strings.EqualFold(m, "default") {
		return m
	}
	hid := s.HarnessID()
	for _, ag := range a.agents {
		if strings.EqualFold(ag.ID, hid) {
			if resolved := resolvedDefaultModel(ag); resolved != "" {
				return "default (" + resolved + ")"
			}
			break
		}
	}
	return "default"
}

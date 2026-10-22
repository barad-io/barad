package tui

import (
	"context"
	"image/color"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// ---------------------------------------------------------------- the menu
//
// One menu holds every rare thing, in groups: what needs you now, the
// conversation, the session's settings under its name, and Barad's own
// settings. Type to filter, Enter to choose, Esc to go back. A menu can
// instead hold one text field (Rename).

type item struct {
	group             string // the heading it sits under; items of a group are listed together
	label, value, key string
	danger, check     bool
	ask, action       string // what a dangerous item asks before it acts, and its button (the label when empty)
	stay              bool   // the menu stays open after it runs (a setting you toggle)
	run               func(a *app) tea.Cmd
	sub               func(a *app) *menu
}

type menu struct {
	title   string
	items   []item
	sel     int
	q       Editor
	parent  *menu
	confirm *item  // a dangerous item, asking before it acts
	hint    string // a line above the items: what is loading, or why there is nothing
	edit    *Editor
	submit  func(a *app, text string) tea.Cmd
	about   string             // what an edit field takes, shown when it is empty
	mask    bool               // the edit field is a secret: it shows dots
	reload  func(a *app) *menu // rebuilds the menu when what it lists arrives
	load    tea.Cmd            // fetches what the menu lists, when it opens
}

func (mn *menu) visible() []item {
	var out []item
	for _, it := range mn.items {
		if Fuzzy(mn.q.Value(), it.label) {
			out = append(out, it)
		}
	}
	return out
}

// showMenu shows a menu, its selection on the current choice.
func (a *app) showMenu(mn *menu) {
	for i, it := range mn.items {
		if it.check {
			mn.sel = i
		}
	}
	a.menu = mn
}

// reloadMenu rebuilds the open menu when what it shows changes, keeping
// your place in it.
func (a *app) reloadMenu() {
	if mn := a.menu; mn != nil && mn.reload != nil {
		next := mn.reload(a)
		next.parent, next.q, next.sel = mn.parent, mn.q, max(0, min(mn.sel, len(next.items)-1)) // where you were
		a.menu = next
	}
}

func (a *app) menuKey(k tea.KeyPressMsg) tea.Cmd {
	mn := a.menu
	ks := k.String()
	if mn.confirm != nil {
		switch ks {
		case "enter":
			it := *mn.confirm
			a.menu = nil
			return it.run(a)
		case "esc":
			mn.confirm = nil
		}
		return nil
	}
	if mn.edit != nil {
		switch ks {
		case "esc":
			a.menu = mn.parent
			a.reloadMenu()
		case "enter":
			if text := strings.TrimSpace(mn.edit.Value()); text != "" {
				a.menu = nil
				return mn.submit(a, text)
			}
		default:
			mn.edit.Key(k, 0)
		}
		return nil
	}
	vis := mn.visible()
	switch ks {
	case "esc":
		a.menu = mn.parent
		a.reloadMenu()
	case "up", "ctrl+p", "shift+tab":
		mn.sel = max(0, mn.sel-1)
	case "down", "ctrl+n", "tab":
		mn.sel = min(len(vis)-1, mn.sel+1)
	case "enter":
		if mn.sel >= len(vis) {
			return nil
		}
		it := vis[mn.sel]
		switch {
		case it.sub != nil:
			sub := it.sub(a)
			sub.parent = mn
			a.showMenu(sub)
			return sub.load
		case it.danger:
			mn.confirm = &it
		case it.run != nil && it.stay:
			cmd := it.run(a)
			a.reloadMenu()
			return cmd
		case it.run != nil:
			a.menu = nil
			return it.run(a)
		}
	default:
		mn.q.Key(k, 0)
		mn.sel = 0
	}
	return nil
}

type signOutMsg struct{ err error }

// projectMenu lists the projects, cur checked; choosing one calls pick.
func (a *app) projectMenu(cur string, pick func(a *app, id string) tea.Cmd) *menu {
	projects := a.projects
	if !slices.ContainsFunc(projects, func(p wire.Project) bool { return p.ProjectID == wire.DefaultProjectID }) {
		projects = append([]wire.Project{{ProjectID: wire.DefaultProjectID, Name: wire.DefaultProjectName}}, projects...)
	}
	var items []item
	for _, p := range projects {
		if !p.ClosingAt.IsZero() {
			continue
		}
		id, name := p.ProjectID, p.Name
		items = append(items, item{label: name, check: id == cur, run: func(a *app) tea.Cmd { return pick(a, id) }})
	}
	return &menu{title: "Project", items: items}
}

type sharedMsg struct {
	share *api.Share
	err   error
}

func (a *app) updateSettings(st api.Settings, say string) tea.Cmd {
	id := a.conv.id
	return call(func(ctx context.Context) (*wire.Session, error) { return a.api.UpdateSettings(ctx, id, st) },
		func(s *wire.Session, err error) tea.Msg {
			if err != nil {
				return doneMsg{say: "Could not change the session", err: err}
			}
			return doneMsg{say: say, session: s, relist: true}
		})
}

// efforts are the reasoning efforts a model takes, from its connection's
// catalog (or, before that loads, what the session reports).
func (a *app) efforts(provider, name string, fallback []wire.ReasoningEffort) []wire.ReasoningEffort {
	if cat := a.catalogs[provider]; cat != nil {
		for _, m := range cat.Models {
			if m.Model == name {
				if !m.SupportsReasoningEffort {
					return nil
				}
				return m.ReasoningEfforts
			}
		}
	}
	return fallback
}

// effortFor keeps the effort when the model takes it, else the model's own default.
func (a *app) effortFor(provider, name string, current wire.ReasoningEffort) wire.ReasoningEffort {
	efforts := a.efforts(provider, name, nil)
	if len(efforts) == 0 {
		return ""
	}
	if slices.Contains(efforts, current) {
		return current
	}
	if cat := a.catalogs[provider]; cat != nil {
		for _, m := range cat.Models {
			if m.Model == name && slices.Contains(efforts, m.DefaultReasoningEffort) {
				return m.DefaultReasoningEffort
			}
		}
	}
	return efforts[0]
}

func effortLabel(e wire.ReasoningEffort) string {
	if l, ok := map[wire.ReasoningEffort]string{"none": "none", "minimal": "minimal", "low": "low", "medium": "medium", "high": "high", "xhigh": "extra high", "max": "maximum"}[e]; ok {
		return l
	}
	return string(e)
}

func (a *app) modelMenu() *menu {
	s := a.conv.session
	mn := &menu{title: "Model", reload: func(a *app) *menu { return a.modelMenu() }}
	cat := a.catalogs[s.ProviderResourceID]
	if cat == nil {
		mn.hint = "Loading models…"
		return mn
	}
	for _, m := range cat.Models {
		name := m.Model
		mn.items = append(mn.items, item{label: name, check: name == s.Model, run: func(a *app) tea.Cmd {
			s := a.conv.session
			st := api.SettingsOf(s)
			st.Model, st.ExpectedModel = name, s.Model
			st.ReasoningEffort = a.effortFor(s.ProviderResourceID, name, s.ReasoningEffort)
			return a.updateSettings(st, "Model: "+name)
		}})
	}
	return mn
}

func (a *app) effortMenu() *menu {
	s := a.conv.session
	var items []item
	for _, e := range a.efforts(s.ProviderResourceID, s.Model, s.SupportedReasoningEfforts) {
		items = append(items, item{label: effortLabel(e), check: e == s.ReasoningEffort, run: func(a *app) tea.Cmd {
			st := api.SettingsOf(a.conv.session)
			st.ReasoningEffort = e
			return a.updateSettings(st, "Reasoning effort: "+effortLabel(e))
		}})
	}
	return &menu{title: "Reasoning effort", items: items}
}

// menuView draws the open menu as a sheet: a strip of ground with its
// title, then its filter and items on a raised body.
func (a *app) menuView() string {
	mn := a.menu
	w := min(76, a.w-8)
	inner := w - 8
	quiet := Fg(lands[a.prefs.Theme].quiet)
	var lines []Line
	if mn.confirm != nil {
		lines = append(lines, Wrap([]Seg{S(mn.confirm.ask, SSec)}, inner)...)
		onDanger := map[string]color.Color{"night": Canvas, "paper": Raised}[a.prefs.Theme]
		lines = append(lines, Line{}, append(append(button("enter", confirmAction(*mn.confirm), onDanger, Danger), gap(2)), button("esc", "Cancel", Text, Muted)...))
		title := Line{S("✕  ", Fg(groundDanger)), S(confirmAction(*mn.confirm)+"?", Fg(groundText).Bold(true))}
		return sheet(a.prefs.Theme, title, Line{S("esc", quiet)}, lines, w, hex("#5a1f33"))
	}
	if mn.edit != nil {
		shown := mn.edit
		if mn.mask { // a secret shows a dot for each character
			shown = &Editor{Text: []rune(strings.Repeat("•", len(mn.edit.Text))), Pos: mn.edit.Pos}
		}
		field := append(Line{S("› ", SAccent.Bold(true))}, shown.Lines(inner-4, 1, SText, mn.about, true)[0]...)
		lines = append(lines, on(append(append(Line{gap(1)}, field...), gap(inner-field.Width()-1)), Muted), Line{},
			append(append(button("enter", "Save", OnAccent, Accent), gap(2)), button("esc", "Cancel", Text, Muted)...))
		return sheet(a.prefs.Theme, Line{S(mn.title, Fg(groundText).Bold(true))}, Line{S("esc", quiet)}, lines, w, nil)
	}
	field := append(Line{S("› ", SAccent.Bold(true))}, mn.q.Lines(inner-4, 1, SText, "type to filter", true)[0]...)
	lines = append(lines, on(append(append(Line{gap(1)}, field...), gap(inner-field.Width()-1)), Muted), Line{})
	if mn.hint != "" {
		lines = append(lines, Line{gap(3), S(mn.hint, SMuted)})
	}
	vis := mn.visible()
	// The items, each group under its heading. A long list scrolls, keeping
	// the selection in view.
	type row struct {
		head string
		i    int // the item; -1 for a heading or the space before one
	}
	var rows []row
	group, selRow := "", 0
	for i, it := range vis {
		if it.group != group || i == 0 {
			if it.group != "" {
				if len(rows) > 0 {
					rows = append(rows, row{i: -1})
				}
				rows = append(rows, row{head: it.group, i: -1})
			}
			group = it.group
		}
		if i == mn.sel {
			selRow = len(rows)
		}
		rows = append(rows, row{i: i})
	}
	most := max(8, a.h-a.h/6-14) // as many rows as fit under the sheet's top
	first := min(max(0, selRow-most/2), max(0, len(rows)-most))
	for _, r := range rows[first:min(len(rows), first+most)] {
		if r.i < 0 {
			if r.head != "" {
				lines = append(lines, Line{gap(3), S(ansi.Truncate(r.head, inner-4, "…"), SFaint.Bold(true))})
			} else {
				lines = append(lines, Line{})
			}
			continue
		}
		it := vis[r.i]
		st := SText
		if it.danger {
			st = SDanger
		}
		if r.i == mn.sel {
			st = st.Bold(true)
		}
		mark := gap(2)
		if it.check {
			mark = S("✓ ", SOk)
		}
		right := Line{}
		if it.value != "" {
			right = append(right, S(ansi.Truncate(it.value, 28, "…"), SMuted))
		}
		if it.key != "" {
			right = append(right, gap(3), S(it.key, SFaint))
		}
		if it.sub != nil {
			right = append(right, S(" ›", SMuted))
		}
		l := spread(Line{gap(1), mark, S(ansi.Truncate(it.label, inner-right.Width()-6, "…"), st)}, append(right, gap(1)), inner)
		if r.i == mn.sel {
			l = on(l, Muted)
			l[0] = S("▍", Fg(Accent).Background(Muted))
		}
		lines = append(lines, l)
	}
	if len(vis) == 0 && mn.hint == "" {
		lines = append(lines, Line{gap(3), S("Nothing matches.", SMuted)})
	}
	if first+most < len(rows) {
		lines = append(lines, Line{gap(3), S("↓ more", SFaint)})
	}
	return sheet(a.prefs.Theme, Line{S(ansi.Truncate(mn.title, inner-6, "…"), Fg(groundText).Bold(true))}, Line{S("esc", quiet)}, lines, w, nil)
}

// ---------------------------------------------------------------- a new session

// A starter is the sheet that starts a session: what to work on, then the
// agent, repository and project it starts with. It begins from the
// project's defaults, as the web form does.
type starter struct {
	task     Editor
	field    int // the focused row: 0 the task, then agent, effort, repositories, environment, project
	provider string
	model    string
	effort   wire.ReasoningEffort
	repos    []api.RepositoryRequest    // cloned when the workspace is made, each at its branch ("" its default)
	env      []wire.EnvironmentVariable // what its agent's commands and shells receive
	project  string
	busy     bool
}

const (
	fieldTask = iota
	fieldAgent
	fieldEffort
	fieldRepo
	fieldEnv
	fieldProject
)

// newSession opens the sheet, starting from the current project.
func (a *app) newSession(prompt string) tea.Cmd {
	st := &starter{}
	st.task.Insert(prompt)
	a.start = st
	a.applyProject(a.projectID())
	var cmds []tea.Cmd
	if a.repos == nil {
		cmds = append(cmds, call(a.api.Repositories, func(r []api.Repository, err error) tea.Msg { return reposMsg{r, err} }))
	}
	for _, p := range a.providers { // every connection's models, for the agent picker
		if p.Ready {
			cmds = append(cmds, a.loadCatalog(p.ResourceID))
		}
	}
	return tea.Batch(cmds...)
}

// applyProject takes a project's starting agent and its repositories.
func (a *app) applyProject(id string) {
	st := a.start
	st.project, st.repos = id, nil
	project := a.project(id)
	for _, r := range project.Repositories {
		st.repos = append(st.repos, api.RepositoryRequest{SourceID: r.SourceID, Repository: r.Repository})
	}
	if ag := project.Agent; ag != nil && a.ready(ag.ProviderResourceID) {
		st.provider, st.model, st.effort = ag.ProviderResourceID, ag.Model, ag.ReasoningEffort
	} else if !a.ready(st.provider) {
		st.provider, st.model, st.effort = a.usualProvider(), "", ""
	}
	st.settle(a)
}

func (a *app) ready(provider string) bool {
	return slices.ContainsFunc(a.providers, func(p wire.ProviderView) bool { return p.ResourceID == provider && p.Ready })
}

// usualProvider is the connection your newest session uses, or the first ready one.
func (a *app) usualProvider() string {
	var newest wire.Session
	for _, s := range a.sessions {
		if a.ready(s.ProviderResourceID) && s.CreatedAt.After(newest.CreatedAt) {
			newest = s
		}
	}
	if newest.ProviderResourceID != "" {
		return newest.ProviderResourceID
	}
	for _, p := range a.providers {
		if p.Ready {
			return p.ResourceID
		}
	}
	return ""
}

// settle makes the model and effort valid for the connection's catalog,
// once it has loaded.
func (st *starter) settle(a *app) {
	cat := a.catalogs[st.provider]
	if cat == nil {
		return
	}
	if !slices.ContainsFunc(cat.Models, func(m wire.ModelCapabilities) bool { return m.Model == st.model }) {
		st.model = cat.DefaultModel
	}
	st.effort = a.effortFor(st.provider, st.model, st.effort)
}

func (a *app) providerOf(id string) wire.ProviderView {
	for _, p := range a.providers {
		if p.ResourceID == id {
			return p
		}
	}
	return wire.ProviderView{}
}

func (a *app) startKey(k tea.KeyPressMsg) tea.Cmd {
	st := a.start
	if st.busy {
		if k.String() == "esc" {
			a.start = nil
		}
		return nil
	}
	rows := []int{fieldTask, fieldAgent, fieldEffort, fieldRepo, fieldEnv, fieldProject}
	if len(a.efforts(st.provider, st.model, nil)) == 0 {
		rows = slices.DeleteFunc(rows, func(r int) bool { return r == fieldEffort })
	}
	i := max(0, slices.Index(rows, st.field))
	switch k.String() {
	case "esc":
		a.start = nil
		return nil
	case "tab", "down":
		if k.String() == "tab" || st.field != fieldTask {
			st.field = rows[(i+1)%len(rows)]
			return nil
		}
	case "shift+tab", "up":
		if k.String() == "shift+tab" || st.field != fieldTask {
			st.field = rows[(i+len(rows)-1)%len(rows)]
			return nil
		}
	case "enter":
		switch st.field {
		case fieldTask:
			return a.begin()
		case fieldAgent:
			mn := a.agentMenu(st.provider, st.model, func(a *app, provider, model string) tea.Cmd {
				a.start.provider, a.start.model = provider, model
				a.start.settle(a)
				return nil
			})
			a.showMenu(mn)
			return mn.load
		case fieldEffort:
			var items []item
			for _, e := range a.efforts(st.provider, st.model, nil) {
				items = append(items, item{label: effortLabel(e), check: e == st.effort, run: func(a *app) tea.Cmd { a.start.effort = e; return nil }})
			}
			a.showMenu(&menu{title: "Reasoning effort", items: items})
		case fieldRepo:
			mn := a.startReposMenu()
			a.showMenu(mn)
			return mn.load
		case fieldEnv:
			a.showMenu(a.envMenu("Environment", "The new session's agent commands and terminal shells receive these.",
				func(a *app) []wire.EnvironmentVariable { return a.start.env },
				func(a *app, v wire.EnvironmentVariable) tea.Cmd {
					a.start.env = append(slices.DeleteFunc(a.start.env, func(x wire.EnvironmentVariable) bool { return x.Name == v.Name }), v)
					return nil
				},
				func(a *app, name string) tea.Cmd {
					a.start.env = slices.DeleteFunc(a.start.env, func(x wire.EnvironmentVariable) bool { return x.Name == name })
					return nil
				}))
		case fieldProject:
			a.showMenu(a.projectMenu(st.project, func(a *app, id string) tea.Cmd { a.applyProject(id); return nil }))
		}
		return nil
	}
	if st.field == fieldTask {
		st.task.Key(k, a.taskWidth())
	}
	return nil
}

// begin creates the session, then sends it the task.
func (a *app) begin() tea.Cmd {
	st := a.start
	task := strings.TrimSpace(st.task.Value())
	switch {
	case task == "":
		return nil
	case st.provider == "" || st.model == "":
		a.say("Choose an agent first")
		a.toastBad = true
		return nil
	}
	st.busy = true
	in := api.NewSession{ProviderResourceID: st.provider, Model: st.model, ReasoningEffort: st.effort, ProjectID: st.project, Repositories: st.repos, Environment: st.env}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, err := a.api.CreateSession(ctx, in)
		if err != nil {
			return createdMsg{err: err}
		}
		_, err = a.api.SendMessage(ctx, s.SessionID, randomID("msg_", 8), task, nil)
		return createdMsg{session: s, prompt: task, err: err}
	}
}

// startWidth is the new session sheet's width; its task field is taskWidth.
func (a *app) startWidth() int { return min(88, a.w-8) }
func (a *app) taskWidth() int  { return a.startWidth() - 8 - 6 }

// startView draws the new session sheet.
func (a *app) startView() string {
	st := a.start
	w := a.startWidth()
	inner := w - 8
	var lines []Line
	field := st.task.Lines(a.taskWidth(), 6, SText, "What should the new session work on?", st.field == fieldTask && a.menu == nil)
	for i, l := range field {
		lead := gap(2)
		if i == 0 {
			lead = S("› ", SAccent.Bold(true))
		}
		row := append(Line{gap(1), lead}, l...)
		lines = append(lines, on(append(row, gap(inner-row.Width())), Muted))
	}
	lines = append(lines, Line{})

	p := a.providerOf(st.provider)
	mark, mc := providerMark(string(p.ProviderType))
	agent := Line{S(mark+" ", Fg(mc)), S(st.model, SText), S("  ·  "+p.Name, SMuted)}
	if st.provider == "" {
		agent = Line{S("no provider connected", SDanger)}
	} else if st.model == "" {
		agent = Line{S(Spinner[a.frame%10]+" ", SAgent), S("loading models", SMuted)}
	}
	repo := Line{S("none", SMuted), S("  ·  a blank workspace", SFaint)}
	if len(st.repos) > 0 {
		var names []string
		for _, r := range st.repos {
			n := r.Repository
			if r.RequestedRef != "" {
				n += " @" + r.RequestedRef
			}
			names = append(names, n)
		}
		repo = Line{S(ansi.Truncate(strings.Join(names, ", "), inner-30, "…"), SText)}
	}
	env := Line{S("none", SMuted)}
	if len(st.env) > 0 {
		var names []string
		for _, v := range st.env {
			names = append(names, v.Name)
		}
		env = Line{S(ansi.Truncate(strings.Join(names, ", "), inner-30, "…"), SText)}
	}
	rows := []struct {
		field int
		k     string
		v     Line
	}{
		{fieldAgent, "AGENT", agent},
		{fieldEffort, "EFFORT", Line{S(effortLabel(st.effort), SText)}},
		{fieldRepo, "REPOSITORIES", repo},
		{fieldEnv, "ENVIRONMENT", env},
		{fieldProject, "PROJECT", Line{S(a.projectName(st.project), SText), S("  ·  its variables and defaults", SMuted)}},
	}
	for _, r := range rows {
		if r.field == fieldEffort && len(a.efforts(st.provider, st.model, nil)) == 0 {
			continue
		}
		l := append(Line{gap(1), S(PadRight(r.k, 14), SFaint.Bold(true))}, r.v...)
		if st.field == r.field {
			l = spread(l, Line{S("enter  change", SMuted), gap(1)}, inner)
			l = on(l, Muted)
			l[0] = S("▍", Fg(Accent).Background(Muted))
		}
		lines = append(lines, l)
	}
	lines = append(lines, Line{})
	if st.busy {
		lines = append(lines, Line{S(Spinner[a.frame%10]+"  ", SAgent), S("Starting the session", SSec)})
	} else {
		lines = append(lines, append(append(button("enter", "Start", OnAccent, Accent), gap(2)), button("tab", "Next field", Text, Muted)...))
	}
	quiet := Fg(lands[a.prefs.Theme].quiet)
	return sheet(a.prefs.Theme, Line{S("New session", Fg(groundText).Bold(true))}, Line{S(a.projectName(st.project), quiet)}, lines, w, nil)
}

// confirmAction is what a dangerous item's button says.
func confirmAction(it item) string {
	if it.action != "" {
		return it.action
	}
	return it.label
}

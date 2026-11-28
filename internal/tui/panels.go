package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/wire"
)

// listOverlay is a clean reusable modal for /settings, /files, /tools,
// /projects, /usage, /billing, and /account. It supports selectable rows,
// inline text input, confirmation prompts, scrollable detail views, and a
// navigation stack so Esc steps back cleanly.
type listOverlay struct {
	title    string
	hint     string
	items    []listItem
	sel      int
	parent   *listOverlay
	input    *inputPrompt
	confirm  *confirmPrompt
	viewer   *textViewer
	onKey    func(a *app, k tea.KeyPressMsg) (tea.Cmd, bool)
	headerFn func(a *app, w int) []Line
}

type listItem struct {
	label  string
	value  string
	danger bool
	run    func(a *app) tea.Cmd
}

type inputPrompt struct {
	label  string
	editor Editor
	secret bool
	submit func(a *app, value string) tea.Cmd
}

type confirmPrompt struct {
	question string
	run      func(a *app) tea.Cmd
}

type textViewer struct {
	title  string
	lines  []Line
	scroll int
	footer string
}

func (o *listOverlay) Title() string {
	if o.viewer != nil {
		return o.viewer.title
	}
	return o.title
}

func (o *listOverlay) push(child *listOverlay) *listOverlay {
	child.parent = o
	return child
}

func (o *listOverlay) Key(a *app, k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	if o.viewer != nil {
		switch ks {
		case "esc", "q", "enter":
			o.viewer = nil
		case "up", "k":
			if o.viewer.scroll > 0 {
				o.viewer.scroll--
			}
		case "down", "j":
			if o.viewer.scroll < max(0, len(o.viewer.lines)-6) {
				o.viewer.scroll++
			}
		case "pgup":
			o.viewer.scroll = max(0, o.viewer.scroll-10)
		case "pgdown":
			o.viewer.scroll = min(max(0, len(o.viewer.lines)-6), o.viewer.scroll+10)
		}
		return nil
	}
	if o.confirm != nil {
		switch ks {
		case "y", "Y", "enter":
			fn := o.confirm.run
			o.confirm = nil
			return fn(a)
		case "n", "N", "esc":
			o.confirm = nil
		}
		return nil
	}
	if o.input != nil {
		switch ks {
		case "esc":
			o.input = nil
		case "enter":
			val := strings.TrimSpace(o.input.editor.Value())
			fn := o.input.submit
			o.input = nil
			return fn(a, val)
		default:
			o.input.editor.Key(k, max(16, a.w-6))
		}
		return nil
	}
	if o.onKey != nil {
		if cmd, handled := o.onKey(a, k); handled {
			return cmd
		}
	}
	switch ks {
	case "esc":
		if o.parent != nil {
			a.ov = o.parent
		} else {
			a.ov = nil
		}
		return nil
	case "up", "k":
		if o.sel > 0 {
			o.sel--
		}
	case "down", "j":
		if o.sel < len(o.items)-1 {
			o.sel++
		}
	case "enter":
		if o.sel >= 0 && o.sel < len(o.items) && o.items[o.sel].run != nil {
			return o.items[o.sel].run(a)
		}
	}
	return nil
}

func (o *listOverlay) View(a *app, w, maxH int) []Line {
	if o.viewer != nil {
		vis := max(4, maxH-1)
		start := min(o.viewer.scroll, max(0, len(o.viewer.lines)-1))
		end := min(len(o.viewer.lines), start+vis)
		out := append([]Line{}, o.viewer.lines[start:end]...)
		foot := o.viewer.footer
		if foot == "" {
			foot = fmt.Sprintf("Lines %d–%d of %d · ↑/↓ scroll · Esc back", start+1, end, len(o.viewer.lines))
		}
		out = append(out, Line{S(foot, SFaint)})
		return out
	}
	if o.confirm != nil {
		return []Line{
			{S("⚠ ", SWarn.Bold(true)), S(o.confirm.question, SText.Bold(true))},
			{S("  Press y/Enter to confirm, or n/Esc to cancel", SMuted)},
		}
	}
	if o.input != nil {
		var ed []Line
		if o.input.secret {
			stars := strings.Repeat("•", len(o.input.editor.Text))
			ed = []Line{{S(stars, SText), S(" ", SCursor)}}
		} else {
			ed = o.input.editor.Lines(max(12, w-4), 1, SText, "", true)
		}
		return []Line{
			{S(o.input.label, SMuted)},
			append(Line{S("❯ ", SAccent)}, ed[0]...),
			{S("  Enter save · Esc cancel", SFaint)},
		}
	}

	var out []Line
	if o.headerFn != nil {
		out = append(out, o.headerFn(a, w)...)
	}
	if len(o.items) == 0 {
		if o.headerFn == nil {
			out = append(out, Line{S("  (empty)", SFaint)})
		}
	} else {
		listH := max(3, maxH-len(out)-1)
		start := 0
		if o.sel >= listH {
			start = o.sel - listH + 1
		}
		for i := start; i < min(len(o.items), start+listH); i++ {
			it := o.items[i]
			pref := "  "
			st := SSec
			if it.danger {
				st = SDanger
			}
			if i == o.sel {
				pref = "▸ "
				st = st.Bold(true)
				if !it.danger {
					st = SText.Bold(true)
				}
			}
			left := Line{S(pref, SAccent), S(it.label, st)}
			if it.value != "" {
				valW := max(8, w-left.Width()-3)
				right := Line{S(ansi.Truncate(it.value, valW, "…"), SFaint)}
				out = append(out, spread(left, right, w))
			} else {
				out = append(out, left)
			}
		}
	}
	if o.hint != "" {
		out = append(out, Line{S("  "+o.hint, SFaint)})
	}
	return out
}

// ---------------------------------------------------------------- /settings

func (a *app) openSettingsOverlay() tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first (/sessions)", true)
		return nil
	}
	s := a.conv.session
	a.ov = a.buildSettingsOverlay(s)
	return tea.Batch(a.loadSessionRepos(s.SessionID), a.loadEnv(api.EnvironmentOwner{Kind: "sessions", ID: s.SessionID}))
}

func (a *app) buildSettingsOverlay(s wire.Session) *listOverlay {
	projName := wire.DefaultProjectName
	for _, p := range a.projects {
		if p.ProjectID == projectOfSession(s) {
			projName = p.Name
			break
		}
	}
	repoCount := fmt.Sprintf("%d connected", len(a.sessionRepos[s.SessionID]))
	envCount := "—"
	if ev := a.envs["sessions/"+s.SessionID]; ev != nil {
		envCount = fmt.Sprintf("%d vars", len(ev.Variables))
	}

	ov := &listOverlay{
		title: "Session settings · " + sessionTitle(s),
		hint:  "↑/↓ move · Enter select · Esc close",
	}

	// Open session proposal actions if present.
	if prop, ok := a.conv.openProposal(); ok {
		ov.items = append(ov.items,
			listItem{label: "Approve proposed session", value: prop.Proposal.Title, run: func(a *app) tea.Cmd {
				return call(func(ctx context.Context) (*wire.SessionProposalDecision, error) {
					return a.api.ApproveProposal(ctx, s, prop)
				}, func(d *wire.SessionProposalDecision, err error) tea.Msg {
					if err != nil {
						return doneMsg{err: err}
					}
					return doneMsg{say: "Proposal approved", relist: true, open: d.DestinationID}
				})
			}},
			listItem{label: "Dismiss proposed session", value: prop.Proposal.Title, run: func(a *app) tea.Cmd {
				return call(func(ctx context.Context) (struct{}, error) {
					return struct{}{}, a.api.DismissProposal(ctx, s.SessionID, prop.Sequence)
				}, func(_ struct{}, err error) tea.Msg {
					return doneMsg{say: "Proposal dismissed", err: err, resync: true}
				})
			}},
		)
	}

	if !s.IsV2() {
		ov.items = append(ov.items, listItem{label: "Continue in new v2 session", value: "migrates disk + transcript", run: func(a *app) tea.Cmd {
			a.ov = nil
			return a.continueCurrentSession()
		}})
	}

	ov.items = append(ov.items,
		listItem{label: "Rename session", value: sessionTitle(s), run: func(a *app) tea.Cmd {
			ed := Editor{}
			ed.Insert(s.Name)
			ov.input = &inputPrompt{
				label: "New session name:", editor: ed,
				submit: func(a *app, val string) tea.Cmd {
					if val == "" {
						return nil
					}
					st := api.SettingsOf(s)
					st.Name = val
					return call(func(ctx context.Context) (*wire.Session, error) {
						return a.api.UpdateSettings(ctx, s.SessionID, st)
					}, func(u *wire.Session, err error) tea.Msg {
						return doneMsg{say: "Renamed session", err: err, session: u, relist: true}
					})
				},
			}
			return nil
		}},
		listItem{label: "Move to project", value: projName, run: func(a *app) tea.Cmd {
			sub := ov.push(&listOverlay{title: "Move to project", hint: "Enter move · Esc back"})
			ps := append([]wire.Project{{ProjectID: wire.DefaultProjectID, Name: wire.DefaultProjectName}}, a.projects...)
			seen := map[string]bool{}
			for _, p := range ps {
				if seen[p.ProjectID] || !p.ClosingAt.IsZero() {
					continue
				}
				seen[p.ProjectID] = true
				pr := p
				mark := ""
				if pr.ProjectID == projectOfSession(s) {
					mark = "current"
				}
				sub.items = append(sub.items, listItem{label: pr.Name, value: mark, run: func(a *app) tea.Cmd {
					a.ov = nil
					return call(func(ctx context.Context) (*wire.Session, error) {
						return a.api.MoveSession(ctx, s.SessionID, pr.ProjectID)
					}, func(u *wire.Session, err error) tea.Msg {
						return doneMsg{say: "Moved to " + pr.Name, err: err, session: u, relist: true}
					})
				}})
			}
			a.ov = sub
			return nil
		}},
		listItem{label: "Git repositories", value: repoCount, run: func(a *app) tea.Cmd {
			a.ov = ov.push(a.buildReposOverlay(s))
			if !a.reposLoaded {
				return a.loadRepos()
			}
			return nil
		}},
		listItem{label: "Environment & secrets", value: envCount, run: func(a *app) tea.Cmd {
			owner := api.EnvironmentOwner{Kind: "sessions", ID: s.SessionID}
			a.ov = ov.push(a.buildEnvOverlay(owner, "Session env · "+sessionTitle(s)))
			return a.loadEnv(owner)
		}},
		listItem{label: "Wake workspace VM", value: string(s.MachineStatus), run: func(a *app) tea.Cmd {
			a.ov = nil
			return call(func(ctx context.Context) (struct{}, error) {
				return struct{}{}, a.api.Wake(ctx, s.SessionID)
			}, func(_ struct{}, err error) tea.Msg {
				return doneMsg{say: "Waking workspace…", err: err, resync: true, relist: true}
			})
		}},
		listItem{label: "Publish share link", value: "read-only page", run: func(a *app) tea.Cmd {
			return call(func(ctx context.Context) (*api.Share, error) {
				return a.api.Share(ctx, s.SessionID, "")
			}, func(sh *api.Share, err error) tea.Msg {
				if err != nil {
					return doneMsg{err: err}
				}
				return shareMsg{url: sh.URL}
			})
		}},
		listItem{label: "Archive session", value: "erases workspace, keeps transcript", danger: true, run: func(a *app) tea.Cmd {
			ov.confirm = &confirmPrompt{
				question: "Archive " + sessionTitle(s) + "? Workspace disk is erased; transcript stays.",
				run: func(a *app) tea.Cmd {
					a.ov = nil
					return call(func(ctx context.Context) (*wire.Session, error) {
						return a.api.Archive(ctx, s.SessionID)
					}, func(u *wire.Session, err error) tea.Msg {
						return doneMsg{say: "Session archived", err: err, session: u, relist: true}
					})
				},
			}
			return nil
		}},
		listItem{label: "Delete session", value: "permanent", danger: true, run: func(a *app) tea.Cmd {
			ov.confirm = &confirmPrompt{
				question: "Permanently delete " + sessionTitle(s) + " and its transcript?",
				run: func(a *app) tea.Cmd {
					a.ov = nil
					return call(func(ctx context.Context) (struct{}, error) {
						return struct{}{}, a.api.DeleteSession(ctx, s.SessionID)
					}, func(_ struct{}, err error) tea.Msg {
						return doneMsg{say: "Deleting session…", err: err, relist: true, detach: true}
					})
				},
			}
			return nil
		}},
	)
	return ov
}

func (a *app) buildReposOverlay(s wire.Session) *listOverlay {
	sub := &listOverlay{title: "Git repositories · " + sessionTitle(s), hint: "Enter add/remove · Esc back"}
	have := map[string]wire.SessionGitRepository{}
	for _, r := range a.sessionRepos[s.SessionID] {
		have[strings.ToLower(r.CanonicalPath)] = r
		repo := r
		sub.items = append(sub.items, listItem{label: "✓ " + r.CanonicalPath, value: "Enter to remove", run: func(a *app) tea.Cmd {
			return call(func(ctx context.Context) (struct{}, error) {
				return struct{}{}, a.api.RemoveRepository(ctx, s.SessionID, repo)
			}, func(_ struct{}, err error) tea.Msg {
				return doneMsg{say: "Removed " + repo.CanonicalPath, err: err, reloadReposFor: s.SessionID}
			})
		}})
	}
	for _, r := range a.repos {
		if _, ok := have[strings.ToLower(r.FullName)]; ok {
			continue
		}
		repo := r
		sub.items = append(sub.items, listItem{label: "+ " + r.FullName, value: "Enter to grant", run: func(a *app) tea.Cmd {
			return call(func(ctx context.Context) (struct{}, error) {
				return struct{}{}, a.api.AddRepository(ctx, s.SessionID, repo.SourceID, repo.FullName)
			}, func(_ struct{}, err error) tea.Msg {
				return doneMsg{say: "Granted " + repo.FullName, err: err, reloadReposFor: s.SessionID}
			})
		}})
	}
	return sub
}

func (a *app) buildEnvOverlay(owner api.EnvironmentOwner, title string) *listOverlay {
	key := owner.Kind + "/" + owner.ID
	sub := &listOverlay{title: title, hint: "Enter to edit/remove · Esc back"}
	ev := a.envs[key]
	sub.items = append(sub.items,
		listItem{label: "+ Add variable (KEY=VALUE)", run: func(a *app) tea.Cmd {
			sub.input = &inputPrompt{
				label: "Enter NAME=VALUE:",
				submit: func(a *app, val string) tea.Cmd {
					name, v, ok := strings.Cut(val, "=")
					if !ok || strings.TrimSpace(name) == "" {
						a.flash("Use NAME=VALUE format", true)
						return nil
					}
					return a.mutateEnv(owner, []wire.EnvironmentVariable{{Name: strings.TrimSpace(name), Value: v}}, nil)
				},
			}
			return nil
		}},
		listItem{label: "+ Add secret variable (KEY=VALUE)", run: func(a *app) tea.Cmd {
			sub.input = &inputPrompt{
				label: "Enter NAME=VALUE (stored as secret):", secret: true,
				submit: func(a *app, val string) tea.Cmd {
					name, v, ok := strings.Cut(val, "=")
					if !ok || strings.TrimSpace(name) == "" {
						a.flash("Use NAME=VALUE format", true)
						return nil
					}
					return a.mutateEnv(owner, []wire.EnvironmentVariable{{Name: strings.TrimSpace(name), Value: v, Secret: true}}, nil)
				},
			}
			return nil
		}},
	)
	if ev != nil {
		for _, v := range ev.Variables {
			vr := v
			val := vr.Value
			if vr.Secret {
				val = "(secret)"
			}
			sub.items = append(sub.items, listItem{label: vr.Name, value: val + " · Enter removes", danger: true, run: func(a *app) tea.Cmd {
				sub.confirm = &confirmPrompt{
					question: "Remove variable " + vr.Name + "?",
					run:      func(a *app) tea.Cmd { return a.mutateEnv(owner, nil, []string{vr.Name}) },
				}
				return nil
			}})
		}
	}
	return sub
}

func (a *app) mutateEnv(owner api.EnvironmentOwner, set []wire.EnvironmentVariable, remove []string) tea.Cmd {
	return call(func(ctx context.Context) (*wire.EnvironmentView, error) {
		cur, err := a.api.Environment(ctx, owner)
		if err != nil {
			return nil, err
		}
		return a.api.ChangeEnvironment(ctx, owner, wire.EnvironmentMutation{
			ExpectedRevision: cur.Revision, SetVariables: set, RemoveVariables: remove,
		})
	}, func(view *wire.EnvironmentView, err error) tea.Msg {
		return envLoadedMsg{key: owner.Kind + "/" + owner.ID, view: view, err: err, saved: true}
	})
}

// ---------------------------------------------------------------- /files

func (a *app) openFilesOverlay(dir string) tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first (/sessions)", true)
		return nil
	}
	if a.conv.session.MachineStatus != wire.MachineStatusHydrated {
		a.flash("Workspace is sleeping — wake it first (/settings → Wake)", true)
		return nil
	}
	if dir == "" {
		dir = "."
	}
	ov := &listOverlay{title: "Files · /workspace/" + strings.TrimPrefix(dir, "."), hint: "Loading…"}
	a.ov = ov
	return a.fetchFiles(a.conv.id, dir, true)
}

func (a *app) fetchFiles(sessionID, dir string, forOverlay bool) tea.Cmd {
	return call(func(ctx context.Context) ([]wire.FileEntry, error) {
		out, err := a.api.Command(ctx, sessionID, randomID("cmd_", 8), "fs_list", map[string]string{"path": dir})
		if err != nil {
			return nil, err
		}
		var list wire.FileList
		if err := json.Unmarshal(out.Result, &list); err != nil {
			return nil, err
		}
		return list.Entries, nil
	}, func(entries []wire.FileEntry, err error) tea.Msg {
		return filesLoadedMsg{sessionID: sessionID, dir: dir, entries: entries, err: err, openOverlay: forOverlay}
	})
}

func (a *app) buildFilesOverlay(sessionID, dir string, entries []wire.FileEntry) *listOverlay {
	disp := "/workspace"
	if dir != "" && dir != "." {
		disp = "/workspace/" + strings.TrimPrefix(dir, "/")
	}
	ov := &listOverlay{
		title: "Files · " + disp,
		hint:  "Enter open/preview · i insert @path · Esc close",
	}
	if dir != "" && dir != "." {
		parent := path.Dir(dir)
		ov.items = append(ov.items, listItem{label: "../", value: "parent directory", run: func(a *app) tea.Cmd {
			return a.fetchFiles(sessionID, parent, true)
		}})
	}
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Type == "directory") != (entries[j].Type == "directory") {
			return entries[i].Type == "directory"
		}
		return entries[i].Name < entries[j].Name
	})
	for _, e := range entries {
		entry := e
		full := entry.Name
		if dir != "" && dir != "." {
			full = path.Join(dir, entry.Name)
		}
		if entry.Type == "directory" {
			ov.items = append(ov.items, listItem{label: entry.Name + "/", value: "dir", run: func(a *app) tea.Cmd {
				return a.fetchFiles(sessionID, full, true)
			}})
		} else {
			ov.items = append(ov.items, listItem{label: entry.Name, value: fmt.Sprintf("%d B", entry.Size), run: func(a *app) tea.Cmd {
				return call(func(ctx context.Context) (string, error) {
					out, err := a.api.Command(ctx, sessionID, randomID("cmd_", 8), "fs_read", map[string]any{"path": full, "offset": 0, "length": 32768})
					if err != nil {
						return "", err
					}
					if out.Download == nil {
						return "(empty)", nil
					}
					var b strings.Builder
					if err := a.api.Download(ctx, out.Download.URL, &b); err != nil {
						return "", err
					}
					return b.String(), nil
				}, func(content string, err error) tea.Msg {
					return filePreviewMsg{path: full, content: content, err: err}
				})
			}})
		}
	}
	ov.onKey = func(a *app, k tea.KeyPressMsg) (tea.Cmd, bool) {
		if k.String() == "i" && ov.sel >= 0 && ov.sel < len(ov.items) {
			name := strings.TrimSuffix(ov.items[ov.sel].label, "/")
			if name != ".." {
				full := name
				if dir != "" && dir != "." {
					full = path.Join(dir, name)
				}
				a.comp.Insert("@" + full + " ")
				a.ov = nil
				a.flash("Inserted @"+full, false)
				return nil, true
			}
		}
		return nil, false
	}
	return ov
}

// ---------------------------------------------------------------- /tools

func (a *app) openToolsOverlay() tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first (/sessions)", true)
		return nil
	}
	steps := a.conv.allSteps()
	ov := &listOverlay{
		title: fmt.Sprintf("Tools & History (%d steps)", len(steps)),
		hint:  "Enter inspect diff/output · o load older history · Esc close",
	}
	if a.conv.hasOlder {
		ov.items = append(ov.items, listItem{label: "↑ Load older transcript history", value: "earlier turns", run: func(a *app) tea.Cmd {
			a.ov = nil
			return a.loadOlder()
		}})
	}
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		lbl := s.kind
		if t := strings.TrimSpace(toolSummary(s)); t != "" {
			if nl := strings.IndexByte(t, '\n'); nl >= 0 {
				t = t[:nl]
			}
			lbl += "  " + ansi.Truncate(t, 44, "…")
		} else if s.kind == "reasoning" {
			first, _, _ := strings.Cut(strings.TrimSpace(s.think), "\n")
			lbl = "reasoning  " + ansi.Truncate(first, 42, "…")
		}
		val := "✓"
		if s.result != nil {
			if f := readResult(s.result).failure(s.kind); f != "" {
				val = "✕ failed"
			}
		} else if s.kind != "reasoning" {
			val = "running"
		}
		stRef := s
		ov.items = append(ov.items, listItem{label: lbl, value: val, run: func(a *app) tea.Cmd {
			ov.viewer = &textViewer{
				title: "Inspect · " + stRef.kind,
				lines: renderStep(stRef, false, max(40, a.w-2), prefs.WorkFull, a.frame),
			}
			return nil
		}})
	}
	ov.onKey = func(a *app, k tea.KeyPressMsg) (tea.Cmd, bool) {
		if k.String() == "o" && a.conv.hasOlder {
			a.ov = nil
			return a.loadOlder(), true
		}
		return nil, false
	}
	a.ov = ov
	return nil
}

// ---------------------------------------------------------------- /projects

func (a *app) openProjectsOverlay() tea.Cmd {
	a.ov = a.buildProjectsOverlay()
	return a.loadProjects()
}

func (a *app) buildProjectsOverlay() *listOverlay {
	ov := &listOverlay{title: "Projects", hint: "Enter manage · Esc close"}
	ov.items = append(ov.items, listItem{label: "+ New project", run: func(a *app) tea.Cmd {
		ov.input = &inputPrompt{
			label: "New project name:",
			submit: func(a *app, name string) tea.Cmd {
				if name == "" {
					return nil
				}
				return call(func(ctx context.Context) (*wire.Project, error) {
					return a.api.CreateProject(ctx, api.ProjectSettings{Name: name, Repositories: []wire.ProjectRepository{}})
				}, func(p *wire.Project, err error) tea.Msg {
					return doneMsg{say: "Created project " + name, err: err, reloadProjects: true}
				})
			},
		}
		return nil
	}})
	ps := append([]wire.Project{{ProjectID: wire.DefaultProjectID, Name: wire.DefaultProjectName}}, a.projects...)
	seen := map[string]bool{}
	for _, p := range ps {
		if seen[p.ProjectID] || !p.ClosingAt.IsZero() {
			continue
		}
		seen[p.ProjectID] = true
		pr := p
		desc := "no default harness"
		if pr.Agent != nil {
			desc = strings.TrimSpace(pr.Agent.Agent + " " + pr.Agent.Model)
		}
		ov.items = append(ov.items, listItem{label: pr.Name, value: desc, run: func(a *app) tea.Cmd {
			sub := ov.push(&listOverlay{title: "Project · " + pr.Name, hint: "Enter select · Esc back"})
			sub.items = append(sub.items, listItem{label: "Environment & secrets", run: func(a *app) tea.Cmd {
				owner := api.EnvironmentOwner{Kind: "projects", ID: pr.ProjectID}
				a.ov = sub.push(a.buildEnvOverlay(owner, "Project env · "+pr.Name))
				return a.loadEnv(owner)
			}})
			if pr.ProjectID != wire.DefaultProjectID {
				sub.items = append(sub.items,
					listItem{label: "Rename project", value: pr.Name, run: func(a *app) tea.Cmd {
						ed := Editor{}
						ed.Insert(pr.Name)
						sub.input = &inputPrompt{
							label: "Rename project:", editor: ed,
							submit: func(a *app, name string) tea.Cmd {
								if name == "" {
									return nil
								}
								in := api.SettingsOfProject(pr)
								in.Name = name
								return call(func(ctx context.Context) (*wire.Project, error) {
									return a.api.UpdateProject(ctx, pr.ProjectID, in)
								}, func(_ *wire.Project, err error) tea.Msg {
									return doneMsg{say: "Renamed project", err: err, reloadProjects: true}
								})
							},
						}
						return nil
					}},
					listItem{label: "Delete project", value: "sessions move to Default", danger: true, run: func(a *app) tea.Cmd {
						sub.confirm = &confirmPrompt{
							question: "Delete project " + pr.Name + "? Sessions move to Default.",
							run: func(a *app) tea.Cmd {
								a.ov = ov
								return call(func(ctx context.Context) (struct{}, error) {
									return struct{}{}, a.api.DeleteProject(ctx, pr.ProjectID)
								}, func(_ struct{}, err error) tea.Msg {
									return doneMsg{say: "Deleted project " + pr.Name, err: err, reloadProjects: true}
								})
							},
						}
						return nil
					}},
				)
			}
			a.ov = sub
			return nil
		}})
	}
	return ov
}

// ---------------------------------------------------------------- /usage

func (a *app) openUsageOverlay() tea.Cmd {
	if a.usageDays == 0 {
		a.usageDays = 30
	}
	a.ov = a.buildUsageOverlay()
	return a.loadUsage()
}

func (a *app) buildUsageOverlay() *listOverlay {
	ov := &listOverlay{
		title: fmt.Sprintf("Usage · last %d days", a.usageDays),
		hint:  "←/→ range (7/30/90d) · Tab switch view · Esc close",
		headerFn: func(a *app, w int) []Line {
			if a.usage == nil || a.workspace == nil {
				return []Line{{S("  "+Spinner[a.frame%len(Spinner)]+" Loading usage…", SMuted)}}
			}
			bal := "—"
			if a.balance != nil {
				bal = formatMoney(a.balance.BalanceUSDNanos)
			}
			t, m := a.workspace.Totals, a.usage.Totals
			lines := []Line{
				{S("  Compute:   ", SMuted), S(formatMoney(t.ChargedUSDNanos), SText.Bold(true)), S(fmt.Sprintf("  (%dh %02dm · %d runs · %s balance)", t.BilledMinutes/60, t.BilledMinutes%60, t.Runs, bal), SFaint)},
				{S("  Inference: ", SMuted), S(formatTokens(m.ProcessedTokens)+" tokens", SText.Bold(true)), S(fmt.Sprintf("  (%s API equiv)", formatMoney(m.APIEquivalentCost.Nanodollars())), SFaint)},
			}
			if a.usageTab == 0 {
				lines = append(lines, Line{S("  By model (Tab for sessions):", SFaint)})
				for i, b := range a.usage.Models {
					if i >= 6 {
						break
					}
					lines = append(lines, spread(
						Line{S("    "+b.Model, SSec)},
						Line{S(formatTokens(b.Metrics.ProcessedTokens)+" tok · "+formatMoney(b.Metrics.APIEquivalentCost.Nanodollars()), SFaint)}, w))
				}
			} else {
				lines = append(lines, Line{S("  By session (Tab for models):", SFaint)})
				for i, ws := range a.workspace.Workspaces {
					if i >= 6 {
						break
					}
					name := ws.SessionID
					for _, s := range a.sessions {
						if s.SessionID == ws.SessionID {
							name = sessionTitle(s)
							break
						}
					}
					lines = append(lines, spread(
						Line{S("    "+ansi.Truncate(name, max(12, w/2), "…"), SSec)},
						Line{S(fmt.Sprintf("%dm · %s", ws.BilledMinutes, formatMoney(ws.ChargedUSDNanos)), SFaint)}, w))
				}
			}
			return lines
		},
		onKey: func(a *app, k tea.KeyPressMsg) (tea.Cmd, bool) {
			switch k.String() {
			case "tab":
				a.usageTab = 1 - a.usageTab
				return nil, true
			case "left", "right":
				ranges := []int{7, 30, 90}
				i := slices.Index(ranges, a.usageDays)
				if i < 0 {
					i = 1
				}
				if k.String() == "left" {
					i = max(0, i-1)
				} else {
					i = min(len(ranges)-1, i+1)
				}
				if ranges[i] != a.usageDays {
					a.usageDays = ranges[i]
					a.usage, a.workspace = nil, nil
					a.ov = a.buildUsageOverlay()
					return a.loadUsage(), true
				}
				return nil, true
			}
			return nil, false
		},
	}
	return ov
}

// ---------------------------------------------------------------- /billing

func (a *app) openBillingOverlay() tea.Cmd {
	a.ov = a.buildBillingOverlay()
	return a.loadBilling()
}

func (a *app) buildBillingOverlay() *listOverlay {
	ov := &listOverlay{
		title: "Billing & Credits",
		hint:  "Enter select · Esc close",
		headerFn: func(a *app, w int) []Line {
			if a.balance == nil {
				return []Line{{S("  "+Spinner[a.frame%len(Spinner)]+" Loading billing…", SMuted)}}
			}
			terms := "accepted (" + a.balance.TermsVersion + ")"
			if !a.balance.TermsAccepted {
				terms = "not accepted"
			}
			return []Line{
				{S("  Balance: ", SMuted), S(formatMoney(a.balance.BalanceUSDNanos), SOk.Bold(true)), S("  ·  rate "+formatMoney(a.balance.WorkspaceRateUSDNanosPerHour)+"/hr awake", SFaint)},
				{S("  Terms:   ", SMuted), S(terms, SSec)},
			}
		},
	}
	ov.items = append(ov.items,
		listItem{label: "Buy credits (opens checkout in browser)", run: func(a *app) tea.Cmd {
			ov.input = &inputPrompt{
				label: "Amount in USD (e.g. 25):",
				submit: func(a *app, val string) tea.Cmd {
					dollars, err := strconv.ParseFloat(strings.TrimPrefix(val, "$"), 64)
					if err != nil || dollars <= 0 {
						a.flash("Enter a positive dollar amount", true)
						return nil
					}
					cents := int64(dollars * 100)
					ver := ""
					if a.balance != nil {
						ver = a.balance.TermsVersion
					}
					return call(func(ctx context.Context) (*wire.Checkout, error) {
						return a.api.Checkout(ctx, "cli_"+randomID("", 8), cents, ver)
					}, func(ch *wire.Checkout, err error) tea.Msg {
						if err != nil {
							return doneMsg{err: err}
						}
						_ = OpenURL(ch.URL)
						return doneMsg{say: "Checkout opened: " + ch.URL}
					})
				},
			}
			return nil
		}},
		listItem{label: "View billing history", value: fmt.Sprintf("%d entries", len(a.history)), run: func(a *app) tea.Cmd {
			var lines []Line
			for _, e := range a.history {
				lines = append(lines, Line{
					S(PadRight(e.EffectiveAt.Local().Format("2 Jan 15:04"), 14), SFaint),
					S(PadRight(e.Kind, 20), SSec),
					S(formatMoney(e.AmountUSDNanos), SText),
				})
			}
			if len(lines) == 0 {
				lines = []Line{{S("No billing entries yet.", SFaint)}}
			}
			ov.viewer = &textViewer{title: "Billing history", lines: lines}
			return nil
		}},
	)
	if a.balance != nil && !a.balance.TermsAccepted {
		ov.items = append(ov.items, listItem{label: "Accept Terms of Service (" + a.balance.TermsVersion + ")", run: func(a *app) tea.Cmd {
			ver := a.balance.TermsVersion
			return call(func(ctx context.Context) (struct{}, error) {
				return struct{}{}, a.api.AcceptTerms(ctx, ver)
			}, func(_ struct{}, err error) tea.Msg {
				return doneMsg{say: "Terms accepted", err: err, reloadBilling: true}
			})
		}})
	}
	return ov
}

// ---------------------------------------------------------------- /account

func (a *app) openAccountOverlay() tea.Cmd {
	a.ov = a.buildAccountOverlay()
	return a.loadAccountInfo()
}

func (a *app) buildAccountOverlay() *listOverlay {
	ghState := "loading…"
	if a.github != nil {
		if a.github.SignedIn {
			ghState = "signed in as " + a.github.Login
		} else {
			ghState = "signed out"
		}
	}
	idState := "loading…"
	if a.identity != nil {
		author, _ := api.Identity(*a.identity)
		idState = author
	}
	enterMode := "Enter sends (Shift/Alt+Enter newline)"
	if a.prefs.Newline {
		enterMode = "Enter newline (Alt+Enter sends)"
	}

	ov := &listOverlay{
		title: "Account & Preferences",
		hint:  "Enter toggle/open · Esc close",
		headerFn: func(a *app, w int) []Line {
			email := a.api.Email()
			if email == "" {
				email = "(signed in)"
			}
			return []Line{{S("  Signed in as ", SMuted), S(email, SText.Bold(true)), S("  ·  barad "+a.opts.Version, SFaint)}}
		},
		items: []listItem{
			{label: "Theme", value: a.prefs.Theme + " (" + activeTheme + ")", run: func(a *app) tea.Cmd {
				i := slices.Index(Themes, a.prefs.Theme)
				next := Themes[(i+1)%len(Themes)]
				a.prefs, _ = prefs.Update(func(p *prefs.Prefs) { p.Theme = next })
				applyTheme(resolveTheme(a.prefs.Theme, a.terminalLight))
				a.ov = a.buildAccountOverlay()
				return nil
			}},
			{label: "Composer Enter key", value: enterMode, run: func(a *app) tea.Cmd {
				a.prefs, _ = prefs.Update(func(p *prefs.Prefs) { p.Newline = !p.Newline })
				a.ov = a.buildAccountOverlay()
				return nil
			}},
			{label: "Tool output verbosity (Ctrl+T)", value: a.prefs.Work.String(), run: func(a *app) tea.Cmd {
				a.cycleWork()
				a.ov = a.buildAccountOverlay()
				return nil
			}},
			{label: "GitHub", value: ghState, run: func(a *app) tea.Cmd {
				return call(func(ctx context.Context) (*wire.GitHubLink, error) {
					return a.api.GitHubSignIn(ctx)
				}, func(l *wire.GitHubLink, err error) tea.Msg {
					if err != nil {
						return doneMsg{err: err}
					}
					_ = OpenURL(l.URL)
					return doneMsg{say: "Opened GitHub sign-in: " + l.URL}
				})
			}},
			{label: "Git commit identity", value: idState, run: func(a *app) tea.Cmd {
				a.flash("Edit with: barad git identity --name … --email …", false)
				return nil
			}},
		},
	}
	return ov
}

func formatMoney(nanos int64) string {
	sign := ""
	if nanos < 0 {
		sign, nanos = "-", -nanos
	}
	return fmt.Sprintf("%s$%.2f", sign, float64(nanos)/1e9)
}

func formatTokens(n int64) string {
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

func sessionActivity(s wire.Session) time.Time {
	t := s.UpdatedAt
	if s.LatestTerminalTurnAt.After(t) {
		t = s.LatestTerminalTurnAt
	}
	if s.LatestUserPromptAt.After(t) {
		t = s.LatestUserPromptAt
	}
	if t.IsZero() {
		t = s.CreatedAt
	}
	return t
}

func init() {
	// Ensure filepath is kept imported for future local file download support.
	_ = filepath.Clean
	_ = os.ErrNotExist
}

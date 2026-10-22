package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/shell"
	"github.com/barad-io/barad/wire"
)

// The menus, by what they act on. Every menu ends with Barad's own group:
// its settings, and the way out.

func (a *app) homeMenu() *menu {
	items := []item{
		{group: "SESSIONS", label: "New session", key: "n", run: func(a *app) tea.Cmd { return a.newSession("") }},
		{group: "SESSIONS", label: "Search every project", key: "/", run: func(a *app) tea.Cmd { a.filter = &Editor{}; return nil }},
	}
	if unread := a.unreadHere(); len(unread) > 0 {
		items = append(items, item{group: "SESSIONS", label: fmt.Sprintf("Mark %d read", len(unread)), key: "R", run: func(a *app) tea.Cmd {
			a.markRead(unread)
			return nil
		}})
	}
	shelf := "Archived sessions"
	if a.shelf {
		shelf = "Back to sessions"
	}
	items = append(items,
		item{group: "SESSIONS", label: shelf, key: "tab", run: func(a *app) tea.Cmd { a.shelf, a.sel = !a.shelf, ""; return nil }},
		item{group: "SESSIONS", label: "Show a project", key: "p", value: a.projectName(a.projectID()), sub: func(a *app) *menu { return a.showProjectMenu() }},
		item{group: "SESSIONS", label: "New project", sub: func(a *app) *menu { return a.newProject() }},
	)
	// the selected session's own, by their keys
	if o := a.order(); len(o) > 0 && !a.shelf {
		s := o[a.pos(o)]
		group := "SELECTED  ·  " + sessionName(s)
		if s.LatestTerminalTurnID != "" {
			label := "Mark read"
			if !api.Unread(s, a.prefs.Reviewed[s.SessionID]) {
				label = "Mark unread"
			}
			items = append(items, item{group: group, label: label, key: "r", run: func(a *app) tea.Cmd { a.toggleRead(s); return nil }})
		}
		items = append(items, item{group: group, label: "Open", key: "enter", run: func(a *app) tea.Cmd { return a.open(s.SessionID) }})
		archive := a.archiveItem(s.SessionID, group)
		archive.key = "a"
		items = append(items, archive)
	}
	items = append(items, a.projectItems()...)
	return &menu{title: "Barad", items: append(items, a.baradItems()...), reload: func(a *app) *menu { return a.homeMenu() }}
}

// baradItems are the account's own: usage, settings, quit.
func (a *app) baradItems() []item {
	balance := ""
	if a.balance != nil {
		balance = money(a.balance.BalanceUSDNanos) + " left"
	}
	return []item{
		{group: "BARAD", label: "Usage", value: balance, run: func(a *app) tea.Cmd { a.screen = screenUsage; return a.loadUsage() }},
		{group: "BARAD", label: "Settings", sub: func(a *app) *menu { return a.settingsMenu() }},
		{group: "BARAD", label: "Quit", key: "q", run: func(a *app) tea.Cmd { return tea.Quit }},
	}
}

func (a *app) chatMenu() *menu {
	c := a.conv
	s := c.session
	var items []item
	// What needs you now.
	if s.CurrentTurnID != "" {
		items = append(items, item{label: "Stop the agent", key: "ctrl+x", run: func(a *app) tea.Cmd { return a.cancelTurn() }})
	}
	if view, ok := c.openProposal(); ok {
		items = append(items,
			item{label: "Approve: " + view.Proposal.Title, run: func(a *app) tea.Cmd {
				s := a.conv.session
				return call(func(ctx context.Context) (*wire.SessionProposalDecision, error) {
					return a.api.ApproveProposal(ctx, s, view)
				},
					func(d *wire.SessionProposalDecision, err error) tea.Msg {
						if err != nil {
							return doneMsg{say: "Could not approve", err: err}
						}
						return doneMsg{say: "Approved — the new session is starting", relist: true, open: d.DestinationID}
					})
			}},
			item{label: "Dismiss the proposal", run: func(a *app) tea.Cmd {
				id := a.conv.id
				return call(func(ctx context.Context) (struct{}, error) {
					return struct{}{}, a.api.DismissProposal(ctx, id, view.Sequence)
				},
					func(_ struct{}, err error) tea.Msg { return doneMsg{say: "Dismissed", err: err, resync: true} })
			}})
	}
	if s.MachineStatus == wire.MachineStatusDehydrated && !s.PendingTurn {
		items = append(items, item{label: "Wake its computer", run: func(a *app) tea.Cmd { return a.wake() }})
	}
	if s.ArchivedAt.IsZero() {
		items = append(items, item{label: "Open a shell in its computer", key: "ctrl+t", run: func(a *app) tea.Cmd { return a.shell() }})
	}
	if o := s.Origin; o != nil {
		if from, ok := a.session(o.SessionID); ok {
			items = append(items, item{label: "Go to the session it came from", value: sessionName(from), run: func(a *app) tea.Cmd { return a.open(from.SessionID) }})
		}
	}

	conversation := "THIS CONVERSATION"
	if reply := c.lastReply(); reply != "" {
		items = append(items, item{group: conversation, label: "Copy the last reply", run: func(a *app) tea.Cmd {
			a.say("Copied the last reply")
			return tea.SetClipboard(reply)
		}})
	}
	items = append(items,
		item{group: conversation, label: "Your messages", key: "ctrl+o", sub: func(a *app) *menu { return a.outlineMenu() }},
		item{group: conversation, label: "Images", value: fmt.Sprint(len(c.pictures())), run: func(a *app) tea.Cmd { return a.openImages() }},
		item{group: conversation, label: "Steps", value: a.prefs.Work.String(), key: "tab", run: func(a *app) tea.Cmd { a.cycleWork(); return nil }},
		item{group: conversation, label: "Attach an image", key: "ctrl+v", run: func(a *app) tea.Cmd {
			return func() tea.Msg { data, err := clipboardImage(); return clipboardMsg{data, err} }
		}},
		item{group: conversation, label: "Share a read-only link", run: func(a *app) tea.Cmd {
			id := a.conv.id
			return call(func(ctx context.Context) (*api.Share, error) { return a.api.Share(ctx, id, randomID("share_", 12)) },
				func(sh *api.Share, err error) tea.Msg { return sharedMsg{sh, err} })
		}},
	)
	items = append(items, a.sessionItems()...)
	items = append(items, item{group: "BARAD", label: "Back to sessions", key: "esc", run: func(a *app) tea.Cmd { a.screen, a.sel = screenHome, a.conv.id; return nil }})
	return &menu{title: sessionName(s), items: append(items, a.baradItems()...), reload: func(a *app) *menu { return a.chatMenu() }}
}

// sessionItems are the open session's settings, under its name.
func (a *app) sessionItems() []item {
	s := a.conv.session
	group := "SESSION  ·  " + sessionName(s)
	if !s.ArchivedAt.IsZero() {
		return []item{{group: group, label: "Details", sub: func(a *app) *menu { return a.detailsMenu() }}, a.deleteItem(group)}
	}
	items := []item{
		{group: group, label: "Rename", sub: func(a *app) *menu {
			e := &Editor{}
			e.Insert(sessionName(a.conv.session))
			return &menu{title: "Rename", edit: e, submit: func(a *app, name string) tea.Cmd {
				st := api.SettingsOf(a.conv.session)
				st.Name = name
				return a.updateSettings(st, "Renamed")
			}}
		}},
		{group: group, label: "Model", value: s.Model, sub: func(a *app) *menu { return a.modelMenu() }},
	}
	if efforts := a.efforts(s.ProviderResourceID, s.Model, s.SupportedReasoningEfforts); len(efforts) > 0 {
		items = append(items, item{group: group, label: "Reasoning effort", value: effortLabel(s.ReasoningEffort), sub: func(a *app) *menu { return a.effortMenu() }})
	}
	if usesFastMode(s) {
		value := "off"
		if s.FastMode {
			value = "on"
		}
		items = append(items, item{group: group, label: "Fast mode", value: value, run: func(a *app) tea.Cmd {
			st := api.SettingsOf(a.conv.session)
			st.FastMode = !st.FastMode
			say := "Fast mode off"
			if st.FastMode {
				say = "Fast mode on: faster replies, at more usage or API cost"
			}
			return a.updateSettings(st, say)
		}})
	}
	repos := "none"
	if r := a.sessionRepos[s.SessionID]; len(r) > 0 {
		repos = fmt.Sprint(len(r))
	}
	items = append(items,
		item{group: group, label: "Project", value: a.projectName(projectOf(s)), sub: func(a *app) *menu {
			return a.projectMenu(projectOf(a.conv.session), func(a *app, id string) tea.Cmd { return a.moveSession(id) })
		}},
		item{group: group, label: "Repositories", value: repos, sub: func(a *app) *menu { return a.repositoriesMenu() }},
		item{group: group, label: "Environment", sub: func(a *app) *menu {
			return a.ownerEnvMenu(api.EnvironmentOwner{Kind: "sessions", ID: a.conv.id},
				"Its agent's commands and terminal shells receive these, live, with the project's.")
		}},
		item{group: group, label: "Details", sub: func(a *app) *menu { return a.detailsMenu() }},
		a.archiveItem(s.SessionID, group),
		a.deleteItem(group),
	)
	return items
}

func (a *app) deleteItem(group string) item {
	return item{group: group, label: "Delete", danger: true, ask: "Delete permanently removes the session, its transcript and its workspace. You cannot undo this.",
		run: func(a *app) tea.Cmd {
			id := a.conv.id
			a.screen, a.conv = screenHome, nil
			return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.DeleteSession(ctx, id) },
				func(_ struct{}, err error) tea.Msg {
					return doneMsg{say: "Deleting the session", err: err, relist: true}
				})
		}}
}

// archiveItem archives a session, after asking: its workspace and
// published pages go for good; the conversation stays.
func (a *app) archiveItem(id, group string) item {
	s, _ := a.session(id)
	return item{group: group, label: "Archive", danger: true, action: "Archive " + sessionName(s),
		ask: "Archive erases this session's workspace and its published pages. The conversation stays. You cannot undo this.",
		run: func(a *app) tea.Cmd {
			if a.conv != nil && a.conv.id == id {
				a.screen, a.conv = screenHome, nil
			}
			return call(func(ctx context.Context) (*wire.Session, error) { return a.api.Archive(ctx, id) },
				func(_ *wire.Session, err error) tea.Msg { return doneMsg{say: "Archived", err: err, relist: true} })
		}}
}

// usesFastMode reports whether a session's provider has a fast mode, as the
// web app decides it.
func usesFastMode(s wire.Session) bool {
	switch s.Provider {
	case "xai", "grok", "claude":
		return false
	}
	return true
}

type shellMsg struct{ err error }

// shell steps the app aside for a shell in the open session's computer, and
// comes back when it exits.
func (a *app) shell() tea.Cmd {
	return tea.Exec(&shell.Command{Client: a.api, Session: a.conv.session}, func(err error) tea.Msg { return shellMsg{err} })
}

func (a *app) wake() tea.Cmd {
	id := a.conv.id
	return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.Wake(ctx, id) },
		func(_ struct{}, err error) tea.Msg {
			return doneMsg{say: "Waking", err: err, resync: true, relist: true}
		})
}

func (a *app) moveSession(projectID string) tea.Cmd {
	id := a.conv.id
	if projectID == projectOf(a.conv.session) {
		return nil
	}
	return call(func(ctx context.Context) (*wire.Session, error) { return a.api.MoveSession(ctx, id, projectID) },
		func(s *wire.Session, err error) tea.Msg {
			return doneMsg{say: "Moved to " + a.projectName(projectID), err: err, session: s, relist: true}
		})
}

// repositoriesMenu is the repositories your Git sources reach, those the
// session's Git credentials reach checked. Choosing one gives or takes away
// that access; the workspace's files are never touched.
func (a *app) repositoriesMenu() *menu {
	id := a.conv.id
	mn := &menu{title: "Repositories", reload: func(a *app) *menu { return a.repositoriesMenu() }, load: a.loadRepos(),
		hint: "Gives the session's Git credentials access. The workspace's files stay as they are."}
	attached := map[string]wire.SessionGitRepository{}
	for _, r := range a.sessionRepos[id] {
		attached[r.GitSourceResourceID+"\n"+strings.ToLower(r.CanonicalPath)] = r
	}
	if a.repos == nil {
		mn.hint = "Loading repositories…"
		return mn
	}
	for _, r := range a.repos {
		have, ok := attached[r.SourceID+"\n"+strings.ToLower(r.FullName)]
		if ok {
			mn.items = append(mn.items, item{label: r.FullName, check: true, danger: true, action: "Take away access",
				ask: "Take away " + r.FullName + "? The session's Git credentials stop reaching it; its clone in the workspace stays.",
				run: func(a *app) tea.Cmd {
					return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.RemoveRepository(ctx, id, have) },
						func(_ struct{}, err error) tea.Msg {
							return reposChangedMsg{id, "Took away " + have.CanonicalPath, err}
						})
				}})
			continue
		}
		mn.items = append(mn.items, item{label: r.FullName, value: r.DefaultBranch, run: func(a *app) tea.Cmd {
			return call(func(ctx context.Context) (struct{}, error) {
				return struct{}{}, a.api.AddRepository(ctx, id, r.SourceID, r.FullName)
			},
				func(_ struct{}, err error) tea.Msg { return reposChangedMsg{id, "Gave access to " + r.FullName, err} })
		}})
	}
	if len(a.repos) == 0 {
		mn.hint = "No repository is reachable. Connect a Git source on the web, under Settings › Source control."
	}
	return mn
}

// reposChangedMsg ends giving or taking away a repository.
type reposChangedMsg struct {
	id, say string
	err     error
}

// instructionsMsg brings a session's instruction files.
type instructionsMsg struct {
	id  string
	w   *wire.WorkspaceInstructions
	err error
}

// detailsMenu is what the web app's technical details show, and the
// instruction files the agent reads.
func (a *app) detailsMenu() *menu {
	s := a.conv.session
	id := s.SessionID
	mn := &menu{title: "Details", reload: func(a *app) *menu { return a.detailsMenu() },
		load: call(func(ctx context.Context) (*wire.WorkspaceInstructions, error) {
			return a.api.WorkspaceInstructions(ctx, id)
		},
			func(w *wire.WorkspaceInstructions, err error) tea.Msg { return instructionsMsg{id, w, err} })}
	add := func(group, label, value string, run func(a *app) tea.Cmd) {
		mn.items = append(mn.items, item{group: group, label: label, value: value, run: run})
	}
	add("SESSION", "Provider", a.providerOf(s.ProviderResourceID).Name, nil)
	add("SESSION", "Model", modelName(s), nil)
	add("SESSION", "Status", string(s.MachineStatus), nil)
	add("SESSION", "Session ID", s.SessionID, func(a *app) tea.Cmd { a.say("Copied the session ID"); return tea.SetClipboard(s.SessionID) })
	add("SESSION", "Created", clock(s.CreatedAt, a.now), nil)
	add("SESSION", "Last activity", clock(a.lastActive(), a.now), nil)
	ins := a.instructions[s.SessionID]
	switch {
	case ins == nil:
		add("AGENT INSTRUCTIONS", "Loading…", "", nil)
	case len(ins.Loaded) == 0 && ins.ScannedAt.IsZero():
		add("AGENT INSTRUCTIONS", "Read when the workspace is first set up", "", nil)
	case len(ins.Loaded) == 0:
		add("AGENT INSTRUCTIONS", "None found", "", nil)
	}
	if ins != nil {
		for _, f := range ins.Loaded {
			size := fmt.Sprintf("%.1f KB", float64(f.LoadedBytes)/1024)
			if f.LoadedBytes < f.SourceBytes {
				size += fmt.Sprintf(" of %.1f KB", float64(f.SourceBytes)/1024)
			}
			add("AGENT INSTRUCTIONS", f.Path, size, nil)
		}
		if n := len(ins.Discovered) - len(ins.Loaded); n > 0 {
			add("AGENT INSTRUCTIONS", fmt.Sprintf("%d more found, not read", n), "", nil)
		}
	}
	return mn
}

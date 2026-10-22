package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Projects, environments and the pickers they share with a new session:
// the agent, repositories at a branch, and variables.

// project is the project with id, or Default.
func (a *app) project(id string) wire.Project {
	for _, p := range a.projects {
		if p.ProjectID == id {
			return p
		}
	}
	return wire.Project{ProjectID: wire.DefaultProjectID, Name: wire.DefaultProjectName}
}

// projectItems are the shown project's settings, under its name.
func (a *app) projectItems() []item {
	p := a.project(a.projectID())
	group := "PROJECT  ·  " + p.Name
	agent := "chosen per session"
	if p.Agent != nil {
		agent = p.Agent.Model
	}
	repos := "none"
	if len(p.Repositories) > 0 {
		repos = fmt.Sprint(len(p.Repositories))
	}
	var items []item
	if p.ProjectID != wire.DefaultProjectID {
		items = append(items, item{group: group, label: "Rename", sub: func(a *app) *menu {
			e := &Editor{}
			e.Insert(p.Name)
			return &menu{title: "Rename", edit: e, submit: func(a *app, name string) tea.Cmd {
				return a.saveProject(p.ProjectID, func(s *api.ProjectSettings) { s.Name = name }, "Renamed")
			}}
		}})
	}
	items = append(items, item{group: group, label: "Agent", value: agent, sub: func(a *app) *menu {
		cur := a.project(p.ProjectID).Agent
		provider, model := "", ""
		if cur != nil {
			provider, model = cur.ProviderResourceID, cur.Model
		}
		mn := a.agentMenu(provider, model, func(a *app, provider, model string) tea.Cmd {
			return a.saveProject(p.ProjectID, func(s *api.ProjectSettings) {
				s.Agent = &wire.ProjectAgent{ProviderResourceID: provider, Model: model, ReasoningEffort: a.effortFor(provider, model, effortOf(s.Agent))}
			}, "New sessions start with "+model)
		})
		mn.items = append(mn.items, item{label: "None: each session chooses", check: cur == nil, run: func(a *app) tea.Cmd {
			return a.saveProject(p.ProjectID, func(s *api.ProjectSettings) { s.Agent = nil }, "Each new session chooses its agent")
		}})
		return mn
	}})
	if ag := p.Agent; ag != nil {
		if efforts := a.efforts(ag.ProviderResourceID, ag.Model, nil); len(efforts) > 0 {
			items = append(items, item{group: group, label: "Reasoning effort", value: effortLabel(ag.ReasoningEffort), sub: func(a *app) *menu {
				var its []item
				for _, e := range efforts {
					its = append(its, item{label: effortLabel(e), check: e == ag.ReasoningEffort, run: func(a *app) tea.Cmd {
						return a.saveProject(p.ProjectID, func(s *api.ProjectSettings) {
							if s.Agent != nil {
								s.Agent.ReasoningEffort = e
							}
						}, "Reasoning effort: "+effortLabel(e))
					}})
				}
				return &menu{title: "Reasoning effort", items: its, load: a.loadCatalog(ag.ProviderResourceID)}
			}})
		}
	}
	items = append(items,
		item{group: group, label: "Repositories", value: repos, sub: func(a *app) *menu { return a.projectReposMenu(p.ProjectID) }},
		item{group: group, label: "Environment", sub: func(a *app) *menu {
			return a.ownerEnvMenu(api.EnvironmentOwner{Kind: "projects", ID: p.ProjectID},
				"Every session in "+p.Name+" receives these, live. A session's own variable of the same name wins.")
		}},
	)
	if p.ProjectID != wire.DefaultProjectID {
		items = append(items, item{group: group, label: "Delete", danger: true, action: "Delete " + p.Name,
			ask: "Its sessions move to Default. No session, transcript or workspace is deleted.",
			run: func(a *app) tea.Cmd {
				id := p.ProjectID
				a.prefs.Project = ""
				a.savePrefs()
				return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.DeleteProject(ctx, id) },
					func(_ struct{}, err error) tea.Msg {
						return doneMsg{say: "Deleted " + p.Name, err: err, relist: true, account: true}
					})
			}})
	}
	return items
}

// newProject asks for a name and makes the project, showing it.
func (a *app) newProject() *menu {
	return &menu{title: "New project", edit: &Editor{}, about: "its name", submit: func(a *app, name string) tea.Cmd {
		return call(func(ctx context.Context) (*wire.Project, error) {
			return a.api.CreateProject(ctx, api.ProjectSettings{Name: name, Repositories: []wire.ProjectRepository{}})
		}, func(p *wire.Project, err error) tea.Msg {
			if err == nil {
				return projectMadeMsg{p}
			}
			return doneMsg{say: "Could not make the project", err: err}
		})
	}}
}

type projectMadeMsg struct{ p *wire.Project }

func effortOf(ag *wire.ProjectAgent) wire.ReasoningEffort {
	if ag == nil {
		return ""
	}
	return ag.ReasoningEffort
}

// saveProject changes a project's settings and saves them all.
func (a *app) saveProject(id string, change func(s *api.ProjectSettings), say string) tea.Cmd {
	in := api.SettingsOfProject(a.project(id))
	change(&in)
	return call(func(ctx context.Context) (*wire.Project, error) { return a.api.UpdateProject(ctx, id, in) },
		func(_ *wire.Project, err error) tea.Msg { return doneMsg{say: say, err: err, account: true} })
}

// projectReposMenu is the repositories new sessions in a project clone,
// checked, among those your Git sources reach; choosing one adds or drops it.
func (a *app) projectReposMenu(id string) *menu {
	mn := &menu{title: "Repositories", reload: func(a *app) *menu { return a.projectReposMenu(id) },
		hint: "New sessions in this project clone these. The branch is chosen per session.", load: a.loadRepos()}
	if a.repos == nil {
		mn.hint = "Loading repositories…"
		return mn
	}
	have := a.project(id).Repositories
	for _, r := range a.repos {
		in := slices.ContainsFunc(have, func(h wire.ProjectRepository) bool { return strings.EqualFold(h.Repository, r.FullName) })
		mn.items = append(mn.items, item{label: r.FullName, check: in, stay: true, run: func(a *app) tea.Cmd {
			return a.saveProject(id, func(s *api.ProjectSettings) {
				if in {
					s.Repositories = slices.DeleteFunc(s.Repositories, func(h wire.ProjectRepository) bool { return strings.EqualFold(h.Repository, r.FullName) })
				} else {
					s.Repositories = append(s.Repositories, wire.ProjectRepository{SourceID: r.SourceID, Repository: r.FullName})
				}
			}, "Saved the repositories")
		}})
	}
	return mn
}

// loadRepos fetches the repositories your Git sources reach, once.
func (a *app) loadRepos() tea.Cmd {
	if a.repos != nil {
		return nil
	}
	return call(a.api.Repositories, func(r []api.Repository, err error) tea.Msg { return reposMsg{r, err} })
}

// agentMenu chooses a connection's model: the connections, each opening its
// models (or the models directly when there is one). pick takes the choice.
func (a *app) agentMenu(curProvider, curModel string, pick func(a *app, provider, model string) tea.Cmd) *menu {
	var ready []wire.ProviderView
	var loads []tea.Cmd
	for _, p := range a.providers {
		if p.Ready {
			ready = append(ready, p)
			loads = append(loads, a.loadCatalog(p.ResourceID))
		}
	}
	modelsOf := func(p wire.ProviderView) func(a *app) *menu {
		var build func(a *app) *menu
		build = func(a *app) *menu {
			mn := &menu{title: p.Name, reload: build, load: a.loadCatalog(p.ResourceID)}
			cat := a.catalogs[p.ResourceID]
			if cat == nil {
				mn.hint = "Loading models…"
				return mn
			}
			for _, m := range cat.Models {
				name := m.Model
				mn.items = append(mn.items, item{label: name, check: curProvider == p.ResourceID && curModel == name,
					run: func(a *app) tea.Cmd { return pick(a, p.ResourceID, name) }})
			}
			return mn
		}
		return build
	}
	if len(ready) == 1 {
		mn := modelsOf(ready[0])(a)
		mn.title = "Agent"
		return mn
	}
	var items []item
	for _, p := range ready {
		items = append(items, item{label: p.Name, value: providerName(string(p.ProviderType)), check: p.ResourceID == curProvider, sub: modelsOf(p)})
	}
	mn := &menu{title: "Agent", items: items, load: tea.Batch(loads...)}
	if len(items) == 0 {
		mn.hint = "No provider is connected: add one in Settings."
	}
	return mn
}

// startReposMenu is the repositories a new session clones: those your Git
// sources reach, the chosen ones checked with their branch. Choosing one
// adds it, or opens it to pick its branch or leave it out.
func (a *app) startReposMenu() *menu {
	mn := &menu{title: "Repositories", reload: func(a *app) *menu { return a.startReposMenu() }, load: a.loadRepos(),
		hint: "Cloned once, when the workspace is made."}
	if a.repos == nil {
		mn.hint = "Loading repositories…"
		return mn
	}
	for _, r := range a.repos {
		i := slices.IndexFunc(a.start.repos, func(c api.RepositoryRequest) bool { return strings.EqualFold(c.Repository, r.FullName) })
		if i < 0 {
			mn.items = append(mn.items, item{label: r.FullName, stay: true, run: func(a *app) tea.Cmd {
				a.start.repos = append(a.start.repos, api.RepositoryRequest{SourceID: r.SourceID, Repository: r.FullName})
				return nil
			}})
			continue
		}
		branch := a.start.repos[i].RequestedRef
		if branch == "" {
			branch = r.DefaultBranch + " (default)"
		}
		mn.items = append(mn.items, item{label: r.FullName, value: branch, check: true, sub: func(a *app) *menu { return a.branchMenu(r) }})
	}
	return mn
}

// branchMenu picks the branch a new session clones a repository at, or
// leaves the repository out.
func (a *app) branchMenu(r api.Repository) *menu {
	key := r.SourceID + "\n" + r.FullName
	mn := &menu{title: r.FullName, reload: func(a *app) *menu { return a.branchMenu(r) }}
	cur := ""
	if i := slices.IndexFunc(a.start.repos, func(c api.RepositoryRequest) bool { return strings.EqualFold(c.Repository, r.FullName) }); i >= 0 {
		cur = a.start.repos[i].RequestedRef
	}
	set := func(ref string) func(a *app) tea.Cmd {
		return func(a *app) tea.Cmd {
			for i := range a.start.repos {
				if strings.EqualFold(a.start.repos[i].Repository, r.FullName) {
					a.start.repos[i].RequestedRef = ref
				}
			}
			a.menu = a.menu.parent // back to the repositories
			a.reloadMenu()
			return nil
		}
	}
	mn.items = append(mn.items, item{group: "BRANCH", label: r.DefaultBranch, value: "default", check: cur == "", stay: true, run: set("")})
	switch b := a.branches[key]; {
	case b == nil:
		mn.hint = "Loading branches…"
		mn.load = call(func(ctx context.Context) (*wire.GitBranches, error) {
			return a.api.Branches(ctx, r.SourceID, r.FullName)
		},
			func(b *wire.GitBranches, err error) tea.Msg { return branchesMsg{key, b, err} })
	default:
		for _, name := range b.Branches {
			if name != r.DefaultBranch {
				mn.items = append(mn.items, item{group: "BRANCH", label: name, check: cur == name, stay: true, run: set(name)})
			}
		}
	}
	mn.items = append(mn.items, item{group: "REPOSITORY", label: "Leave it out", stay: true, run: func(a *app) tea.Cmd {
		a.start.repos = slices.DeleteFunc(a.start.repos, func(c api.RepositoryRequest) bool { return strings.EqualFold(c.Repository, r.FullName) })
		a.menu = a.menu.parent
		a.reloadMenu()
		return nil
	}})
	return mn
}

type branchesMsg struct {
	key string
	b   *wire.GitBranches
	err error
}

// ownerEnvMenu is a session's or a project's variables, kept on the server.
func (a *app) ownerEnvMenu(o api.EnvironmentOwner, about string) *menu {
	key := o.Kind + "/" + o.ID
	change := func(a *app, m wire.EnvironmentMutation, say string) tea.Cmd {
		if env := a.envs[key]; env != nil {
			m.ExpectedRevision = env.Revision
		}
		return call(func(ctx context.Context) (*wire.EnvironmentView, error) { return a.api.ChangeEnvironment(ctx, o, m) },
			func(v *wire.EnvironmentView, err error) tea.Msg { return envMsg{key, v, say, err} })
	}
	mn := a.envMenu("Environment", about,
		func(a *app) []wire.EnvironmentVariable {
			if env := a.envs[key]; env != nil {
				return env.Variables
			}
			return nil
		},
		func(a *app, v wire.EnvironmentVariable) tea.Cmd {
			return change(a, wire.EnvironmentMutation{SetVariables: []wire.EnvironmentVariable{v}}, "Set "+v.Name)
		},
		func(a *app, name string) tea.Cmd {
			return change(a, wire.EnvironmentMutation{RemoveVariables: []string{name}}, "Removed "+name)
		})
	mn.reload = func(a *app) *menu { return a.ownerEnvMenu(o, about) }
	if a.envs[key] == nil {
		mn.hint = "Loading…"
	}
	mn.load = call(func(ctx context.Context) (*wire.EnvironmentView, error) { return a.api.Environment(ctx, o) },
		func(v *wire.EnvironmentView, err error) tea.Msg { return envMsg{key, v, "", err} })
	return mn
}

type envMsg struct {
	key string
	v   *wire.EnvironmentView
	say string
	err error
}

// envMenu lists variables, each to change or remove, and ways to add one.
// A secret's value never shows; it can only be replaced.
func (a *app) envMenu(title, about string, list func(a *app) []wire.EnvironmentVariable,
	set func(a *app, v wire.EnvironmentVariable) tea.Cmd, remove func(a *app, name string) tea.Cmd) *menu {
	var build func(a *app) *menu
	build = func(a *app) *menu {
		mn := &menu{title: title, hint: about, reload: build}
		for _, v := range list(a) {
			value := v.Value
			if v.Secret {
				value = "secret"
			}
			mn.items = append(mn.items, item{group: "VARIABLES", label: v.Name, value: value, sub: func(a *app) *menu {
				return &menu{title: v.Name, items: []item{
					{label: "Change the value", sub: func(a *app) *menu { return valueField(v.Name, v.Secret, set) }},
					{label: "Remove", danger: true, action: "Remove " + v.Name, ask: "Commands stop receiving " + v.Name + ".",
						run: func(a *app) tea.Cmd { return remove(a, v.Name) }},
				}}
			}})
		}
		mn.items = append(mn.items,
			item{group: "ADD", label: "A variable", sub: func(a *app) *menu {
				return &menu{title: "A variable", edit: &Editor{}, about: "NAME=value", submit: func(a *app, text string) tea.Cmd {
					name, value, ok := strings.Cut(text, "=")
					if !ok {
						a.toast, a.toastBad, a.toastAt = "Write it as NAME=value", true, a.now
						return nil
					}
					return set(a, wire.EnvironmentVariable{Name: strings.TrimSpace(name), Value: value})
				}}
			}},
			item{group: "ADD", label: "A secret", sub: func(a *app) *menu {
				return &menu{title: "A secret", edit: &Editor{}, about: "its NAME", submit: func(a *app, name string) tea.Cmd {
					a.menu = valueField(strings.TrimSpace(name), true, set)
					return nil
				}}
			}},
		)
		return mn
	}
	return build(a)
}

// valueField asks for a variable's value, in dots for a secret.
func valueField(name string, secret bool, set func(a *app, v wire.EnvironmentVariable) tea.Cmd) *menu {
	about := "its value"
	if secret {
		about = "its value: it is never shown again"
	}
	return &menu{title: name, edit: &Editor{}, about: about, mask: secret, submit: func(a *app, value string) tea.Cmd {
		return set(a, wire.EnvironmentVariable{Name: name, Value: value, Secret: secret})
	}}
}

package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/internal/update"
	"github.com/barad-io/barad/wire"
)

// Settings is the web app's Settings, as menus: this computer, provider
// connections, source control, billing, and the account. Each part loads
// what it shows when it opens; a change saves at once.

// pollEvery is how often the app asks how something in the browser stands:
// a sign-in, a payment. Tests shorten it.
var pollEvery = 3 * time.Second

// loadedMsg ends a settings call: apply puts what it brought in place, and
// returns what to do next.
type loadedMsg struct {
	apply func(a *app) tea.Cmd
	what  string // what failed, when err is set
	err   error
}

// fetch loads a value for a menu and keeps it with put.
func fetch[T any](what string, get func(ctx context.Context) (T, error), put func(a *app, v T)) tea.Cmd {
	return call(get, func(v T, err error) tea.Msg {
		return loadedMsg{apply: func(a *app) tea.Cmd { put(a, v); return nil }, what: "Could not load " + what, err: err}
	})
}

func (a *app) settingsMenu() *menu {
	mn := &menu{title: "Settings", reload: func(a *app) *menu { return a.settingsMenu() },
		load: tea.Batch(a.loadAccount(), a.loadGitHub(), a.loadBilling())}
	add := func(group string, it item) {
		it.group = group
		mn.items = append(mn.items, it)
	}

	here := "THIS COMPUTER"
	add(here, item{label: "Theme", value: a.prefs.Theme, stay: true, run: func(a *app) tea.Cmd { a.toggleTheme(); return nil }})
	add(here, item{label: "Enter", value: onOff(a.prefs.Newline, "new line", "sends"), stay: true,
		run: func(a *app) tea.Cmd { a.setPrefs(func(p *prefs.Prefs) { p.Newline = !p.Newline }); return nil }})
	add(here, item{label: "Notifications", value: onOff(a.prefs.Silent, "off", "on"), stay: true,
		run: func(a *app) tea.Cmd { a.setPrefs(func(p *prefs.Prefs) { p.Silent = !p.Silent }); return nil }})
	add(here, item{label: "Steps", value: a.prefs.Work.String(), stay: true, run: func(a *app) tea.Cmd { a.cycleWork(); return nil }})

	for _, p := range a.providers {
		state := api.ProviderTypeName(p.ProviderType)
		if !p.Ready {
			state = "signed out"
		}
		add("PROVIDERS", item{label: p.Name, value: state, sub: func(a *app) *menu { return a.providerMenu(p.ResourceID) }})
	}
	add("PROVIDERS", item{label: "Connect a provider", sub: func(a *app) *menu { return a.addProviderMenu() }})

	switch gh := a.github; {
	case gh == nil:
		add("SOURCE CONTROL", item{label: "GitHub", value: "loading"})
	case gh.SignedIn:
		add("SOURCE CONTROL", item{label: "GitHub", value: "@" + gh.Login, sub: func(a *app) *menu { return a.githubMenu() }})
	default:
		add("SOURCE CONTROL", item{label: "Sign in to GitHub", value: "signed out", run: func(a *app) tea.Cmd { return a.githubSignIn() }})
	}
	for _, s := range a.sources {
		add("SOURCE CONTROL", item{label: s.Name, value: "Git source", sub: func(a *app) *menu { return a.sourceMenu(s.ResourceID) }})
	}
	identity := "loading"
	if a.identity != nil {
		author, _ := api.Identity(*a.identity)
		identity = "by " + author
	}
	add("SOURCE CONTROL", item{label: "Git identity", value: identity, sub: func(a *app) *menu { return a.identityMenu() }})

	credits := "loading"
	if a.balance != nil {
		credits = money(a.balance.BalanceUSDNanos) + " left"
	}
	add("BILLING", item{label: "Buy credits", value: credits, sub: func(a *app) *menu { return a.buyMenu() }})
	add("BILLING", item{label: "Balance history", sub: func(a *app) *menu { return a.historyMenu() }})
	add("BILLING", item{label: "Usage", run: func(a *app) tea.Cmd { a.screen = screenUsage; return a.loadUsage() }})

	release := a.version
	switch {
	case update.Newer(a.prefs.Latest, a.version):
		release += "  ·  " + a.prefs.Latest + " is out: barad update"
	case update.Released(a.version):
		release += "  ·  the newest"
	}
	add("ACCOUNT", item{label: "Signed in as", value: a.api.Email()})
	add("ACCOUNT", item{label: "barad", value: release})
	add("ACCOUNT", item{label: "Change password", sub: func(a *app) *menu { return a.passwordMenu() }})
	add("ACCOUNT", item{label: "Sign out", danger: true, ask: "Sign out of Barad on this computer? Your sessions keep running.", run: func(a *app) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return signOutMsg{a.api.SignOut(ctx)}
		}
	}})
	return mn
}

func onOff(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// ---------------------------------------------------------------- providers

func (a *app) provider(id string) (wire.ProviderView, bool) {
	for _, p := range a.providers {
		if p.ResourceID == id {
			return p, true
		}
	}
	return wire.ProviderView{}, false
}

// providerMenu is one connection: its name, its sign-in or key, and the
// sessions that use it.
func (a *app) providerMenu(id string) *menu {
	p, ok := a.provider(id)
	if !ok {
		return &menu{title: "Provider", hint: "It is gone."}
	}
	mn := &menu{title: p.Name, reload: func(a *app) *menu { return a.providerMenu(id) },
		load: fetch("the sessions using it", func(ctx context.Context) ([]wire.ResourceDependency, error) {
			return a.api.ConnectionUsers(ctx, "providers", id)
		}, func(a *app, u []wire.ResourceDependency) { a.users[id] = u })}
	mn.hint = api.ProviderTypeName(p.ProviderType) + onOff(p.Ready, "  ·  ready", "  ·  signed out: sessions using it wait")
	mn.items = append(mn.items, item{label: "Rename", sub: func(a *app) *menu {
		return a.renameMenu("providers", id, p.Name, func(a *app) tea.Cmd { return a.loadAccount() })
	}})
	switch {
	case api.UsesAPIKey(p.ProviderType):
		mn.items = append(mn.items, item{label: onOff(p.Ready, "Replace the API key", "Add an API key"), sub: func(a *app) *menu {
			return &menu{title: p.Name + " API key", edit: &Editor{}, mask: true, about: "the key: it is never shown again", submit: func(a *app, key string) tea.Cmd {
				return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.PutAPIKey(ctx, id, key) },
					func(_ struct{}, err error) tea.Msg {
						return outcome("Saved "+p.Name+"'s key", "Could not save the key", err, true)
					})
			}}
		}})
	default:
		mn.items = append(mn.items, item{label: onOff(p.Ready, "Sign in again", "Sign in"), run: func(a *app) tea.Cmd {
			return a.startSignIn(p.ProviderType, p.Name, id)
		}})
	}
	users, loaded := a.users[id]
	switch {
	case !loaded:
		mn.items = append(mn.items, item{label: "Used by", value: "loading"})
	case len(users) == 0:
		mn.items = append(mn.items, item{label: "Used by", value: "no session"})
	default:
		mn.items = append(mn.items, item{label: "Used by", value: plural(len(users), "session"), sub: func(a *app) *menu { return a.usersMenu(p.Name, users) }})
	}
	if p.Ready {
		mn.items = append(mn.items, item{label: "Sign out", danger: true, action: "Sign " + p.Name + " out",
			ask: "Its sign-in or key is removed. Sessions using it wait until it signs in again.", run: func(a *app) tea.Cmd {
				return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.RemoveCredential(ctx, id) },
					func(_ struct{}, err error) tea.Msg {
						return outcome("Signed "+p.Name+" out", "Could not sign "+p.Name+" out", err, true)
					})
			}})
	}
	mn.items = append(mn.items, item{label: "Remove", danger: true, action: "Remove " + p.Name,
		ask: "The connection goes for good. Sessions using it stop working.", run: func(a *app) tea.Cmd {
			return call(func(ctx context.Context) (struct{}, error) {
				return struct{}{}, a.api.RemoveConnection(ctx, "providers", id)
			},
				func(_ struct{}, err error) tea.Msg {
					return outcome("Removed "+p.Name, "Could not remove "+p.Name, err, true)
				})
		}})
	return mn
}

// usersMenu lists the sessions that use a connection; choosing one opens it.
func (a *app) usersMenu(name string, users []wire.ResourceDependency) *menu {
	mn := &menu{title: "Using " + name}
	for _, u := range users {
		s, ok := a.session(u.SessionID)
		if !ok {
			continue
		}
		mn.items = append(mn.items, item{label: sessionName(s), value: a.projectName(projectOf(s)), run: func(a *app) tea.Cmd { return a.open(s.SessionID) }})
	}
	return mn
}

// renameMenu renames a provider connection or a Git source.
func (a *app) renameMenu(route, id, name string, after func(a *app) tea.Cmd) *menu {
	e := &Editor{}
	e.Insert(name)
	return &menu{title: "Rename", edit: e, submit: func(a *app, next string) tea.Cmd {
		return call(func(ctx context.Context) (struct{}, error) {
			return struct{}{}, a.api.RenameConnection(ctx, route, id, next)
		},
			func(_ struct{}, err error) tea.Msg {
				if err != nil {
					return doneMsg{say: "Could not rename", err: err}
				}
				return loadedMsg{apply: func(a *app) tea.Cmd { a.say("Renamed to " + next); return after(a) }}
			})
	}}
}

// addProviderMenu picks a kind of connection, then its name.
func (a *app) addProviderMenu() *menu {
	mn := &menu{title: "Connect a provider", hint: "A subscription signs in in your browser; an API connection takes a key."}
	for _, t := range api.ProviderTypes {
		mn.items = append(mn.items, item{label: t.Name, sub: func(a *app) *menu {
			e := &Editor{}
			e.Insert(t.Name)
			return &menu{title: "Name the connection", edit: e, submit: func(a *app, name string) tea.Cmd {
				if !api.UsesAPIKey(t.Type) {
					return a.startSignIn(t.Type, name, "")
				}
				a.showMenu(&menu{title: name + " API key", edit: &Editor{}, mask: true, about: "the key: it is never shown again", submit: func(a *app, key string) tea.Cmd {
					return call(func(ctx context.Context) (*wire.ProviderView, error) {
						return a.api.AddAPIKeyProvider(ctx, t.Type, name, key)
					},
						func(_ *wire.ProviderView, err error) tea.Msg {
							return outcome("Connected "+name, "Could not connect "+name, err, true)
						})
				}})
				return nil
			}}
		}})
	}
	return mn
}

// signIn is a subscription sign-in in progress: a code to enter on the
// provider's page (ChatGPT, Grok), or Claude's page, which gives a code to
// paste back.
type signIn struct {
	providerType, name string
	flow               *wire.ProviderAuthorization
	page               string // Claude's sign-in page, once it is ready
	sent               bool   // Claude's code is in
	done               bool
}

type signInMsg struct {
	flow *wire.ProviderAuthorization
	poll *wire.ProviderAuthorizationPoll
	err  error
}

func (a *app) startSignIn(providerType, name, resourceID string) tea.Cmd {
	a.signIn = &signIn{providerType: providerType, name: name}
	a.showMenu(a.signInMenu())
	return call(func(ctx context.Context) (*wire.ProviderAuthorization, error) {
		return a.api.AuthorizeProvider(ctx, providerType, name, resourceID)
	}, func(f *wire.ProviderAuthorization, err error) tea.Msg { return signInMsg{flow: f, err: err} })
}

// pollSignIn asks how the sign-in stands, after a pause.
func (a *app) pollSignIn() tea.Cmd {
	s := a.signIn
	return tea.Tick(pollEvery, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		poll, err := a.api.PollProvider(ctx, s.flow, s.providerType)
		return signInMsg{flow: s.flow, poll: poll, err: err}
	})
}

// signInStep moves a sign-in on with what the API said.
func (a *app) signInStep(msg signInMsg) tea.Cmd {
	s := a.signIn
	if s == nil || (s.flow != nil && msg.flow != s.flow) {
		return nil // an abandoned sign-in
	}
	if msg.err != nil {
		a.signIn = nil
		return a.fail("Could not sign in", msg.err)
	}
	if s.flow == nil { // started
		s.flow = msg.flow
		if page := firstOf(s.flow.VerificationURLComplete, s.flow.VerificationURL); page != "" && s.providerType != "claude" {
			_ = OpenURL(page)
		}
		a.reloadMenu()
		return a.pollSignIn()
	}
	switch p := msg.poll; {
	case p.Connected:
		s.done = true
		a.say("Connected " + s.name)
		a.reloadMenu()
		return a.loadAccount()
	case p.Failed:
		a.signIn = nil
		return a.fail("Could not sign in", fmt.Errorf("the sign-in failed or expired; start again"))
	case s.providerType == "claude" && p.VerificationURL != "" && s.page == "":
		s.page = p.VerificationURL
		_ = OpenURL(s.page)
		a.showMenu(a.signInMenu())
		return nil // until you paste the code
	}
	return a.pollSignIn()
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// signInMenu shows a sign-in: the code to enter, or the field for Claude's.
func (a *app) signInMenu() *menu {
	s := a.signIn
	mn := &menu{title: "Sign in: " + s.name, reload: func(a *app) *menu { return a.signInMenu() }}
	switch {
	case s.done:
		mn.hint = "Connected. Esc to go back."
	case s.flow == nil:
		mn.hint = "Starting the sign-in…"
	case s.providerType == "claude" && s.page == "":
		mn.hint = "Starting Claude's sign-in…"
	case s.providerType == "claude" && !s.sent:
		mn.title, mn.edit, mn.about = "Paste the code Claude shows", &Editor{}, "the code from Claude's page (your browser opened it)"
		mn.submit = func(a *app, code string) tea.Cmd {
			s.sent = true
			a.showMenu(a.signInMenu())
			flow := s.flow
			return call(func(ctx context.Context) (struct{}, error) {
				return struct{}{}, a.api.SubmitProviderCode(ctx, flow, code)
			},
				func(_ struct{}, err error) tea.Msg {
					return signInMsg{flow: flow, poll: &wire.ProviderAuthorizationPoll{Pending: true}, err: err} // then ask again
				})
		}
	case s.providerType == "claude":
		mn.hint = "Finishing the sign-in…"
	default:
		page := firstOf(s.flow.VerificationURLComplete, s.flow.VerificationURL)
		mn.hint = "Enter " + s.flow.UserCode + " at " + page + " — your browser opened it. This finishes on its own."
		mn.items = []item{
			{label: "Copy the code", value: s.flow.UserCode, stay: true, run: func(a *app) tea.Cmd { a.say("Copied the code"); return tea.SetClipboard(s.flow.UserCode) }},
			{label: "Open the page again", stay: true, run: func(a *app) tea.Cmd { _ = OpenURL(page); return nil }},
		}
	}
	if s.providerType == "claude" && s.page != "" && !s.done {
		mn.items = append(mn.items, item{label: "Open Claude's page again", stay: true, run: func(a *app) tea.Cmd { _ = OpenURL(s.page); return nil }})
	}
	return mn
}

// ---------------------------------------------------------------- source control

func (a *app) loadGitHub() tea.Cmd {
	return tea.Batch(
		fetch("GitHub", a.api.GitHub, func(a *app, v *wire.GitHubAccount) { a.github = v }),
		fetch("your Git sources", a.api.GitSources, func(a *app, v []wire.AccountResource) { a.sources = v }),
		fetch("the Git identity", a.api.GitSettings, func(a *app, v *wire.GitSettings) { a.identity = v }),
	)
}

// awaitGitHub waits for GitHub, in the browser, to do what was asked: done
// says when, and what to say then.
type awaitGitHub struct {
	done  func(gh *wire.GitHubAccount) (string, bool)
	until time.Time
}

type githubMsg struct {
	gh  *wire.GitHubAccount
	err error
}

func (a *app) watchGitHub(done func(gh *wire.GitHubAccount) (string, bool)) tea.Cmd {
	a.awaiting = &awaitGitHub{done: done, until: a.now.Add(10 * time.Minute)}
	return a.pollGitHub()
}

func (a *app) pollGitHub() tea.Cmd {
	return tea.Tick(pollEvery, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		gh, err := a.api.GitHub(ctx)
		return githubMsg{gh, err}
	})
}

func (a *app) githubStep(msg githubMsg) tea.Cmd {
	w := a.awaiting
	if w == nil {
		return nil
	}
	if msg.err != nil {
		a.awaiting = nil
		return a.fail("Could not reach GitHub", msg.err)
	}
	a.github = msg.gh
	if say, ok := w.done(msg.gh); ok {
		a.awaiting = nil
		a.say(say)
		return a.loadGitHub()
	}
	if a.now.After(w.until) {
		a.awaiting = nil
		return nil
	}
	return a.pollGitHub()
}

func (a *app) githubSignIn() tea.Cmd {
	return call(a.api.GitHubSignIn, func(l *wire.GitHubLink, err error) tea.Msg {
		if err != nil {
			return doneMsg{say: "Could not sign in to GitHub", err: err}
		}
		return loadedMsg{apply: func(a *app) tea.Cmd {
			_ = OpenURL(l.URL)
			a.say("Sign in to GitHub in your browser")
			return a.watchGitHub(func(gh *wire.GitHubAccount) (string, bool) { return "Signed in to GitHub as " + gh.Login, gh.SignedIn })
		}}
	})
}

// githubMenu is the GitHub sign-in: the accounts and organizations Barad
// is installed on that you can connect, installing it on another, and
// signing out.
func (a *app) githubMenu() *menu {
	gh := a.github
	mn := &menu{title: "GitHub", hint: "Signed in as @" + gh.Login, reload: func(a *app) *menu { return a.githubMenu() }}
	for _, in := range gh.Installations {
		if in.SourceID != "" {
			mn.items = append(mn.items, item{label: in.AccountLogin, value: "connected", check: true})
			continue
		}
		mn.items = append(mn.items, item{label: "Connect " + in.AccountLogin, value: strings.ToLower(in.AccountType), run: func(a *app) tea.Cmd {
			return call(func(ctx context.Context) (*wire.AccountResource, error) {
				return a.api.ConnectInstallation(ctx, in.InstallationID)
			},
				func(s *wire.AccountResource, err error) tea.Msg {
					if err != nil {
						return doneMsg{say: "Could not connect " + in.AccountLogin, err: err}
					}
					return loadedMsg{apply: func(a *app) tea.Cmd { a.say("Connected " + s.Name); return a.loadGitHub() }}
				})
		}})
	}
	mn.items = append(mn.items,
		item{label: "Install on another account or organization", run: func(a *app) tea.Cmd {
			before := map[int64]bool{}
			for _, in := range gh.Installations {
				before[in.InstallationID] = true
			}
			return call(a.api.GitHubInstall, func(l *wire.GitHubLink, err error) tea.Msg {
				if err != nil {
					return doneMsg{say: "Could not reach GitHub", err: err}
				}
				return loadedMsg{apply: func(a *app) tea.Cmd {
					_ = OpenURL(l.URL)
					a.say("Install Barad in your browser: only an organization's owner can")
					return a.watchGitHub(func(gh *wire.GitHubAccount) (string, bool) {
						for _, in := range gh.Installations {
							if !before[in.InstallationID] && in.SourceID != "" {
								return "Connected " + in.AccountLogin, true
							}
						}
						return "", false
					})
				}}
			})
		}},
		item{label: "Sign out of GitHub", danger: true, ask: "Sources stay connected, but sessions cannot reach their repositories until you sign in again.",
			run: func(a *app) tea.Cmd {
				return call(func(ctx context.Context) (struct{}, error) { return struct{}{}, a.api.GitHubSignOut(ctx) },
					func(_ struct{}, err error) tea.Msg {
						if err != nil {
							return doneMsg{say: "Could not sign out of GitHub", err: err}
						}
						return loadedMsg{apply: func(a *app) tea.Cmd { a.say("Signed out of GitHub"); return a.loadGitHub() }}
					})
			}},
	)
	return mn
}

// sourceMenu is one Git source: what it reaches, its name, and
// disconnecting it.
func (a *app) sourceMenu(id string) *menu {
	var s wire.AccountResource
	for _, x := range a.sources {
		if x.ResourceID == id {
			s = x
		}
	}
	mn := &menu{title: s.Name, hint: "A GitHub account or organization Barad is installed on.", reload: func(a *app) *menu { return a.sourceMenu(id) },
		load: fetch("its repositories", func(ctx context.Context) ([]wire.GitHubRepository, error) { return a.api.SourceRepositories(ctx, id) },
			func(a *app, r []wire.GitHubRepository) { a.sourceRepos[id] = r })}
	repos := "loading"
	if r, ok := a.sourceRepos[id]; ok {
		repos = plural(len(r), "repository")
	}
	mn.items = append(mn.items,
		item{label: "Repositories", value: repos, sub: func(a *app) *menu {
			list := &menu{title: s.Name + "'s repositories"}
			for _, r := range a.sourceRepos[id] {
				list.items = append(list.items, item{label: r.FullName, value: r.DefaultBranch})
			}
			return list
		}},
		item{label: "Rename", sub: func(a *app) *menu {
			return a.renameMenu("git-sources", id, s.Name, func(a *app) tea.Cmd { return a.loadGitHub() })
		}},
		item{label: "Disconnect", danger: true, action: "Disconnect " + s.Name, ask: "Sessions lose access to its repositories. Their clones stay.",
			run: func(a *app) tea.Cmd {
				return call(func(ctx context.Context) (struct{}, error) {
					return struct{}{}, a.api.RemoveConnection(ctx, "git-sources", id)
				},
					func(_ struct{}, err error) tea.Msg {
						if err != nil {
							return doneMsg{say: "Could not disconnect", err: err}
						}
						return loadedMsg{apply: func(a *app) tea.Cmd { a.say("Disconnected " + s.Name); return a.loadGitHub() }}
					})
			}},
	)
	return mn
}

// identityMenu is who commits are by: a change saves at once, and reaches a
// session when its computer next wakes.
func (a *app) identityMenu() *menu {
	mn := &menu{title: "Git identity", reload: func(a *app) *menu { return a.identityMenu() }}
	id := a.identity
	if id == nil {
		mn.hint = "Loading…"
		return mn
	}
	author, committer := api.Identity(*id)
	mn.hint = "Author " + author + "  ·  committer " + committer + ". A session takes a change when it next wakes."
	save := func(change func(s *wire.GitSettings)) tea.Cmd {
		next := *a.identity
		change(&next)
		return call(func(ctx context.Context) (*wire.GitSettings, error) { return a.api.PutGitSettings(ctx, next) },
			func(s *wire.GitSettings, err error) tea.Msg {
				return loadedMsg{apply: func(a *app) tea.Cmd { a.identity = s; a.say("Saved the Git identity"); return nil }, what: "Could not save the Git identity", err: err}
			})
	}
	field := func(label, value string, set func(s *wire.GitSettings, v string)) item {
		return item{label: label, value: value, sub: func(a *app) *menu {
			e := &Editor{}
			e.Insert(value)
			return &menu{title: label, edit: e, submit: func(a *app, v string) tea.Cmd {
				return save(func(s *wire.GitSettings) { set(s, v) })
			}}
		}}
	}
	mn.items = append(mn.items,
		field("Name", id.UserName, func(s *wire.GitSettings, v string) { s.UserName = v }),
		field("Email", id.HumanEmail, func(s *wire.GitSettings, v string) { s.HumanEmail = v }),
		item{label: "Separate author and committer", value: onOff(id.SeparateIdentities, "on", "off"), stay: true,
			run: func(a *app) tea.Cmd {
				return save(func(s *wire.GitSettings) { s.SeparateIdentities = !s.SeparateIdentities })
			}},
		item{label: "Barad is the author", value: onOff(id.BaradIsAuthor, "on", "off"), stay: true,
			run: func(a *app) tea.Cmd { return save(func(s *wire.GitSettings) { s.BaradIsAuthor = !s.BaradIsAuthor }) }},
	)
	return mn
}

// ---------------------------------------------------------------- billing

func (a *app) loadBilling() tea.Cmd {
	return fetch("your credits", a.api.Billing, func(a *app, b *wire.BillingAccount) { a.balance = b })
}

// buyMenu buys credits: an amount, the terms if they are new to you, then
// the payment page in your browser. The balance updates when you pay.
func (a *app) buyMenu() *menu {
	mn := &menu{title: "Buy credits", reload: func(a *app) *menu { return a.buyMenu() }}
	b := a.balance
	if b == nil {
		mn.hint = "Loading…"
		return mn
	}
	mn.hint = money(b.BalanceUSDNanos) + " left. Workspaces cost " + money(b.WorkspaceRateUSDNanosPerHour) + " an hour while awake; asleep they are free."
	buy := func(cents int64) item {
		label := money(cents * 10_000_000)
		it := item{label: label, run: func(a *app) tea.Cmd { return a.checkout(cents) }}
		if !b.TermsAccepted {
			it.danger, it.action = true, "Accept the terms and pay "+label
			it.ask = "Buying credits accepts Barad's Terms of Service (" + b.TermsVersion + "): " + b.TermsURL
		}
		return it
	}
	for _, d := range []int64{10, 25, 50, 100} {
		if c := d * 100; c >= b.MinimumPurchaseUSDCents && c <= b.MaximumPurchaseUSDCents {
			mn.items = append(mn.items, buy(c))
		}
	}
	mn.items = append(mn.items, item{label: "Another amount", sub: func(a *app) *menu {
		return &menu{title: "Buy credits", edit: &Editor{}, about: fmt.Sprintf("dollars, %s to %s", money(b.MinimumPurchaseUSDCents*10_000_000), money(b.MaximumPurchaseUSDCents*10_000_000)),
			submit: func(a *app, text string) tea.Cmd {
				d, err := strconv.ParseFloat(strings.TrimPrefix(text, "$"), 64)
				cents := int64(d*100 + 0.5)
				if err != nil || cents < b.MinimumPurchaseUSDCents || cents > b.MaximumPurchaseUSDCents {
					return a.fail("Could not buy credits", fmt.Errorf("buy between %s and %s", money(b.MinimumPurchaseUSDCents*10_000_000), money(b.MaximumPurchaseUSDCents*10_000_000)))
				}
				if it := buy(cents); it.danger {
					a.menu = &menu{confirm: &it}
					return nil
				}
				return a.checkout(cents)
			}}
	}})
	return mn
}

type paidMsg struct {
	b   *wire.BillingAccount
	err error
}

// checkout accepts the terms if need be, opens the payment page, and
// watches the balance until the payment arrives (half an hour at most).
func (a *app) checkout(cents int64) tea.Cmd {
	b := *a.balance
	return call(func(ctx context.Context) (*wire.Checkout, error) {
		if !b.TermsAccepted {
			if err := a.api.AcceptTerms(ctx, b.TermsVersion); err != nil {
				return nil, err
			}
		}
		return a.api.Checkout(ctx, randomID("tui_", 12), cents, b.TermsVersion)
	}, func(c *wire.Checkout, err error) tea.Msg {
		if err != nil {
			return doneMsg{say: "Could not buy credits", err: err}
		}
		return loadedMsg{apply: func(a *app) tea.Cmd {
			_ = OpenURL(c.URL)
			a.say("Pay in your browser: your credits arrive here when you do")
			a.paying, a.payUntil = &b, a.now.Add(30*time.Minute)
			return a.pollPayment()
		}}
	})
}

func (a *app) pollPayment() tea.Cmd {
	return tea.Tick(pollEvery, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		b, err := a.api.Billing(ctx)
		return paidMsg{b, err}
	})
}

func (a *app) paymentStep(msg paidMsg) tea.Cmd {
	if a.paying == nil || msg.err != nil {
		return nil
	}
	a.balance = msg.b
	if msg.b.BalanceUSDNanos > a.paying.BalanceUSDNanos {
		a.paying = nil
		a.say("Paid: " + money(msg.b.BalanceUSDNanos) + " of credits")
		a.reloadMenu()
		return nil
	}
	if a.now.After(a.payUntil) {
		a.paying = nil
		return nil
	}
	return a.pollPayment()
}

// historyMenu is what came into and went out of the balance.
func (a *app) historyMenu() *menu {
	mn := &menu{title: "Balance history", reload: func(a *app) *menu { return a.historyMenu() },
		load: fetch("the balance history", func(ctx context.Context) (*wire.BillingHistory, error) { return a.api.BillingHistory(ctx, "") },
			func(a *app, h *wire.BillingHistory) { a.history = h.Entries })}
	if a.history == nil {
		mn.hint = "Loading…"
	}
	for _, e := range a.history {
		what := map[string]string{"account_initialized": "Account opened", "workspace_runtime": "Workspace time", "purchase": "Credits bought", "refund": "Refund",
			"chargeback": "Chargeback", "chargeback_credit": "Chargeback returned", "promotional_grant": "Free credits", "adjustment": "Adjustment"}[e.Kind]
		if s, ok := a.session(e.SessionID); ok {
			what += ": " + sessionName(s)
		}
		amount := money(e.AmountUSDNanos)
		if e.AmountUSDNanos > 0 {
			amount = "+" + amount
		}
		mn.items = append(mn.items, item{label: firstOf(what, e.Kind), value: amount + "  ·  " + clock(e.EffectiveAt, a.now)})
	}
	return mn
}

// ---------------------------------------------------------------- the account

// passwordMenu asks for the current password, then the new one twice.
func (a *app) passwordMenu() *menu {
	return &menu{title: "Current password", edit: &Editor{}, mask: true, about: "your password now", submit: func(a *app, previous string) tea.Cmd {
		a.showMenu(&menu{title: "New password", edit: &Editor{}, mask: true, about: "the new password", submit: func(a *app, proposed string) tea.Cmd {
			a.showMenu(&menu{title: "New password again", edit: &Editor{}, mask: true, about: "the new password, again", submit: func(a *app, again string) tea.Cmd {
				if again != proposed {
					return a.fail("Could not change the password", fmt.Errorf("the new passwords differ"))
				}
				return call(func(ctx context.Context) (struct{}, error) {
					return struct{}{}, a.api.ChangePassword(ctx, previous, proposed)
				},
					func(_ struct{}, err error) tea.Msg {
						return outcome("Changed your password", "Could not change the password", err, false)
					})
			}})
			return nil
		}})
		return nil
	}}
}

// outcome ends an action: what to say when it worked, or when it failed.
func outcome(say, failed string, err error, account bool) doneMsg {
	if err != nil {
		return doneMsg{say: failed, err: err}
	}
	return doneMsg{say: say, account: account}
}

func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	if strings.HasSuffix(thing, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(thing, "y"))
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

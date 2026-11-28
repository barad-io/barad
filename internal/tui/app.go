// Package tui implements Barad's primary-buffer inline coding-agent TUI:
// full-width conversation viewport backed by native terminal scrollback,
// pinned bottom composer with slash commands and @file completion, single-line
// status footer, on-demand overlays with 100% web-app parity, and per-pane
// OSC 7501 status reporting for Rex and modern terminal multiplexers.
package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/internal/shell"
	"github.com/barad-io/barad/wire"
)

// Options controls how the Barad TUI launches.
type Options struct {
	SessionQuery string // attach directly to a matching session ID or name
	Resume       bool   // attach directly to the last active session
	Version      string // build version string
}

type app struct {
	api           *api.Client
	opts          Options
	w, h          int
	frame         int
	now           time.Time
	prefs         prefs.Prefs
	terminalLight *bool
	exit          error
	farewell      string

	// Account state.
	sessions     []wire.Session
	archived     []wire.Session
	listed       bool
	listing      bool
	relist       bool
	projects     []wire.Project
	agents       []wire.AgentSpec
	sizes        []wire.MachineSize
	repos        []api.Repository
	reposLoaded  bool
	sessionRepos map[string][]wire.SessionGitRepository
	envs         map[string]*wire.EnvironmentView
	github       *wire.GitHubAccount
	identity     *wire.GitSettings
	usage        *wire.UsageReport
	workspace    *wire.WorkspaceUsage
	balance      *wire.BillingAccount
	history      []wire.BillingEntry
	usageDays    int
	usageTab     int
	wsFiles      map[string][]string
	online       bool
	status       statusTracker

	// Active session + primary-buffer scrollback state.
	conv              *conversation
	flushed           int  // settled exchanges already printed via tea.Println
	headerPrinted     bool // whether the attached session header divider was printed
	lastActiveTurnID  string
	cancelRequestedOn string
	lastCtrlC         time.Time

	// Composer & overlays.
	comp    Editor
	attach  []attachment
	compSel int
	ov      overlay

	toast    string
	toastBad bool
	toastAt  time.Time
}

// Run launches the primary-buffer inline Barad TUI for a signed-in client.
func Run(client *api.Client, opts Options) error {
	a := newApp(client, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tea.NewProgram(a, tea.WithFPS(60))
	go a.follow(ctx, p)
	if _, err := p.Run(); err != nil {
		return err
	}
	if a.farewell != "" {
		fmt.Println(a.farewell)
	}
	return a.exit
}

func newApp(client *api.Client, opts Options) *app {
	a := &app{
		api:          client,
		opts:         opts,
		w:            100,
		h:            30,
		now:          time.Now(),
		prefs:        prefs.Load(),
		sessionRepos: map[string][]wire.SessionGitRepository{},
		envs:         map[string]*wire.EnvironmentView{},
		wsFiles:      map[string][]string{},
		usageDays:    30,
	}
	applyTheme(resolveTheme(a.prefs.Theme, nil))
	if opts.SessionQuery == "" && !opts.Resume {
		a.ov = &pickerOverlay{}
	}
	return a
}

func (a *app) follow(ctx context.Context, p *tea.Program) {
	hints := make(chan api.Dirty)
	go func() {
		for {
			select {
			case h := <-hints:
				p.Send(dirtyMsg(h))
			case <-ctx.Done():
				return
			}
		}
	}()
	wait := time.Second
	for ctx.Err() == nil {
		began := time.Now()
		err := a.api.Updates(ctx, func() { p.Send(onlineMsg{online: true}) }, hints)
		if ctx.Err() != nil {
			return
		}
		p.Send(onlineMsg{err: err})
		if errors.Is(err, api.ErrSignedOut) {
			return
		}
		if time.Since(began) > time.Minute {
			wait = time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		wait = min(wait*2, 30*time.Second)
	}
}

// ---------------------------------------------------------------- messages

type (
	tickMsg   time.Time
	onlineMsg struct {
		online bool
		err    error
	}
	dirtyMsg    api.Dirty
	sessionsMsg struct {
		sessions []wire.Session
		err      error
	}
	syncMsg struct {
		id   string
		data *api.Sync
		err  error
	}
	olderMsg struct {
		id   string
		data *api.Older
		err  error
	}
	sentMsg struct {
		id, requestID, text string
		attach              []attachment
		err                 error
	}
	createdMsg struct {
		session *wire.Session
		err     error
	}
	modelSavedMsg struct {
		session *wire.Session
		err     error
	}
	agentsMsg struct {
		res *wire.AgentsResponse
		err error
	}
	projectsMsg struct {
		projects []wire.Project
		err      error
	}
	reposMsg struct {
		repos []api.Repository
		err   error
	}
	sessionReposMsg struct {
		id    string
		repos []wire.SessionGitRepository
		err   error
	}
	envLoadedMsg struct {
		key   string
		view  *wire.EnvironmentView
		err   error
		saved bool
	}
	filesLoadedMsg struct {
		sessionID   string
		dir         string
		entries     []wire.FileEntry
		err         error
		openOverlay bool
	}
	filePreviewMsg struct {
		path    string
		content string
		err     error
	}
	usageMsg struct {
		report    *wire.UsageReport
		workspace *wire.WorkspaceUsage
		balance   *wire.BillingAccount
		err       error
	}
	billingMsg struct {
		balance *wire.BillingAccount
		history []wire.BillingEntry
		err     error
	}
	accountInfoMsg struct {
		github   *wire.GitHubAccount
		identity *wire.GitSettings
		err      error
	}
	clipboardMsg struct {
		data []byte
		err  error
	}
	execDoneMsg struct {
		kind string
		err  error
	}
	shareMsg struct {
		url string
	}
	doneMsg struct {
		say            string
		err            error
		relist         bool
		resync         bool
		open           string
		session        *wire.Session
		detach         bool
		reloadProjects bool
		reloadBilling  bool
		reloadReposFor string
	}
)

var frameEvery = 100 * time.Millisecond

func tick() tea.Cmd {
	return tea.Tick(frameEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func call[T any](f func(ctx context.Context) (T, error), done func(T, error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		v, err := f(ctx)
		return done(v, err)
	}
}

func (a *app) Init() tea.Cmd {
	return tea.Batch(
		tick(),
		a.list(),
		a.loadAgents(),
		a.loadProjects(),
		tea.RequestBackgroundColor,
	)
}

func (a *app) quit() tea.Cmd {
	return tea.Sequence(a.status.clear(), tea.Quit)
}

// ---------------------------------------------------------------- Update

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			a.w = msg.Width
		}
		if msg.Height > 0 {
			a.h = msg.Height
		}
		return a, nil

	case tea.BackgroundColorMsg:
		light := !msg.IsDark()
		a.terminalLight = &light
		applyTheme(resolveTheme(a.prefs.Theme, a.terminalLight))
		return a, nil

	case tickMsg:
		a.frame++
		a.now = time.Time(msg)
		cmds := []tea.Cmd{tick()}
		// Fallback sync poll while turn active or waking (in case WS reconnects).
		if a.conv != nil && !a.conv.syncing && a.frame%25 == 0 {
			s := a.conv.session
			if s.CurrentTurnID != "" || s.MachineStatus == wire.MachineStatusHydrating || s.PendingTurn || len(a.conv.pending) > 0 {
				cmds = append(cmds, a.syncConv(a.conv))
			}
		}
		if c := a.status.emit(a.conv); c != nil {
			cmds = append(cmds, c)
		}
		return a, tea.Batch(cmds...)

	case onlineMsg:
		wasOffline := !a.online
		a.online = msg.online
		if errors.Is(msg.err, api.ErrSignedOut) {
			a.exit = errors.New("signed out; run barad login")
			return a, a.quit()
		}
		if msg.online && wasOffline {
			cmds := []tea.Cmd{a.list()}
			if a.conv != nil {
				cmds = append(cmds, a.syncConv(a.conv))
			}
			return a, tea.Batch(cmds...)
		}
		return a, nil

	case dirtyMsg:
		var cmds []tea.Cmd
		if len(msg.Summaries) > 0 {
			cmds = append(cmds, a.list())
		}
		if a.conv != nil && slices.Contains(msg.Sessions, a.conv.id) {
			cmds = append(cmds, a.syncConv(a.conv))
		}
		return a, tea.Batch(cmds...)

	case sessionsMsg:
		a.listing = false
		var cmds []tea.Cmd
		if a.relist {
			a.relist = false
			cmds = append(cmds, a.list())
		}
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, tea.Batch(cmds...)
		}
		a.listed = true
		a.sessions, a.archived = nil, nil
		for _, s := range msg.sessions {
			switch {
			case s.MachineStatus == wire.MachineStatusDeleting:
			case !s.ArchivedAt.IsZero() || s.MachineStatus == wire.MachineStatusArchived:
				a.archived = append(a.archived, s)
			default:
				a.sessions = append(a.sessions, s)
			}
		}
		if a.prefs.Reviewed == nil {
			a.prefs, _ = prefs.Update(func(p *prefs.Prefs) {
				if p.Reviewed == nil {
					p.Read(msg.sessions)
				}
			})
		}
		// Keep attached session metadata fresh.
		if a.conv != nil {
			for _, s := range msg.sessions {
				if s.SessionID == a.conv.id {
					if a.lastActiveTurnID != "" && s.CurrentTurnID == "" && s.LatestTerminalTurnID == a.lastActiveTurnID {
						a.lastActiveTurnID = ""
						if s.LatestTerminalTurnStatus == wire.TurnStatusCompleted {
							a.status.markTurnCompleted(s.LatestTerminalTurnID)
						}
					}
					a.conv.session = s
					break
				}
			}
		}
		// Handle initial auto-attach (`barad <session>` or `barad --resume`).
		if a.conv == nil && (a.opts.SessionQuery != "" || a.opts.Resume) {
			if target, ok := a.resolveStartupSession(); ok {
				a.opts.SessionQuery, a.opts.Resume = "", false
				cmds = append(cmds, a.attachSession(target))
			} else {
				if a.opts.SessionQuery != "" {
					a.flash(fmt.Sprintf("No session matching %q — pick one below", a.opts.SessionQuery), true)
				}
				a.opts.SessionQuery, a.opts.Resume = "", false
				a.ov = &pickerOverlay{}
			}
		}
		if c := a.status.emit(a.conv); c != nil {
			cmds = append(cmds, c)
		}
		return a, tea.Batch(cmds...)

	case syncMsg:
		if a.conv == nil || a.conv.id != msg.id {
			return a, nil
		}
		a.conv.syncing = false
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		prevTurn := a.lastActiveTurnID
		more := a.conv.applySync(msg.data)
		if a.conv.session.CurrentTurnID != "" {
			a.lastActiveTurnID = a.conv.session.CurrentTurnID
		} else if prevTurn != "" && a.conv.session.LatestTerminalTurnID == prevTurn {
			a.lastActiveTurnID = ""
			if a.conv.session.LatestTerminalTurnStatus == wire.TurnStatusCompleted {
				a.status.markTurnCompleted(prevTurn)
			}
		}
		// Mark session read while attached.
		if a.conv.session.LatestTerminalTurnID != "" && a.prefs.Reviewed[a.conv.id] != a.conv.session.LatestTerminalTurnID {
			s := a.conv.session
			a.prefs, _ = prefs.Update(func(p *prefs.Prefs) { p.Read([]wire.Session{s}) })
		}
		var cmds []tea.Cmd
		if flushCmd := a.flushCommitted(); flushCmd != nil {
			cmds = append(cmds, flushCmd)
		}
		if more || a.conv.resync {
			a.conv.resync = false
			cmds = append(cmds, a.syncConv(a.conv))
		}
		// Prefetch workspace root file list once when hydrated for `@` completion.
		if a.conv.session.MachineStatus == wire.MachineStatusHydrated {
			if _, ok := a.wsFiles[a.conv.id]; !ok {
				a.wsFiles[a.conv.id] = []string{} // mark in-flight
				cmds = append(cmds, a.fetchFiles(a.conv.id, ".", false))
			}
		}
		if c := a.status.emit(a.conv); c != nil {
			cmds = append(cmds, c)
		}
		return a, tea.Batch(cmds...)

	case olderMsg:
		if a.conv == nil || a.conv.id != msg.id {
			return a, nil
		}
		a.conv.loadingOlder = false
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		beforeLen := len(a.conv.exchanges())
		a.conv.applyOlder(msg.data)
		added := len(a.conv.exchanges()) - beforeLen
		if added > 0 {
			a.flushed += added
			// Print the newly loaded older exchanges to scrollback cleanly.
			var lines []string
			lines = append(lines, Line{S("── Earlier history ──", SFaint)}.Paint(nil, 0))
			for _, ex := range a.conv.exchanges()[:added] {
				lines = append(lines, renderExchange(a.conv, ex, a.w, a.prefs.Work, a.frame, a.now, a.cancelRequestedOn)...)
				lines = append(lines, "")
			}
			a.flash(fmt.Sprintf("Loaded %d older exchanges", added), false)
			return a, printlnChunked(lines, a.h)
		}
		a.flash("No older messages", false)
		return a, nil

	case sentMsg:
		if msg.err != nil {
			if a.conv != nil && a.conv.id == msg.id {
				a.conv.sending = slices.DeleteFunc(a.conv.sending, func(p api.PendingMessage) bool { return p.RequestID == msg.requestID })
				a.conv.changed()
				if a.comp.Value() == "" {
					a.comp.Insert(msg.text)
				}
				a.attach = append(msg.attach, a.attach...)
			}
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		if a.conv != nil && a.conv.id == msg.id {
			return a, tea.Batch(a.syncConv(a.conv), a.status.emit(a.conv))
		}
		return a, nil

	case createdMsg:
		if msg.err != nil {
			if n, ok := a.ov.(*newSessionOverlay); ok {
				n.busy = false
				n.errMsg = msg.err.Error()
			} else {
				a.flash(msg.err.Error(), true)
			}
			return a, nil
		}
		a.ov = nil
		a.sessions = append([]wire.Session{*msg.session}, a.sessions...)
		a.flash("Created "+sessionTitle(*msg.session), false)
		return a, tea.Batch(a.list(), a.attachSession(*msg.session))

	case modelSavedMsg:
		if msg.err != nil {
			if m, ok := a.ov.(*modelOverlay); ok {
				m.saving = false
				m.errMsg = msg.err.Error()
			} else {
				a.flash(msg.err.Error(), true)
			}
			return a, nil
		}
		if a.conv != nil && msg.session != nil && a.conv.id == msg.session.SessionID {
			a.conv.session = *msg.session
		}
		a.ov = nil
		a.flash("Updated model settings", false)
		return a, a.list()

	case agentsMsg:
		if msg.err == nil && msg.res != nil {
			a.agents = msg.res.Agents
			a.sizes = msg.res.Sizes
			if n, ok := a.ov.(*newSessionOverlay); ok {
				n.applyProjectDefaults(a)
			}
		}
		return a, nil

	case projectsMsg:
		if msg.err == nil {
			a.projects = msg.projects
			if lo, ok := a.ov.(*listOverlay); ok && lo.title == "Projects" {
				a.ov = a.buildProjectsOverlay()
			}
		}
		return a, nil

	case reposMsg:
		if msg.err == nil {
			a.repos = msg.repos
			a.reposLoaded = true
			if lo, ok := a.ov.(*listOverlay); ok && strings.HasPrefix(lo.title, "Git repositories") && a.conv != nil {
				parent := lo.parent
				next := a.buildReposOverlay(a.conv.session)
				next.parent = parent
				a.ov = next
			}
		}
		return a, nil

	case sessionReposMsg:
		if msg.err == nil {
			a.sessionRepos[msg.id] = msg.repos
			if lo, ok := a.ov.(*listOverlay); ok && a.conv != nil && a.conv.id == msg.id {
				if strings.HasPrefix(lo.title, "Session settings") {
					a.ov = a.buildSettingsOverlay(a.conv.session)
				} else if strings.HasPrefix(lo.title, "Git repositories") {
					parent := lo.parent
					next := a.buildReposOverlay(a.conv.session)
					next.parent = parent
					a.ov = next
				}
			}
		}
		return a, nil

	case envLoadedMsg:
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		a.envs[msg.key] = msg.view
		if msg.saved {
			a.flash("Environment updated", false)
		}
		if lo, ok := a.ov.(*listOverlay); ok {
			if strings.HasPrefix(lo.title, "Session settings") && a.conv != nil {
				a.ov = a.buildSettingsOverlay(a.conv.session)
			} else if strings.HasPrefix(lo.title, "Session env") || strings.HasPrefix(lo.title, "Project env") {
				kind, id, _ := strings.Cut(msg.key, "/")
				parent := lo.parent
				next := a.buildEnvOverlay(api.EnvironmentOwner{Kind: kind, ID: id}, lo.title)
				next.parent = parent
				a.ov = next
			}
		}
		return a, nil

	case filesLoadedMsg:
		if msg.err != nil {
			if msg.openOverlay {
				a.ov = nil
				a.flash(msg.err.Error(), true)
			}
			return a, nil
		}
		if msg.dir == "." {
			var names []string
			for _, e := range msg.entries {
				n := e.Name
				if e.Type == "directory" {
					n += "/"
				}
				names = append(names, n)
			}
			a.wsFiles[msg.sessionID] = names
		}
		if msg.openOverlay {
			a.ov = a.buildFilesOverlay(msg.sessionID, msg.dir, msg.entries)
		}
		return a, nil

	case filePreviewMsg:
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		if lo, ok := a.ov.(*listOverlay); ok {
			lo.viewer = &textViewer{
				title: "File · " + msg.path,
				lines: codeLines(msg.content, msg.path[max(0, strings.LastIndex(msg.path, ".")):], max(30, a.w-4)),
			}
		}
		return a, nil

	case usageMsg:
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		a.usage, a.workspace, a.balance = msg.report, msg.workspace, msg.balance
		return a, nil

	case billingMsg:
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		a.balance, a.history = msg.balance, msg.history
		if lo, ok := a.ov.(*listOverlay); ok && lo.title == "Billing & Credits" {
			a.ov = a.buildBillingOverlay()
		}
		return a, nil

	case accountInfoMsg:
		if msg.err == nil {
			a.github, a.identity = msg.github, msg.identity
			if lo, ok := a.ov.(*listOverlay); ok && lo.title == "Account & Preferences" {
				a.ov = a.buildAccountOverlay()
			}
		}
		return a, nil

	case clipboardMsg:
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		a.attach = append(a.attach, attachment{
			name:      fmt.Sprintf("clipboard-%s.png", a.now.Format("150405")),
			data:      msg.data,
			mediaType: "image/png",
		})
		a.flash("Attached clipboard image", false)
		return a, nil

	case execDoneMsg:
		if msg.err != nil {
			a.flash(msg.kind+": "+msg.err.Error(), true)
		} else if msg.kind == "login" {
			a.flash("Harness sign-in finished — syncing session…", false)
		} else {
			a.flash("Returned from shell", false)
		}
		var cmds []tea.Cmd
		cmds = append(cmds, a.list())
		if a.conv != nil {
			cmds = append(cmds, a.syncConv(a.conv))
		}
		return a, tea.Batch(cmds...)

	case shareMsg:
		a.ov = nil
		a.flash("Share link printed above", false)
		line := Line{S("Published share link: ", SMuted), S(msg.url, linked(SAgent.Underline(true), msg.url))}.Paint(nil, 0)
		return a, tea.Println(line)

	case doneMsg:
		if msg.err != nil {
			a.flash(msg.err.Error(), true)
			return a, nil
		}
		if msg.say != "" {
			a.flash(msg.say, false)
		}
		var cmds []tea.Cmd
		if msg.session != nil && a.conv != nil && a.conv.id == msg.session.SessionID {
			a.conv.session = *msg.session
			if lo, ok := a.ov.(*listOverlay); ok && strings.HasPrefix(lo.title, "Session settings") {
				sel := lo.sel
				next := a.buildSettingsOverlay(a.conv.session)
				next.sel = min(sel, max(0, len(next.items)-1))
				a.ov = next
			}
		}
		if msg.detach {
			a.conv = nil
			a.flushed = 0
			a.headerPrinted = false
			a.ov = &pickerOverlay{}
			cmds = append(cmds, a.status.clear())
		}
		if msg.relist {
			cmds = append(cmds, a.list())
		}
		if msg.resync && a.conv != nil {
			cmds = append(cmds, a.syncConv(a.conv))
		}
		if msg.reloadProjects {
			cmds = append(cmds, a.loadProjects())
		}
		if msg.reloadBilling {
			cmds = append(cmds, a.loadBilling())
		}
		if msg.reloadReposFor != "" {
			cmds = append(cmds, a.loadSessionRepos(msg.reloadReposFor))
		}
		if msg.open != "" {
			for _, s := range a.sessions {
				if s.SessionID == msg.open {
					cmds = append(cmds, a.attachSession(s))
					break
				}
			}
		}
		return a, tea.Batch(cmds...)

	case tea.PasteMsg:
		if a.ov != nil {
			return a, nil
		}
		pasted := strings.TrimSpace(msg.Content)
		if pasted != "" && !strings.Contains(pasted, "\n") {
			if at, err := readAttachment(pasted); err == nil {
				if len(a.attach) >= wire.PromptMediaMaximumItems {
					a.flash("Maximum attachments reached", true)
					return a, nil
				}
				a.attach = append(a.attach, at)
				a.flash("Attached "+at.name, false)
				return a, nil
			}
		}
		a.comp.Insert(msg.Content)
		return a, nil

	case tea.KeyPressMsg:
		a.status.ackUserInput()
		cmd := a.handleKey(msg)
		if stCmd := a.status.emit(a.conv); stCmd != nil {
			return a, tea.Batch(cmd, stCmd)
		}
		return a, cmd
	}
	return a, nil
}

func (a *app) handleKey(k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	// Global Ctrl+P opens fuzzy session picker (except when already inside picker).
	if ks == "ctrl+p" {
		if _, isPicker := a.ov.(*pickerOverlay); isPicker {
			if a.conv != nil {
				a.ov = nil
			}
			return nil
		}
		return a.openPicker("")
	}
	// Global Ctrl+C handling: close overlay, stop turn, clear input, or quit on double press.
	if ks == "ctrl+c" {
		if a.ov != nil {
			if a.conv == nil {
				return a.quit()
			}
			a.ov = nil
			return nil
		}
		if a.conv != nil && a.conv.session.CurrentTurnID != "" && a.comp.Value() == "" && a.now.Sub(a.lastCtrlC) > 2*time.Second {
			a.lastCtrlC = a.now
			return a.cancelTurn()
		}
		if a.comp.Value() != "" {
			a.comp.Reset()
			a.lastCtrlC = a.now
			a.flash("Cleared input (press Ctrl+C again to quit)", false)
			return nil
		}
		return a.quit()
	}

	if a.ov != nil {
		return a.ov.Key(a, k)
	}

	// Completion popup navigation (/commands or @files).
	if comps := a.completions(); len(comps) > 0 {
		switch ks {
		case "up":
			if a.compSel > 0 {
				a.compSel--
			} else {
				a.compSel = len(comps) - 1
			}
			return nil
		case "down":
			a.compSel = (a.compSel + 1) % len(comps)
			return nil
		case "tab":
			idx := min(max(0, a.compSel), len(comps)-1)
			return comps[idx].apply(a)
		case "enter":
			if !a.prefs.Newline {
				idx := min(max(0, a.compSel), len(comps)-1)
				return comps[idx].apply(a)
			}
		case "esc":
			a.comp.Reset()
			a.compSel = 0
			return nil
		}
	}

	switch ks {
	case "esc":
		if a.conv != nil && a.conv.session.CurrentTurnID != "" {
			return a.cancelTurn()
		}
		return nil
	case "ctrl+t":
		a.cycleWork()
		return nil
	case "ctrl+v":
		return func() tea.Msg {
			data, err := clipboardImage()
			return clipboardMsg{data, err}
		}
	case "backspace":
		if a.comp.Value() == "" && len(a.attach) > 0 {
			a.attach = a.attach[:len(a.attach)-1]
			return nil
		}
	case "enter", "alt+enter", "ctrl+enter":
		sends := (ks == "enter") != a.prefs.Newline
		if !sends {
			a.comp.Insert("\n")
			return nil
		}
		val := strings.TrimSpace(a.comp.Value())
		if strings.HasPrefix(val, "/") {
			if cmd, ok := a.runSlash(val); ok {
				return cmd
			}
		}
		if a.conv == nil {
			return a.openPicker("")
		}
		if !a.conv.session.ArchivedAt.IsZero() || a.conv.session.MachineStatus == wire.MachineStatusArchived {
			return nil
		}
		if !a.conv.session.IsV2() {
			a.flash("Legacy read-only session — run /continue to continue in v2", true)
			return nil
		}
		if val != "" || len(a.attach) > 0 {
			return a.sendMessage()
		}
		return nil
	}
	a.comp.Key(k, max(12, a.w-2))
	a.compSel = 0
	return nil
}

// ---------------------------------------------------------------- View (Primary Buffer Inline)

func (a *app) View() tea.View {
	w := max(a.w, 32)
	var sections []string

	// 1. Live active/unflushed exchange (streaming text, active tools, spinner).
	if a.conv != nil {
		exs := a.conv.exchanges()
		start := min(a.flushed, len(exs))
		for i := start; i < len(exs); i++ {
			rendered := renderExchange(a.conv, exs[i], w, a.prefs.Work, a.frame, a.now, a.cancelRequestedOn)
			sections = append(sections, rendered...)
			if i < len(exs)-1 {
				sections = append(sections, "")
			}
		}
		// Auth Gate / legacy notices.
		if a.conv.session.AuthRequired() {
			sections = append(sections, Line{
				S("⚠ ", SWarn.Bold(true)),
				S("Sign-in required for "+harnessLabel(a.conv.session.HarnessID())+" — run ", SWarn),
				S("/login", SAccent.Bold(true)),
				S(" to authenticate and run queued prompts", SWarn),
			}.Paint(nil, 0))
		} else if !a.conv.session.IsV2() {
			sections = append(sections, Line{
				S("ℹ ", SAgent),
				S("Legacy read-only session — run ", SMuted),
				S("/continue", SAccent.Bold(true)),
				S(" to continue in a new v2 session", SMuted),
			}.Paint(nil, 0))
		}
	}

	// 2. Uniform post-turn queued prompt banner.
	for _, ql := range a.renderQueuedBanner(w) {
		sections = append(sections, ql.Paint(nil, 0))
	}

	// 3. On-demand overlay (invisible until invoked).
	if a.ov != nil {
		maxH := max(10, min(a.h-6, 20))
		for _, ol := range renderOverlayFrame(a.ov, a, w, maxH) {
			sections = append(sections, ol.Paint(nil, 0))
		}
	}

	// 4. Pinned bottom composer.
	for _, cl := range a.renderComposer(w) {
		sections = append(sections, cl.Paint(nil, 0))
	}

	// 5. Single thin status footer.
	sections = append(sections, a.renderFooter(w).Paint(nil, 0))

	v := tea.NewView(strings.Join(sections, "\n"))
	v.AltScreen = false
	v.MouseMode = tea.MouseModeNone
	v.WindowTitle = a.windowTitle()
	return v
}

func (a *app) windowTitle() string {
	if a.conv == nil {
		return "barad"
	}
	return sessionTitle(a.conv.session) + " · barad"
}

// ---------------------------------------------------------------- primary-buffer flushing

// flushCommitted prints newly settled exchanges ONCE into the terminal's native
// primary scrollback via tea.Println so native scroll, selection, and copy work
// seamlessly without ever fighting the terminal emulator or multiplexer.
func (a *app) flushCommitted() tea.Cmd {
	if a.conv == nil || !a.conv.initialized {
		return nil
	}
	var lines []string
	if !a.headerPrinted {
		a.headerPrinted = true
		s := a.conv.session
		mark, mc := harnessMark(s.HarnessID())
		head := Line{
			S("── ", Fg(BorderDefault)),
			S(mark+" ", Fg(mc)),
			S(sessionTitle(s), SText.Bold(true)),
			S(" · "+harnessLabel(s.HarnessID())+"/"+s.Model, SMuted),
			S(" ──", Fg(BorderDefault)),
		}
		lines = append(lines, "", head.Paint(nil, 0))
		if a.conv.hasOlder {
			lines = append(lines, Line{S("  (earlier history available — run /tools and press o to load)", SFaint)}.Paint(nil, 0))
		}
		lines = append(lines, "")
	}

	exs := a.conv.exchanges()
	w := max(a.w, 32)
	for a.flushed < len(exs) && !exs[a.flushed].moving {
		rendered := renderExchange(a.conv, exs[a.flushed], w, a.prefs.Work, a.frame, a.now, a.cancelRequestedOn)
		lines = append(lines, rendered...)
		lines = append(lines, "")
		a.flushed++
	}
	if len(lines) == 0 {
		return nil
	}
	return printlnChunked(lines, a.h)
}

// printlnChunked emits lines to Bubble Tea v2's primary scrollback in small
// chunks so cursedRenderer.insertAbove never exceeds the viewport height and
// always keeps the bottom composer + footer anchored cleanly in view.
func printlnChunked(lines []string, termH int) tea.Cmd {
	if len(lines) == 0 {
		return nil
	}
	chunk := max(1, min(6, (termH-6)/2))
	if len(lines) <= chunk {
		return tea.Println(strings.Join(lines, "\n"))
	}
	cmds := make([]tea.Cmd, 0, (len(lines)+chunk-1)/chunk)
	for i := 0; i < len(lines); i += chunk {
		end := min(len(lines), i+chunk)
		cmds = append(cmds, tea.Println(strings.Join(lines[i:end], "\n")))
	}
	return tea.Sequence(cmds...)
}

// ---------------------------------------------------------------- actions & API calls

func (a *app) attachSession(s wire.Session) tea.Cmd {
	a.conv = newConversation(s)
	a.flushed = 0
	a.headerPrinted = false
	a.lastActiveTurnID = s.CurrentTurnID
	a.cancelRequestedOn = ""
	a.status.ackUserInput()
	a.prefs, _ = prefs.Update(func(p *prefs.Prefs) {
		p.LastSession = s.SessionID
		p.Read([]wire.Session{s})
	})
	return tea.Batch(a.syncConv(a.conv), a.status.emit(a.conv))
}

func (a *app) resolveStartupSession() (wire.Session, bool) {
	all := append(append([]wire.Session{}, a.sessions...), a.archived...)
	if a.opts.Resume {
		if id := a.prefs.LastSession; id != "" {
			for _, s := range all {
				if s.SessionID == id {
					return s, true
				}
			}
		}
		if len(a.sessions) > 0 {
			return a.sessions[0], true
		}
		return wire.Session{}, false
	}
	q := strings.TrimSpace(a.opts.SessionQuery)
	if q == "" {
		return wire.Session{}, false
	}
	for _, s := range all {
		if s.SessionID == q {
			return s, true
		}
	}
	ql := strings.ToLower(q)
	var exact, partial []wire.Session
	for _, s := range all {
		nl := strings.ToLower(sessionTitle(s))
		if nl == ql {
			exact = append(exact, s)
		} else if strings.Contains(nl, ql) {
			partial = append(partial, s)
		}
	}
	if len(exact) == 1 {
		return exact[0], true
	}
	if len(exact) == 0 && len(partial) == 1 {
		return partial[0], true
	}
	return wire.Session{}, false
}

func (a *app) sendMessage() tea.Cmd {
	c := a.conv
	text := strings.TrimSpace(a.comp.Value())
	atts := a.attach
	reqID := randomID("msg_", 8)
	pending := api.PendingMessage{RequestID: reqID, Text: text, Kind: "sending", CreatedAt: a.now}
	for _, at := range atts {
		pending.Media = append(pending.Media, wire.PromptMediaRef{Filename: at.name, MediaType: at.mediaType})
	}
	c.sending = append(c.sending, pending)
	c.changed()
	a.comp.Reset()
	a.attach = nil
	id := c.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var ids []string
		for _, at := range atts {
			mid, err := a.api.UploadMedia(ctx, id, reqID, randomID("media_", 10), at.name, at.mediaType, at.data)
			if err != nil {
				return sentMsg{id, reqID, text, atts, err}
			}
			ids = append(ids, mid)
		}
		_, err := a.api.SendMessage(ctx, id, reqID, text, ids)
		return sentMsg{id, reqID, text, atts, err}
	}
}

func (a *app) cancelTurn() tea.Cmd {
	c := a.conv
	if c == nil || c.session.CurrentTurnID == "" || a.cancelRequestedOn == c.session.CurrentTurnID {
		return nil
	}
	a.cancelRequestedOn = c.session.CurrentTurnID
	a.flash("Stopping active turn…", false)
	id := c.id
	return call(func(ctx context.Context) (struct{}, error) {
		return struct{}{}, a.api.Cancel(ctx, id)
	}, func(_ struct{}, err error) tea.Msg {
		return doneMsg{err: err, resync: true}
	})
}

func (a *app) execShell() tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first (/sessions)", true)
		return nil
	}
	s := a.conv.session
	if !s.ArchivedAt.IsZero() || s.MachineStatus == wire.MachineStatusArchived {
		a.flash("Archived session has no workspace VM", true)
		return nil
	}
	cmd := &shell.Command{Client: a.api, Session: s}
	return tea.Exec(cmd, func(err error) tea.Msg { return execDoneMsg{kind: "shell", err: err} })
}

func (a *app) execLogin() tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first (/sessions)", true)
		return nil
	}
	s := a.conv.session
	if !s.ArchivedAt.IsZero() || s.MachineStatus == wire.MachineStatusArchived {
		a.flash("Archived session has no workspace VM", true)
		return nil
	}
	agentID := s.HarnessID()
	loginCmd := shell.DefaultLoginCommand(agentID)
	for _, ag := range a.agents {
		if strings.EqualFold(ag.ID, agentID) && ag.LoginCommand != "" {
			loginCmd = ag.LoginCommand
			break
		}
	}
	cmd := &shell.LoginCommand{
		Client:       a.api,
		Session:      s,
		AgentID:      agentID,
		LoginCommand: loginCmd,
	}
	return tea.Exec(cmd, func(err error) tea.Msg { return execDoneMsg{kind: "login", err: err} })
}

func (a *app) continueCurrentSession() tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first (/sessions)", true)
		return nil
	}
	s := a.conv.session
	return call(func(ctx context.Context) (*wire.Session, error) {
		res, err := a.api.ContinueSession(ctx, s.SessionID, api.ContinueSessionRequest{})
		if err != nil {
			return nil, err
		}
		return &res.Session, nil
	}, func(ns *wire.Session, err error) tea.Msg {
		return createdMsg{session: ns, err: err}
	})
}

func (a *app) openLatestMedia() tea.Cmd {
	if a.conv == nil {
		a.flash("Attach to a session first", true)
		return nil
	}
	pics := a.conv.pictures()
	if len(pics) == 0 {
		a.flash("No screenshots or attached images in this session", false)
		return nil
	}
	last := pics[len(pics)-1]
	id := a.conv.id
	return call(func(ctx context.Context) (string, error) {
		if last.mediaID != "" {
			return a.api.MediaURL(ctx, id, last.mediaID)
		}
		return a.api.ScreenshotURL(ctx, id, last.shot)
	}, func(u string, err error) tea.Msg {
		if err != nil {
			return doneMsg{err: err}
		}
		_ = OpenURL(u)
		return doneMsg{say: "Opened " + last.name}
	})
}

func (a *app) cycleWork() {
	next := (a.prefs.Work + 1) % 3
	a.prefs, _ = prefs.Update(func(p *prefs.Prefs) { p.Work = next })
	a.flash("Tool output: "+next.String(), false)
}

func (a *app) list() tea.Cmd {
	if a.listing {
		a.relist = true
		return nil
	}
	a.listing = true
	return call(a.api.Sessions, func(list []wire.Session, err error) tea.Msg { return sessionsMsg{list, err} })
}

func (a *app) syncConv(c *conversation) tea.Cmd {
	if c == nil {
		return nil
	}
	if c.syncing {
		c.resync = true
		return nil
	}
	c.syncing = true
	id, initial, afterT, afterE := c.id, !c.initialized, c.afterTranscript, c.afterEvent
	return call(func(ctx context.Context) (*api.Sync, error) {
		return a.api.Sync(ctx, id, initial, afterT, afterE)
	}, func(d *api.Sync, err error) tea.Msg { return syncMsg{id, d, err} })
}

func (a *app) loadOlder() tea.Cmd {
	c := a.conv
	if c == nil || !c.hasOlder || c.loadingOlder || c.before == 0 {
		return nil
	}
	c.loadingOlder = true
	id, before := c.id, c.before
	return call(func(ctx context.Context) (*api.Older, error) {
		return a.api.TranscriptBefore(ctx, id, before)
	}, func(d *api.Older, err error) tea.Msg { return olderMsg{id, d, err} })
}

func (a *app) loadAgents() tea.Cmd {
	return call(a.api.Agents, func(res *wire.AgentsResponse, err error) tea.Msg { return agentsMsg{res, err} })
}

func (a *app) loadProjects() tea.Cmd {
	return call(a.api.Projects, func(ps []wire.Project, err error) tea.Msg { return projectsMsg{ps, err} })
}

func (a *app) loadRepos() tea.Cmd {
	return call(a.api.Repositories, func(rs []api.Repository, err error) tea.Msg { return reposMsg{rs, err} })
}

func (a *app) loadSessionRepos(id string) tea.Cmd {
	return call(func(ctx context.Context) ([]wire.SessionGitRepository, error) {
		return a.api.SessionRepositories(ctx, id)
	}, func(rs []wire.SessionGitRepository, err error) tea.Msg { return sessionReposMsg{id, rs, err} })
}

func (a *app) loadEnv(owner api.EnvironmentOwner) tea.Cmd {
	return call(func(ctx context.Context) (*wire.EnvironmentView, error) {
		return a.api.Environment(ctx, owner)
	}, func(v *wire.EnvironmentView, err error) tea.Msg {
		return envLoadedMsg{key: owner.Kind + "/" + owner.ID, view: v, err: err}
	})
}

func (a *app) loadUsage() tea.Cmd {
	days := a.usageDays
	tz := time.Local.String()
	if tz == "Local" {
		tz = "UTC"
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rep, err := a.api.Usage(ctx, days, tz)
		if err != nil {
			return usageMsg{err: err}
		}
		ws, err := a.api.WorkspaceUsage(ctx, days, tz)
		if err != nil {
			return usageMsg{err: err}
		}
		bal, _ := a.api.Billing(ctx)
		return usageMsg{report: rep, workspace: ws, balance: bal}
	}
}

func (a *app) loadBilling() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		bal, err := a.api.Billing(ctx)
		if err != nil {
			return billingMsg{err: err}
		}
		hist, _ := a.api.BillingHistory(ctx, "")
		var entries []wire.BillingEntry
		if hist != nil {
			entries = hist.Entries
		}
		return billingMsg{balance: bal, history: entries}
	}
}

func (a *app) loadAccountInfo() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		gh, err := a.api.GitHub(ctx)
		if err != nil {
			return accountInfoMsg{err: err}
		}
		id, _ := a.api.GitSettings(ctx)
		return accountInfoMsg{github: gh, identity: id}
	}
}

func (a *app) flash(msg string, bad bool) {
	a.toast, a.toastBad, a.toastAt = msg, bad, a.now
}

func randomID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

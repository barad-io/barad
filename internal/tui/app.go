// Package tui is Barad's terminal app: your sessions on a cover, a session
// as a conversation, one menu for everything rare, and full screens for
// images and usage. It reads and changes state only through the Barad API,
// and follows live updates the way the web app does: a hint arrives on the
// update socket, then the app syncs from the API.
package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/internal/update"
	"github.com/barad-io/barad/wire"
)

type screen int

const (
	screenHome screen = iota
	screenChat
	screenImages
	screenUsage
)

// An attachment is a file waiting to go with the next message: an image,
// or text too long for the message itself.
type attachment struct {
	name      string
	data      []byte
	mediaType string
	pic       *Picture // images only
}

type app struct {
	api      *api.Client
	version  string // the release running, or "dev"
	w, h     int
	gfx      Graphics
	frame    int
	now      time.Time
	prefs    prefs.Prefs
	exit     error  // why the app quit, when it was not asked to
	farewell string // what to say when it has quit

	// The account.
	sessions          []wire.Session // not archived, not being deleted
	archived          []wire.Session // the conversations whose workspaces are gone
	listed            bool
	listing, relist   bool
	projects          []wire.Project
	providers         []wire.ProviderView
	catalogs          map[string]*api.Models // by provider connection
	repos             []api.Repository
	sessionRepos      map[string][]wire.SessionGitRepository // what each session's Git credentials reach
	instructions      map[string]*wire.WorkspaceInstructions // each session's agent instruction files
	envs              map[string]*wire.EnvironmentView       // variables, by "sessions/ID" or "projects/ID"
	branches          map[string]*wire.GitBranches           // by source and repository
	github            *wire.GitHubAccount
	sources           []wire.AccountResource
	sourceRepos       map[string][]wire.GitHubRepository // by Git source
	identity          *wire.GitSettings
	history           []wire.BillingEntry
	users             map[string][]wire.ResourceDependency // the sessions using each connection
	signIn            *signIn                              // a provider sign-in in progress
	awaiting          *awaitGitHub                         // GitHub, in the browser, finishing a step
	paying            *wire.BillingAccount                 // the balance before a payment in the browser
	activityAt        map[string]time.Time                 // when typing was last reported, by session
	payUntil          time.Time
	usage             *wire.UsageReport
	workspace         *wire.WorkspaceUsage
	balance           *wire.BillingAccount
	online            bool
	reported          map[string]string // OSC 7501 sequences sent, by session
	pics              map[string]*Picture
	picErr            map[string]string
	picLoading        map[string]bool
	mast              *Picture // the cover's landscape, drawn for mastKey
	mastKey           string
	mastBusy          bool
	toast             string
	toastBad          bool
	toastAt           time.Time
	notice            string               // a desktop notification to send at the next frame
	md                map[mdKey][]Line     // agent prose, laid out
	laid              map[laidKey][]string // still exchanges, painted (transcript)
	cancelRequestedOn string               // the turn you asked to stop

	// Where you are.
	screen   screen
	sel      string // the selected session on the home screen
	conv     *conversation
	comp     Editor
	attach   []attachment
	note     string // the last thing the composer has to say about attachments
	scroll   int    // transcript rows above the bottom
	filter   *Editor
	shelf    bool     // the home screen lists the archived sessions
	start    *starter // the new session sheet
	menu     *menu
	imgIdx   int
	usageTab int   // 0 Barad, 1 inference
	days     int   // the usage screen's range: 7, 30 or 90 days
	span     *span // text selected with the mouse
}

type mdKey struct {
	text  string
	width int
}

// Run starts the app for a signed-in client and returns when you quit;
// version is the release it is.
func Run(client *api.Client, version string) error {
	a := newApp(client, DetectGraphics())
	a.version = version
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tea.NewProgram(a, tea.WithFPS(120)) // a key shows within 8 ms of its frame
	go a.follow(ctx, p)
	if _, err := p.Run(); err != nil {
		return err
	}
	if a.farewell != "" {
		fmt.Println(a.farewell)
	}
	return a.exit
}

func newApp(client *api.Client, g Graphics) *app {
	a := &app{api: client, gfx: g, now: time.Now(), catalogs: map[string]*api.Models{}, sessionRepos: map[string][]wire.SessionGitRepository{}, instructions: map[string]*wire.WorkspaceInstructions{},
		envs: map[string]*wire.EnvironmentView{}, branches: map[string]*wire.GitBranches{}, sourceRepos: map[string][]wire.GitHubRepository{}, users: map[string][]wire.ResourceDependency{}, activityAt: map[string]time.Time{},
		reported: map[string]string{}, pics: map[string]*Picture{}, picErr: map[string]string{}, picLoading: map[string]bool{}, md: map[mdKey][]Line{}}
	a.prefs = prefs.Load()
	if t := os.Getenv("BARAD_THEME"); t == "night" || t == "paper" {
		a.prefs.Theme = t
	}
	applyTheme(a.prefs.Theme)
	return a
}

// follow keeps the update socket open, reconnecting with backoff, and turns
// its hints into messages.
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
		prompt  string
		err     error
	}
	accountMsg struct {
		projects  []wire.Project
		providers []wire.ProviderView
		err       error
	}
	catalogMsg struct {
		provider string
		models   *api.Models
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
	pictureMsg struct {
		key string
		pic *Picture
		err error
	}
	usageMsg struct {
		report    *wire.UsageReport
		workspace *wire.WorkspaceUsage
		balance   *wire.BillingAccount
		err       error
	}
	clipboardMsg struct {
		data []byte
		err  error
	}
	latestMsg struct {
		tag string
		err error
	}
	mastMsg struct {
		key string
		p   *Picture
	}
	// doneMsg ends an action: what to say, and what to reload.
	doneMsg struct {
		say     string
		err     error
		relist  bool
		resync  bool
		open    string // a session to open
		session *wire.Session
		account bool // reload the projects and providers
	}
)

func (a *app) Init() tea.Cmd {
	return tea.Batch(tick(), a.list(), a.loadAccount(), a.checkLatest(), tea.Raw(ansi.ClearProgramStatusID("barad")))
}

// checkLatest looks up the newest release, at most once a day, for a
// release build.
func (a *app) checkLatest() tea.Cmd {
	if !update.Released(a.version) || a.now.Sub(a.prefs.LatestCheckedAt) < 24*time.Hour {
		return nil
	}
	return call(update.Latest, func(tag string, err error) tea.Msg { return latestMsg{tag, err} })
}

// frameEvery is how often the app draws a frame: spinners, the bar's sweep
// and kitty uploads move on ticks.
var frameEvery = 80 * time.Millisecond

func tick() tea.Cmd {
	return tea.Tick(frameEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// call runs f against the API off the update loop, with a deadline.
func call[T any](f func(ctx context.Context) (T, error), done func(T, error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		v, err := f(ctx)
		return done(v, err)
	}
}

func (a *app) list() tea.Cmd {
	if a.listing {
		a.relist = true
		return nil
	}
	a.listing = true
	return call(a.api.Sessions, func(s []wire.Session, err error) tea.Msg { return sessionsMsg{s, err} })
}

func (a *app) loadAccount() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		projects, err := a.api.Projects(ctx)
		if err != nil {
			return accountMsg{err: err}
		}
		providers, err := a.api.Providers(ctx)
		return accountMsg{projects, providers, err}
	}
}

// loadSessionRepos reads which repositories a session's Git credentials reach.
func (a *app) loadSessionRepos(id string) tea.Cmd {
	if _, ok := a.sessionRepos[id]; !ok {
		a.sessionRepos[id] = nil // asked
	}
	return call(func(ctx context.Context) ([]wire.SessionGitRepository, error) {
		return a.api.SessionRepositories(ctx, id)
	},
		func(r []wire.SessionGitRepository, err error) tea.Msg { return sessionReposMsg{id, r, err} })
}

func (a *app) loadCatalog(provider string) tea.Cmd {
	if provider == "" || a.catalogs[provider] != nil {
		return nil
	}
	return call(func(ctx context.Context) (*api.Models, error) { return a.api.Models(ctx, provider) },
		func(m *api.Models, err error) tea.Msg { return catalogMsg{provider, m, err} })
}

// sync reads the open session from its cursors. A sync already on its way
// is followed by another, so no hint is lost.
func (a *app) sync() tea.Cmd {
	c := a.conv
	if c == nil {
		return nil
	}
	if c.syncing {
		c.resync = true
		return nil
	}
	c.syncing = true
	id, initial, after, event := c.id, !c.initialized, c.afterTranscript, c.afterEvent
	return call(func(ctx context.Context) (*api.Sync, error) { return a.api.Sync(ctx, id, initial, after, event) },
		func(d *api.Sync, err error) tea.Msg { return syncMsg{id, d, err} })
}

func (a *app) loadOlder() tea.Cmd {
	c := a.conv
	if c == nil || !c.hasOlder || c.loadingOlder || c.before == 0 {
		return nil
	}
	c.loadingOlder = true
	id, before := c.id, c.before
	return call(func(ctx context.Context) (*api.Older, error) { return a.api.TranscriptBefore(ctx, id, before) },
		func(d *api.Older, err error) tea.Msg { return olderMsg{id, d, err} })
}

// fetchPictures starts loading every picture in the conversation that is
// not loaded yet.
func (a *app) fetchPictures() tea.Cmd {
	if a.conv == nil {
		return nil
	}
	var cmds []tea.Cmd
	id := a.conv.id
	for _, ref := range a.conv.pictures() {
		if a.pics[ref.key] != nil || a.picLoading[ref.key] || a.picErr[ref.key] != "" {
			continue
		}
		a.picLoading[ref.key] = true
		ref := ref
		cmds = append(cmds, call(func(ctx context.Context) (*Picture, error) {
			var link string
			var err error
			if ref.mediaID != "" {
				link, err = a.api.MediaURL(ctx, id, ref.mediaID)
			} else {
				link, err = a.api.ScreenshotURL(ctx, id, ref.shot)
			}
			if err != nil {
				return nil, err
			}
			data, err := a.api.Fetch(ctx, link)
			if err != nil {
				return nil, err
			}
			return NewPicture(ref.name, data)
		}, func(p *Picture, err error) tea.Msg { return pictureMsg{ref.key, p, err} }))
	}
	return tea.Batch(cmds...)
}

func (a *app) loadUsage() tea.Cmd {
	if a.days == 0 {
		a.days = 30
	}
	days := a.days
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		zone := time.Local.String()
		if zone == "Local" {
			zone = "UTC"
		}
		var m usageMsg
		m.report, m.err = a.api.Usage(ctx, days, zone)
		if m.err != nil {
			return m
		}
		if m.workspace, m.err = a.api.WorkspaceUsage(ctx, days, zone); m.err != nil {
			return m
		}
		m.balance, m.err = a.api.Billing(ctx)
		return m
	}
}

func randomID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + fmt.Sprintf("%x", b)
}

// ---------------------------------------------------------------- update

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
	case tickMsg:
		a.now = time.Time(msg)
		a.frame++
		cmds := []tea.Cmd{tick(), a.report(), a.drawMast()}
		if t := Transmissions(); t != "" && a.frame > 2 { // images sent before the alt screen are lost
			cmds = append(cmds, tea.Raw(t))
		}
		if a.notice != "" {
			cmds = append(cmds, tea.Raw(ansi.Notify(a.notice)))
			a.notice = ""
		}
		return a, tea.Batch(cmds...)
	case onlineMsg:
		if errors.Is(msg.err, api.ErrSignedOut) {
			return a, a.signedOut()
		}
		a.online = msg.online
		if msg.online {
			// Hints sent while the socket was away are lost: catch up.
			return a, tea.Batch(a.list(), a.sync())
		}
	case dirtyMsg:
		// The open session's list entry comes with its sync; any other
		// changed summary reloads the list, as the web app does.
		var cmds []tea.Cmd
		if a.conv != nil && slices.Contains(msg.Sessions, a.conv.id) {
			cmds = append(cmds, a.sync())
		}
		if slices.ContainsFunc(msg.Summaries, func(id string) bool { return a.conv == nil || id != a.conv.id }) {
			cmds = append(cmds, a.list())
		}
		return a, tea.Batch(cmds...)
	case sessionsMsg:
		a.listing = false
		var cmd tea.Cmd
		if a.relist {
			a.relist = false
			cmd = a.list()
		}
		if msg.err != nil {
			return a, tea.Batch(cmd, a.fail("Could not load sessions", msg.err))
		}
		a.setSessions(msg.sessions)
		return a, cmd
	case accountMsg:
		if msg.err != nil {
			return a, a.fail("Could not load your account", msg.err)
		}
		a.projects, a.providers = msg.projects, msg.providers
		a.reloadMenu()
	case loadedMsg:
		if msg.err != nil {
			return a, a.fail(msg.what, msg.err)
		}
		cmd := msg.apply(a)
		a.reloadMenu()
		return a, cmd
	case shellMsg:
		if msg.err != nil {
			return a, a.fail("The shell ended", msg.err)
		}
		return a, tea.Batch(a.sync(), a.list())
	case signInMsg:
		return a, a.signInStep(msg)
	case githubMsg:
		return a, a.githubStep(msg)
	case paidMsg:
		return a, a.paymentStep(msg)
	case catalogMsg:
		if msg.err != nil {
			return a, a.fail("Could not load models", msg.err)
		}
		a.catalogs[msg.provider] = msg.models
		if a.start != nil {
			a.start.settle(a)
		}
		a.reloadMenu()
	case reposMsg:
		if msg.err != nil {
			return a, a.fail("Could not list repositories", msg.err)
		}
		a.repos = msg.repos
		a.reloadMenu()
	case reposChangedMsg:
		if msg.err != nil {
			return a, a.fail("Could not change the repositories", msg.err)
		}
		a.say(msg.say)
		return a, a.loadSessionRepos(msg.id)
	case instructionsMsg:
		if msg.err != nil {
			return a, a.fail("Could not read the agent's instructions", msg.err)
		}
		a.instructions[msg.id] = msg.w
		a.reloadMenu()
	case sessionReposMsg:
		if msg.err != nil {
			delete(a.sessionRepos, msg.id) // ask again at the next sync
			return a, nil
		}
		a.sessionRepos[msg.id] = msg.repos
		a.reloadMenu()
	case syncMsg:
		return a, a.synced(msg)
	case olderMsg:
		if a.conv == nil || a.conv.id != msg.id {
			return a, nil
		}
		a.conv.loadingOlder = false
		if msg.err != nil {
			return a, a.fail("Could not load earlier messages", msg.err)
		}
		a.conv.applyOlder(msg.data)
		return a, a.fetchPictures()
	case sentMsg:
		if msg.err != nil {
			// The message goes back in the box, unless you have started another.
			if a.conv != nil && a.conv.id == msg.id {
				a.conv.sending = slices.DeleteFunc(a.conv.sending, func(p api.PendingMessage) bool { return p.RequestID == msg.requestID })
				a.conv.changed()
				if a.comp.Value() == "" && len(a.attach) == 0 {
					a.comp.Insert(msg.text)
					a.attach = msg.attach
				}
			}
			return a, a.fail("Your message was not sent", msg.err)
		}
		return a, tea.Batch(a.sync(), a.list())
	case createdMsg:
		if msg.session == nil {
			if a.start != nil {
				a.start.busy = false
			}
			return a, a.fail("Could not start the session", msg.err)
		}
		a.start = nil
		a.replaceSession(*msg.session)
		cmds := []tea.Cmd{a.open(msg.session.SessionID), a.list()}
		if msg.err != nil { // the session started, its first message did not
			a.comp.Insert(msg.prompt)
			cmds = append(cmds, a.fail("Your message was not sent", msg.err))
		}
		return a, tea.Batch(cmds...)
	case pictureMsg:
		delete(a.picLoading, msg.key)
		if msg.err != nil {
			a.picErr[msg.key] = msg.err.Error()
			return a, nil
		}
		a.pics[msg.key] = msg.pic
	case sharedMsg:
		if msg.err != nil {
			return a, a.fail("Could not share", msg.err)
		}
		a.say("Link copied — it works until " + msg.share.ExpiresAt.Local().Format("2 Jan 15:04"))
		return a, tea.SetClipboard(msg.share.URL)
	case signOutMsg:
		if msg.err != nil {
			return a, a.fail("Could not sign out", msg.err)
		}
		a.farewell = "Signed out."
		return a, tea.Quit
	case usageMsg:
		if msg.err != nil {
			return a, a.fail("Could not load usage", msg.err)
		}
		a.usage, a.workspace, a.balance = msg.report, msg.workspace, msg.balance
	case clipboardMsg:
		if msg.err != nil {
			a.note = msg.err.Error()
			return a, nil
		}
		before := a.draft()
		a.attachImage("pasted.png", msg.data)
		return a, a.drafting(before)
	case latestMsg:
		if msg.err == nil { // offline: ask again next time
			a.setPrefs(func(p *prefs.Prefs) { p.Latest, p.LatestCheckedAt = msg.tag, a.now })
		}
	case mastMsg:
		a.mastBusy = false
		a.mast, a.mastKey = msg.p, msg.key
	case doneMsg:
		var cmds []tea.Cmd
		if msg.err != nil {
			return a, a.fail(msg.say, msg.err)
		}
		if msg.say != "" {
			a.say(msg.say)
		}
		if msg.session != nil && a.conv != nil && a.conv.id == msg.session.SessionID {
			a.conv.session = *msg.session
			a.conv.changed()
			a.replaceSession(*msg.session)
		}
		if msg.relist {
			cmds = append(cmds, a.list())
		}
		if msg.resync {
			cmds = append(cmds, a.sync())
		}
		if msg.open != "" {
			a.openSession(msg.open)
			cmds = append(cmds, a.sync())
		}
		if msg.account {
			cmds = append(cmds, a.loadAccount())
		}
		return a, tea.Batch(cmds...)
	case projectMadeMsg:
		a.projects = append(a.projects, *msg.p)
		a.showProject(msg.p.ProjectID)
		a.say("Made " + msg.p.Name + ": set its agent and repositories in the menu")
		return a, a.loadAccount()
	case envMsg:
		if msg.err != nil {
			delete(a.envs, msg.key) // read it afresh
			return a, a.fail("Could not change the environment", msg.err)
		}
		a.envs[msg.key] = msg.v
		if msg.say != "" {
			a.say(msg.say)
		}
		a.reloadMenu()
	case branchesMsg:
		if msg.err != nil {
			return a, a.fail("Could not list the branches", msg.err)
		}
		a.branches[msg.key] = msg.b
		a.reloadMenu()
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return a, a.mouse(msg.(tea.MouseMsg))
	case tea.MouseWheelMsg:
		a.span = nil
		if a.screen == screenChat && a.menu == nil {
			if msg.Button == tea.MouseWheelUp {
				return a, a.scrollBy(3)
			}
			return a, a.scrollBy(-3)
		}
	case tea.PasteMsg:
		before := a.draft()
		switch {
		case a.menu != nil:
			if a.menu.edit != nil {
				a.menu.edit.Insert(msg.Content)
			} else {
				a.menu.q.Insert(msg.Content)
			}
		case a.start != nil:
			a.start.task.Insert(msg.Content)
		case a.filter != nil:
			a.filter.Insert(msg.Content)
		case a.screen == screenChat:
			a.paste(msg.Content)
		}
		return a, a.drafting(before)
	case tea.KeyPressMsg:
		a.span = nil
		before := a.draft()
		cmd := a.key(msg)
		return a, tea.Batch(cmd, a.drafting(before))
	}
	return a, nil
}

// draft is what the open session's composer holds: its text and attachments.
func (a *app) draft() string {
	if a.conv == nil {
		return ""
	}
	d := a.conv.id + "\x00" + a.comp.Value()
	for _, at := range a.attach {
		d += "\x00" + at.name
	}
	return d
}

// drafting reports activity when a key or a paste changed the open
// session's draft: writing a message keeps its computer awake, as typing in
// its terminal does and as on the web. At most once a minute; an asleep
// computer stays asleep until the message is sent.
func (a *app) drafting(before string) tea.Cmd {
	c := a.conv
	if c == nil || a.screen != screenChat || a.draft() == before || a.comp.Value() == "" && len(a.attach) == 0 {
		return nil
	}
	s := c.session
	if s.MachineStatus != wire.MachineStatusHydrated || !s.ArchivedAt.IsZero() || a.now.Sub(a.activityAt[s.SessionID]) < time.Minute {
		return nil
	}
	a.activityAt[s.SessionID] = a.now
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = a.api.Activity(ctx, s.SessionID) // a missed report costs at most a nap
		return nil
	}
}

// synced takes a sync page in: the conversation, its session on the list,
// what you have now seen, and its pictures.
func (a *app) synced(msg syncMsg) tea.Cmd {
	c := a.conv
	if c == nil || c.id != msg.id {
		return nil
	}
	c.syncing = false
	if msg.err != nil {
		if api.IsCode(msg.err, "not_found") || isStatus(msg.err, 404) {
			a.conv, a.screen, a.menu = nil, screenHome, nil
			return tea.Batch(a.fail("That session is gone", msg.err), a.list())
		}
		return a.fail("Could not sync the session", msg.err)
	}
	beforeT, beforeE := c.afterTranscript, c.afterEvent
	more := c.applySync(msg.data)
	advanced := c.afterTranscript != beforeT || c.afterEvent != beforeE
	if more && advanced || c.resync {
		c.resync = false
		c.syncing = true
		id, after, event := c.id, c.afterTranscript, c.afterEvent
		next := call(func(ctx context.Context) (*api.Sync, error) { return a.api.Sync(ctx, id, false, after, event) },
			func(d *api.Sync, err error) tea.Msg { return syncMsg{id, d, err} })
		a.replaceSession(c.session)
		a.markReviewed()
		return tea.Batch(next, a.fetchPictures())
	}
	a.replaceSession(c.session)
	a.markReviewed()
	var cmds []tea.Cmd
	if _, ok := a.sessionRepos[c.id]; !ok {
		cmds = append(cmds, a.loadSessionRepos(c.id))
	}
	if a.cancelRequestedOn != "" && a.cancelRequestedOn != c.session.CurrentTurnID {
		a.cancelRequestedOn = ""
	}
	return tea.Batch(append(cmds, a.fetchPictures())...)
}

func isStatus(err error, status int) bool {
	var e *api.Error
	return errors.As(err, &e) && e.Status == status
}

// setSessions takes a fresh list. The open session keeps whichever copy is
// newer: the list and a sync can pass each other.
func (a *app) setSessions(list []wire.Session) {
	var keep, ended, archived []wire.Session
	for _, s := range list {
		if s.MachineStatus == wire.MachineStatusDeleting {
			continue
		}
		if !s.ArchivedAt.IsZero() {
			archived = append(archived, s)
			continue
		}
		if a.conv != nil && s.SessionID == a.conv.id && a.conv.session.UpdatedAt.After(s.UpdatedAt) {
			s = a.conv.session
		}
		if prev, ok := a.session(s.SessionID); ok && a.endedUnseen(prev, s) {
			ended = append(ended, s)
		}
		keep = append(keep, s)
	}
	a.sessions, a.archived = keep, archived
	a.prefs.Reviewed = prefs.Load().Reviewed // the commands may have marked some read
	a.announce(ended)
	if !a.listed {
		a.listed = true
		if a.prefs.Reviewed == nil { // a first run has read everything so far, as the web app assumes
			a.markRead(keep)
		}
		if o := a.order(); len(o) > 0 && a.sel == "" {
			a.sel = o[0].SessionID
		}
	}
}

func (a *app) replaceSession(s wire.Session) {
	for i := range a.sessions {
		if a.sessions[i].SessionID == s.SessionID {
			if a.endedUnseen(a.sessions[i], s) {
				a.announce([]wire.Session{s})
			}
			a.sessions[i] = s
			return
		}
	}
	if s.ArchivedAt.IsZero() {
		a.sessions = append(a.sessions, s)
	}
}

// endedUnseen reports whether a session's turn ended between two copies
// of it, out of your sight: not the conversation on screen.
func (a *app) endedUnseen(prev, next wire.Session) bool {
	watching := a.screen == screenChat && a.conv != nil && a.conv.id == next.SessionID
	return a.listed && !watching && next.LatestTerminalTurnID != "" && next.LatestTerminalTurnID != prev.LatestTerminalTurnID
}

// announce says briefly, in the footer, that sessions finished a turn, and
// unless you silenced it, with a desktop notification (OSC 9).
func (a *app) announce(ended []wire.Session) {
	var text string
	switch len(ended) {
	case 0:
		return
	case 1:
		s := ended[0]
		word := map[wire.TurnStatus]string{wire.TurnStatusFailed: " failed", wire.TurnStatusCancelled: " stopped"}[s.LatestTerminalTurnStatus]
		if word == "" {
			word = " is done"
		}
		text = sessionName(s) + word
	default:
		text = fmt.Sprintf("%d sessions finished", len(ended))
	}
	a.say(text)
	a.toastBad = len(ended) == 1 && ended[0].LatestTerminalTurnStatus == wire.TurnStatusFailed
	if !a.prefs.Silent {
		a.notice = "Barad: " + text
	}
}

func (a *app) session(id string) (wire.Session, bool) {
	for _, s := range append(a.sessions, a.archived...) {
		if s.SessionID == id {
			return s, true
		}
	}
	return wire.Session{}, false
}

// markReviewed records that you have seen the open session's newest turn.
func (a *app) markReviewed() {
	if a.screen == screenHome || a.conv == nil {
		return
	}
	s := a.conv.session
	if s.LatestTerminalTurnID != "" && a.prefs.Reviewed[s.SessionID] != s.LatestTerminalTurnID {
		a.markRead([]wire.Session{s})
	}
}

func (a *app) openSession(id string) {
	s, ok := a.session(id)
	if !ok {
		s = wire.Session{SessionID: id}
	}
	if a.conv == nil || a.conv.id != id {
		a.conv = newConversation(s)
		a.comp.Reset()
		a.attach, a.note = nil, ""
	}
	a.screen, a.scroll, a.sel = screenChat, 0, id
	a.markReviewed()
}

func (a *app) say(s string) { a.toast, a.toastBad, a.toastAt = s, false, a.now }

// fail reports an error in the footer, or quits when you are signed out.
func (a *app) fail(what string, err error) tea.Cmd {
	if errors.Is(err, api.ErrSignedOut) {
		return a.signedOut()
	}
	a.toast, a.toastBad, a.toastAt = what+": "+err.Error(), true, a.now
	return nil
}

func (a *app) signedOut() tea.Cmd {
	a.exit = errors.New("you are signed out; run `barad login`")
	return tea.Quit
}

// ---------------------------------------------------------------- derived state

var groupLabel = map[api.Group]string{api.NeedsYou: "NEEDS YOU", api.Running: "RUNNING", api.Resting: "RESTING"}

// standing is a session's state on the list. A message you are sending
// holds it as working.
func (a *app) standing(s wire.Session) api.Standing {
	held := a.conv != nil && a.conv.id == s.SessionID && len(a.conv.sending)+len(a.conv.pending) > 0
	return api.StandingOf(s, a.prefs.Reviewed[s.SessionID], held)
}

// activityAt is when a session last did something you would notice.
func activityAt(s wire.Session) time.Time {
	t := s.CreatedAt
	if s.LatestTerminalTurnAt.After(t) {
		t = s.LatestTerminalTurnAt
	}
	if s.LatestUserPromptAt.After(t) {
		t = s.LatestUserPromptAt
	}
	return t
}

func projectOf(s wire.Session) string {
	if s.ProjectID == "" {
		return wire.DefaultProjectID
	}
	return s.ProjectID
}

// projectID is the project the home screen shows: the one you chose, or
// until you choose, the one you were last busy in.
func (a *app) projectID() string {
	want := a.prefs.Project
	if want == "" {
		var newest wire.Session
		for _, s := range a.sessions {
			if activityAt(s).After(activityAt(newest)) {
				newest = s
			}
		}
		want = projectOf(newest)
	}
	for _, p := range a.projects {
		if p.ProjectID == want {
			return p.ProjectID
		}
	}
	return wire.DefaultProjectID
}

func (a *app) projectName(id string) string {
	for _, p := range a.projects {
		if p.ProjectID == id {
			return p.Name
		}
	}
	return wire.DefaultProjectName
}

// order is the home list: the current project's sessions, what needs you
// first, then what is running, then the rest, each most recent first.
// order is the sessions the home screen lists: the project's, or with
// tab the archived ones. A search looks across every project, by name or ID.
func (a *app) order() []wire.Session {
	var out []wire.Session
	list, project, q := a.sessions, a.projectID(), ""
	if a.shelf {
		list = a.archived
	}
	if a.filter != nil {
		q = strings.TrimSpace(a.filter.Value())
	}
	for _, s := range list {
		if q == "" && projectOf(s) != project {
			continue
		}
		if q != "" && !Fuzzy(q, sessionName(s)) && !strings.HasPrefix(s.SessionID, q) {
			continue
		}
		out = append(out, s)
	}
	if a.shelf {
		sort.SliceStable(out, func(i, j int) bool { return out[i].ArchivedAt.After(out[j].ArchivedAt) })
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := a.standing(out[i]).Group, a.standing(out[j]).Group
		if gi != gj {
			return gi < gj
		}
		return activityAt(out[i]).After(activityAt(out[j]))
	})
	return out
}

// elsewhere counts the sessions in other projects that need you.
func (a *app) elsewhere() int {
	n, project := 0, a.projectID()
	for _, s := range a.sessions {
		if projectOf(s) != project && a.standing(s).Group == api.NeedsYou {
			n++
		}
	}
	return n
}

func (a *app) count(g api.Group) int {
	n, project := 0, a.projectID()
	for _, s := range a.sessions {
		if projectOf(s) == project && a.standing(s).Group == g {
			n++
		}
	}
	return n
}

// countAll counts the sessions of every project in a group.
func (a *app) countAll(g api.Group) int {
	n := 0
	for _, s := range a.sessions {
		if a.standing(s).Group == g {
			n++
		}
	}
	return n
}

func sessionName(s wire.Session) string {
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	return "Untitled session"
}

// ---------------------------------------------------------------- prefs

// setPrefs changes the preferences as they are on disk (the commands may
// have changed them since), and keeps the result.
func (a *app) setPrefs(change func(p *prefs.Prefs)) {
	next, err := prefs.Update(change)
	if err != nil {
		change(&a.prefs) // not saved, but in effect while the app runs
		return
	}
	if t := os.Getenv("BARAD_THEME"); t == "night" || t == "paper" {
		next.Theme = a.prefs.Theme
	}
	a.prefs = next
}

// markRead records that you have seen these sessions' newest turns.
func (a *app) markRead(list []wire.Session) {
	a.setPrefs(func(p *prefs.Prefs) { p.Read(list) })
}

// showProject makes the home screen list a project's sessions.
func (a *app) showProject(id string) {
	a.setPrefs(func(p *prefs.Prefs) { p.Project = id })
	a.sel = ""
}

func (a *app) cycleWork() {
	a.setPrefs(func(p *prefs.Prefs) { p.Work = (p.Work + 1) % 3 })
}

func (a *app) toggleTheme() {
	theme := map[string]string{"night": "paper", "paper": "night"}[a.prefs.Theme]
	a.setPrefs(func(p *prefs.Prefs) { p.Theme = theme })
	a.prefs.Theme = theme // even when BARAD_THEME chose the first
	applyTheme(a.prefs.Theme)
	clear(a.md)
}

// ---------------------------------------------------------------- program status (OSC 7501)

// report tells a terminal that shows program status (rex) what each session
// is doing, sending only what changed.
func (a *app) report() tea.Cmd {
	var b strings.Builder
	for _, s := range a.sessions {
		st := a.standing(s)
		r := ansi.ProgramStatus{ID: "barad/" + s.SessionID, App: "barad", Title: sessionName(s), State: ansi.ProgramStateIdle, Message: st.Word}
		switch st.Word {
		case "working", "waking":
			r.State = ansi.ProgramStateWorking
		case "failed":
			r.State = ansi.ProgramStateError
		case "ready":
			r.State = ansi.ProgramStateDone
		}
		seq := ansi.SetProgramStatus(r)
		if a.reported[s.SessionID] != seq {
			a.reported[s.SessionID] = seq
			b.WriteString(seq)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return tea.Raw(b.String())
}

// ---------------------------------------------------------------- the cover

// mastRows is how tall the landscape is, by the window's height.
func (a *app) mastRows() int {
	switch {
	case a.h >= 44:
		return 9
	case a.h >= 30:
		return 6
	}
	return 0
}

// drawMast starts drawing the landscape off the update loop when the window
// or theme has changed.
func (a *app) drawMast() tea.Cmd {
	key := fmt.Sprintf("%s/%dx%d", a.prefs.Theme, a.w, a.mastRows())
	if a.frame < 3 || a.mastRows() == 0 || key == a.mastKey || a.mastBusy {
		return nil
	}
	a.mastBusy = true
	theme, w, rows, g := a.prefs.Theme, a.w, a.mastRows(), a.gfx
	return func() tea.Msg {
		p := landscape(theme, w, rows, g)
		p.Lines(w, g) // lay it out (and queue its upload) here, not in View
		return mastMsg{key, p}
	}
}

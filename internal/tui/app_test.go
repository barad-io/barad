package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// driver runs the app the way Bubble Tea does, but in step: each message
// goes through Update, and the commands it returns run at once, their
// messages fed back, until nothing is left. Ticks are dropped (frames are
// sent by hand), so a run always ends.
type driver struct {
	t *testing.T
	a *app
	f *fakeAPI
}

func newDriver(t *testing.T) *driver {
	frameEvery = 0
	t.Setenv("BARAD_THEME", "night")
	f := newFakeAPI(0)
	t.Cleanup(f.srv.Close)
	d := &driver{t: t, a: newApp(f.signIn(t, t.TempDir()), GraphicsBlocks), f: f}
	d.send(tea.WindowSizeMsg{Width: 150, Height: 50})
	d.run(d.a.Init())
	return d
}

func (d *driver) send(msg tea.Msg) {
	_, cmd := d.a.Update(msg)
	d.run(cmd)
}

func (d *driver) run(cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 1000 {
			d.t.Fatal("the app did not settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tickMsg, nil:
		default:
			_, next := d.a.Update(msg)
			queue = append(queue, next)
		}
	}
}

// keys types each key: a name (enter, esc, tab…) or text.
func (d *driver) keys(keys ...string) {
	for _, k := range keys {
		switch k {
		case "enter":
			d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "esc":
			d.send(tea.KeyPressMsg{Code: tea.KeyEscape})
		case "tab":
			d.send(tea.KeyPressMsg{Code: tea.KeyTab})
		case "down":
			d.send(tea.KeyPressMsg{Code: tea.KeyDown})
		case "up":
			d.send(tea.KeyPressMsg{Code: tea.KeyUp})
		case "ctrl+u":
			d.send(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		default:
			for _, r := range k {
				d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		}
	}
}

func (d *driver) screen() string {
	d.send(tickMsg(time.Now()))
	return ansi.Strip(d.a.View().Content)
}

func (d *driver) sees(want ...string) {
	d.t.Helper()
	s := d.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			d.t.Fatalf("the screen does not show %q:\n%s", w, s)
		}
	}
}

func (d *driver) misses(gone ...string) {
	d.t.Helper()
	s := d.screen()
	for _, w := range gone {
		if strings.Contains(s, w) {
			d.t.Fatalf("the screen still shows %q:\n%s", w, s)
		}
	}
}

// reads checks the whole transcript, scrolled or not.
func (d *driver) reads(want ...string) {
	d.t.Helper()
	s := ansi.Strip(strings.Join(d.a.transcript(), "\n"))
	for _, w := range want {
		if !strings.Contains(s, w) {
			d.t.Fatalf("the transcript does not show %q:\n%s", w, s)
		}
	}
}

func (d *driver) fake(id string) wire.Session {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	return d.f.find(id).s
}

// openSession finds a session by searching for it.
func (d *driver) openSession(query string) {
	d.keys("/", query, "enter")
}

func TestHomeListsSessionsByWhatTheyNeed(t *testing.T) {
	d := newDriver(t)
	d.sees("Barad", "Acme", "NEEDS YOU", "Upgrade the payments SDK", "failed", "RUNNING", "Move the billing cron to the queue", "working",
		"RESTING", "Fix the flaky checkout test", "Profile the search API", "asleep")
	d.misses("Rotate the staging keys") // another project
	s := d.screen()
	if strings.Index(s, "Upgrade the payments SDK") > strings.Index(s, "Move the billing cron") || strings.Index(s, "Move the billing cron") > strings.Index(s, "Fix the flaky") {
		t.Fatalf("groups are out of order:\n%s", s)
	}
	// Another project, from the menu.
	d.keys("?", "project", "enter", "Ops", "enter")
	d.sees("Rotate the staging keys", "Ops")
	d.misses("Fix the flaky checkout test")
}

func TestConversationShowsWorkFoldedThenUnfolded(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.sees("ACME", "Fix the flaky checkout test", "✻ claude-opus-5", "high", "repo  acme/web", "context", "31%", "awake",
		"you", "The checkout test fails about one run in five", "claude", "worked for", "5 steps",
		"The test was flaky because Cart.Add updated total", "WHAT CHANGED", "func (c *Cart) Add(it Item) {")
	d.misses("go test ./checkout/ -race -count=20")

	d.keys("tab") // shown: a row per step
	d.reads("thought", "The failure is intermittent", "ran", "go test ./checkout/ -race -count=20", "FAIL: TestCartTotals",
		"edited", "/workspace/web/checkout/cart.go", "+1 −0", "captured", "ok    acme/checkout", "6 lines")
	if d.a.pics["shot/12"] == nil {
		t.Fatalf("the screenshot was not loaded: %v %v", d.a.picErr, d.f.calls)
	}

	d.keys("tab") // full output
	d.reads("WARNING: DATA RACE", "+   mu    sync.Mutex", "package checkout")

	d.keys("tab") // folded again
	d.misses("WARNING: DATA RACE")
	if d.a.prefs.Work != workFolded {
		t.Fatalf("work = %v", d.a.prefs.Work)
	}
}

func TestSendingAMessageShowsTheAgentsAnswer(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.keys("Add a regression test for the race", "enter")
	d.sees("Add a regression test for the race", "Done. I made the change and ran the tests; everything passes.")
	d.misses("sending")
	if n := len(d.a.conv.sending); n != 0 {
		t.Fatalf("%d messages are still sending", n)
	}
	if got := d.a.standing(d.a.conv.session).word; got != "idle" {
		t.Fatalf("an answer you are looking at is %q, want idle (read)", got)
	}
}

func TestStreamedTextShowsUntilItIsCommitted(t *testing.T) {
	c := newConversation(wire.Session{SessionID: "s"})
	fs := &fakeSession{s: wire.Session{SessionID: "s", CurrentTurnID: "t1"}, turns: map[string]wire.Turn{}}
	fs.user("hello", time.Now(), nil)
	fs.turn("t1", time.Now())
	fs.event("t1", "st", wire.EventKindModelDelta, map[string]any{"delta": "Hel"})
	fs.event("t1", "st", wire.EventKindModelDelta, map[string]any{"delta": "lo there"})
	c.applySync(&api.Sync{Session: fs.s, Items: fs.items, Events: fs.events, EventNextCursor: fs.ev, TranscriptNextCursor: fs.seq})
	exs := c.exchanges()
	last := exs[len(exs)-1].parts
	if p := last[len(last)-1]; p.kind != partText || !p.live || p.text != "Hello there" {
		t.Fatalf("live part = %+v", p)
	}
	fs.reply("t1", "Hello there.", time.Now())
	fs.items[len(fs.items)-1].StreamID = "st"
	fs.finish("t1", wire.TurnStatusCompleted, nil, time.Now())
	c.applySync(&api.Sync{Session: fs.s, Items: fs.items[1:], EventNextCursor: fs.ev, TranscriptNextCursor: fs.seq})
	if c.liveText != "" {
		t.Fatalf("live text %q survived its commit", c.liveText)
	}
	parts := c.exchanges()[0].parts
	if len(parts) != 1 || parts[0].text != "Hello there." || parts[0].live {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestExchangesPairCallsWithResultsAndEndTurns(t *testing.T) {
	fs := &fakeSession{s: wire.Session{SessionID: "s"}, turns: map[string]wire.Turn{}}
	now := time.Now()
	fs.user("one", now, nil)
	fs.turn("t1", now)
	fs.tool("t1", "bash", map[string]any{"command": "make"}, map[string]any{"exit_code": 2, "output": "boom"}, now, time.Second)
	fs.finish("t1", wire.TurnStatusFailed, &wire.ErrorInfo{Message: "it broke"}, now)
	fs.user("two", now, nil)
	fs.turn("t2", now)
	fs.reply("t2", "ok", now)
	fs.finish("t2", wire.TurnStatusCancelled, nil, now)
	c := newConversation(fs.s)
	c.applySync(&api.Sync{Session: fs.s, Items: fs.items, Turns: []wire.Turn{fs.turns["t1"], fs.turns["t2"]}})
	exs := c.exchanges()
	if len(exs) != 2 || exs[0].user.text != "one" || exs[1].user.text != "two" {
		t.Fatalf("exchanges = %+v", exs)
	}
	run := exs[0].parts[0]
	if run.kind != partRun || len(run.steps) != 1 || run.steps[0].result == nil {
		t.Fatalf("run = %+v", run)
	}
	if got := readResult(run.steps[0].result).failure("bash"); got != "Exited with status 2." {
		t.Fatalf("failure = %q", got)
	}
	if exs[0].parts[1].kind != partFailure || exs[1].parts[1].kind != partCancelled {
		t.Fatalf("turn ends = %+v / %+v", exs[0].parts, exs[1].parts)
	}
}

func TestMenuRenamesChangesModelAndArchives(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.keys("?")
	d.sees("Model", "claude-opus-5", "Reasoning effort", "Rename", "Share a read-only link", "Archive")

	d.keys("rename", "enter", "ctrl+u", "Checkout race", "enter")
	if got := d.fake("ses_checkout").Name; got != "Checkout race" {
		t.Fatalf("name = %q", got)
	}
	d.sees("Checkout race", "Renamed")

	d.keys("?", "model", "enter")
	d.sees("claude-sonnet-5")
	d.keys("sonnet", "enter")
	if got := d.fake("ses_checkout"); got.Model != "claude-sonnet-5" || got.ReasoningEffort != "high" {
		t.Fatalf("model = %q, effort %q", got.Model, got.ReasoningEffort)
	}
	d.sees("✻ claude-sonnet-5  ·  high")

	d.keys("?", "archive", "enter")
	d.sees("Archive?", "erases this session's computer")
	d.keys("enter")
	if d.fake("ses_checkout").ArchivedAt.IsZero() {
		t.Fatal("the session was not archived")
	}
	d.sees("NEEDS YOU", "Archived")
	d.misses("Checkout race")
}

func TestNewSessionStartsWithTheProjectsDefaults(t *testing.T) {
	d := newDriver(t)
	d.keys("n")
	d.sees("New session", "AGENT", "claude-opus-5", "Claude Max", "EFFORT", "high", "REPOSITORY", "acme/web", "PROJECT", "Acme")
	d.keys("tab", "tab", "tab", "enter") // the repository picker
	d.sees("None — a blank workspace", "acme/infra")
	d.keys("infra", "enter")
	d.sees("acme/infra")
	d.keys("tab", "tab", "Write a changelog", "enter") // around to the task
	d.sees("Write a changelog", "Done. I made the change")
	s := d.a.conv.session
	if s.Model != "claude-opus-5" || s.ReasoningEffort != "high" || s.ProjectID != "proj_acme" {
		t.Fatalf("session = %+v", s)
	}
	if repos := d.f.find(s.SessionID).repos; len(repos) != 1 || repos[0] != "acme/infra" {
		t.Fatalf("repositories = %v", repos)
	}
}

func TestApprovingAProposalOpensTheNewSession(t *testing.T) {
	d := newDriver(t)
	d.openSession("onboarding")
	d.sees("proposes a new session", "Write the deploy guide", "Approve or dismiss it from the menu", "asks you")
	d.keys("?", "approve", "enter")
	d.sees("Write the deploy guide", "Approved")
	if d.a.conv.id == "ses_guide" {
		t.Fatal("the new session did not open")
	}
	d.keys("esc", "/", "onboarding", "enter")
	d.sees("Approved — the new session started")
}

func TestAttachingAnImageUploadsItWithTheMessage(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	path := filepath.Join(t.TempDir(), "mock.png")
	if err := os.WriteFile(path, picture(320, 200, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	d.send(tea.PasteMsg{Content: path})
	d.sees("▣ mock.png", "320×200", "it uploads when you send")
	d.keys("Does it look like this?", "enter")
	d.f.mu.Lock()
	stored := len(d.f.media)
	d.f.mu.Unlock()
	if stored != 1 {
		t.Fatalf("%d images uploaded", stored)
	}
	u := d.a.conv.exchanges()[1].user
	if len(u.media) != 1 || d.a.pics[mediaRef(u.media[0]).key] == nil {
		t.Fatalf("the sent image is not shown: %+v %v", u, d.a.picErr)
	}
}

func TestUsageShowsTheMonth(t *testing.T) {
	d := newDriver(t)
	d.keys("?", "usage", "enter")
	d.sees("Usage", "last 30 days", "balance $8.20", "TOKENS", "72.0M", "60% cached", "WORKSPACE TIME", "41h 12m", "API COST", "DAILY SPEND", "today")
	d.keys("esc")
	d.sees("NEEDS YOU")
}

func TestStoppingTheAgent(t *testing.T) {
	d := newDriver(t)
	d.openSession("billing cron")
	d.sees("working", "running", "go test ./billing/...", "ctrl+x stop")
	d.send(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if got := d.fake("ses_cron").CurrentTurnID; got != "" {
		t.Fatalf("the turn %q is still running", got)
	}
	d.sees("cancelled after")
}

func TestLongPastesGoAsATextAttachment(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	long := strings.Repeat("a log line that goes on\n", 4000) // about 96 KB
	d.send(tea.PasteMsg{Content: long})
	d.sees("▤ pasted-text.txt", "it uploads when you send")
	if d.a.comp.Value() != "" {
		t.Fatal("the paste went into the message too")
	}
	d.keys("Why does this fail?", "enter")
	d.f.mu.Lock()
	var got []byte
	for _, data := range d.f.media {
		got = data
	}
	d.f.mu.Unlock()
	if string(got) != long {
		t.Fatalf("uploaded %d bytes, want %d", len(got), len(long))
	}
	d.reads("Why does this fail?", "▤ pasted-text.txt")
}

func TestEveryScreenFitsSmallWindows(t *testing.T) {
	d := newDriver(t)
	for _, size := range [][2]int{{20, 8}, {60, 16}, {80, 24}, {240, 70}} {
		d.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		d.keys("esc", "esc")
		d.screen()
		d.keys("n")
		d.screen()
		d.keys("esc", "/", "checkout", "enter", "tab", "tab")
		d.screen()
		d.keys("tab", "?")
		d.screen()
		d.keys("esc", "esc", "?", "usage", "enter")
		d.screen()
		d.keys("esc")
	}
}

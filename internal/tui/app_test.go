package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/fakeapi"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/internal/update"
	"github.com/barad-io/barad/wire"
)

// driver runs the app the way Bubble Tea does, but in step: each message
// goes through Update, and the commands it returns run at once, their
// messages fed back, until nothing is left. Ticks are dropped (frames are
// sent by hand), so a run always ends.
type driver struct {
	t *testing.T
	a *app
	f *fakeapi.API
}

func newDriver(t *testing.T) *driver {
	frameEvery, pollEvery = 0, time.Millisecond
	open := OpenURL
	OpenURL = func(string) error { return nil }
	t.Cleanup(func() { OpenURL = open })
	t.Setenv("BARAD_THEME", "night")
	f := fakeapi.New(0)
	t.Cleanup(f.Srv.Close)
	d := &driver{t: t, a: newApp(f.SignIn(t, t.TempDir()), GraphicsBlocks), f: f}
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
	lines, _ := d.a.transcript()
	s := ansi.Strip(strings.Join(lines, "\n"))
	for _, w := range want {
		if !strings.Contains(s, w) {
			d.t.Fatalf("the transcript does not show %q:\n%s", w, s)
		}
	}
}

func (d *driver) fake(id string) wire.Session {
	d.f.Mu.Lock()
	defer d.f.Mu.Unlock()
	return d.f.Find(id).S
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
	// Another project, with p.
	d.keys("p")
	d.sees("Projects", "SHOW A PROJECT", "1 need you", "New project", "PROJECT  ·  Acme")
	d.keys("Ops", "enter")
	d.sees("Rotate the staging keys", "Ops")
	d.misses("Fix the flaky checkout test")
}

func TestConversationShowsWorkFoldedThenUnfolded(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.sees("ACME", "Fix the flaky checkout test", "✻ claude-opus-5", "high", "repo  acme/web", "context", "31%", "awake",
		"you", "The checkout test fails about one run in five", "claude", "worked for", "5 steps",
		"The test was flaky because `Cart.Add` updated `total`", "WHAT CHANGED", "func (c *Cart) Add(it Item) {")
	d.misses("go test ./checkout/ -race -count=20")

	d.keys("tab") // shown: a row per step
	d.reads("thought", "The failure is intermittent", "ran", "go test ./checkout/ -race -count=20", "FAIL: TestCartTotals",
		"edited", "/workspace/web/checkout/cart.go", "+1 −0", "captured", "ok    acme/checkout", "6 lines")
	if d.a.pics["shot/12"] == nil {
		t.Fatalf("the screenshot was not loaded: %v %v", d.a.picErr, d.f.Calls)
	}

	d.keys("tab") // full output
	d.reads("WARNING: DATA RACE", "+   mu    sync.Mutex", "package checkout")

	d.keys("tab") // folded again
	d.misses("WARNING: DATA RACE")
	if d.a.prefs.Work != prefs.WorkFolded {
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
	if got := d.a.standing(d.a.conv.session).Word; got != "idle" {
		t.Fatalf("an answer you are looking at is %q, want idle (read)", got)
	}
}

func TestStreamedTextShowsUntilItIsCommitted(t *testing.T) {
	c := newConversation(wire.Session{SessionID: "s"})
	fs := &fakeapi.Session{S: wire.Session{SessionID: "s", CurrentTurnID: "t1"}, Turns: map[string]wire.Turn{}}
	fs.User("hello", time.Now(), nil)
	fs.Turn("t1", time.Now())
	fs.Event("t1", "st", wire.EventKindModelDelta, map[string]any{"delta": "Hel"})
	fs.Event("t1", "st", wire.EventKindModelDelta, map[string]any{"delta": "lo there"})
	c.applySync(&api.Sync{Session: fs.S, Items: fs.Items, Events: fs.Events, EventNextCursor: fs.Ev, TranscriptNextCursor: fs.Seq})
	exs := c.exchanges()
	last := exs[len(exs)-1].parts
	if p := last[len(last)-1]; p.kind != partText || !p.live || p.text != "Hello there" {
		t.Fatalf("live part = %+v", p)
	}
	fs.Reply("t1", "Hello there.", time.Now())
	fs.Items[len(fs.Items)-1].StreamID = "st"
	fs.Finish("t1", wire.TurnStatusCompleted, nil, time.Now())
	c.applySync(&api.Sync{Session: fs.S, Items: fs.Items[1:], EventNextCursor: fs.Ev, TranscriptNextCursor: fs.Seq})
	if c.liveText != "" {
		t.Fatalf("live text %q survived its commit", c.liveText)
	}
	parts := c.exchanges()[0].parts
	if len(parts) != 1 || parts[0].text != "Hello there." || parts[0].live {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestExchangesPairCallsWithResultsAndEndTurns(t *testing.T) {
	fs := &fakeapi.Session{S: wire.Session{SessionID: "s"}, Turns: map[string]wire.Turn{}}
	now := time.Now()
	fs.User("one", now, nil)
	fs.Turn("t1", now)
	fs.Tool("t1", "bash", map[string]any{"command": "make"}, map[string]any{"exit_code": 2, "output": "boom"}, now, time.Second)
	fs.Finish("t1", wire.TurnStatusFailed, &wire.ErrorInfo{Message: "it broke"}, now)
	fs.User("two", now, nil)
	fs.Turn("t2", now)
	fs.Reply("t2", "ok", now)
	fs.Finish("t2", wire.TurnStatusCancelled, nil, now)
	c := newConversation(fs.S)
	c.applySync(&api.Sync{Session: fs.S, Items: fs.Items, Turns: []wire.Turn{fs.Turns["t1"], fs.Turns["t2"]}})
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
	if exs[0].parts[1].kind != partFailure || exs[0].parts[2].kind != partEnd || exs[1].parts[1].kind != partEnd || exs[1].parts[1].turn.Status != wire.TurnStatusCancelled {
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
	d.sees("Archive Checkout race?", "erases this session's workspace")
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
	d.sees("New session", "AGENT", "claude-opus-5", "Claude Max", "EFFORT", "high", "REPOSITORIES", "acme/web", "ENVIRONMENT", "none", "PROJECT", "Acme")

	// Repositories: add acme/infra, and clone acme/web at a branch.
	d.keys("tab", "tab", "tab", "enter")
	d.sees("Cloned once, when the workspace is made", "✓ acme/web", "main (default)", "acme/infra")
	d.keys("infra", "enter")
	d.sees("✓ acme/infra")
	d.keys("ctrl+u", "web", "enter") // open acme/web
	d.sees("BRANCH", "main", "default", "release", "spike/queue", "Leave it out")
	d.keys("release", "enter")
	d.sees("acme/web", "release")
	d.keys("esc")
	d.sees("acme/web @release, acme/infra")

	// Environment: a variable and a secret, the secret's value in dots.
	d.keys("tab", "enter")
	d.keys("variable", "enter", "MODE=test", "enter")
	d.sees("MODE")
	d.keys("enter", "secret", "enter", "TOKEN", "enter", "s3cr3t")
	d.sees("••••••")
	d.misses("s3cr3t")
	d.keys("enter")
	d.sees("MODE, TOKEN")

	d.keys("tab", "tab", "Write a changelog", "enter") // around to the task
	d.sees("Write a changelog", "Done. I made the change")
	s := d.a.conv.session
	if s.Model != "claude-opus-5" || s.ReasoningEffort != "high" || s.ProjectID != "proj_acme" {
		t.Fatalf("session = %+v", s)
	}
	created := d.f.Find(s.SessionID).Created
	repos, _ := json.Marshal(created["repositories"])
	env, _ := json.Marshal(created["environment"])
	if string(repos) != `[{"repository":"acme/web","requested_ref":"release","source_id":"src_1"},{"repository":"acme/infra","source_id":"src_1"}]` ||
		string(env) != `[{"name":"MODE","secret":false,"value":"test"},{"name":"TOKEN","secret":true,"value":"s3cr3t"}]` {
		t.Fatalf("started with %s and %s", repos, env)
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
	if err := os.WriteFile(path, fakeapi.Picture(320, 200, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	d.send(tea.PasteMsg{Content: path})
	d.sees("▣ mock.png", "320×200", "it uploads when you send")
	d.keys("Does it look like this?", "enter")
	d.f.Mu.Lock()
	stored := len(d.f.Media)
	d.f.Mu.Unlock()
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
	d.sees("Usage", "last 30 days", "credits  $8.20  left", "BARAD   workspace time, paid with your credits", "INFERENCE",
		"CREDITS LEFT", "RUNNING TIME", "41h 12m", "CREDITS USED", "$1.84", "WORKSPACE RUNS", "61", "CREDITS USED EACH DAY", "today", "Sleeping workspaces are free.")
	d.misses("72.0M")
	d.keys("tab")
	d.sees("INFERENCE   tokens, billed by your model providers", "PROCESSED TOKENS", "72.0M", "60% of input cached", "DIRECT API COST",
		"EQUIVALENT API VALUE", "TOKENS EACH DAY", "Barad does not charge for inference")
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
	d.f.Mu.Lock()
	var got []byte
	for _, data := range d.f.Media {
		got = data
	}
	d.f.Mu.Unlock()
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

func TestSignInWithADeviceCode(t *testing.T) {
	f := fakeapi.New(0)
	t.Cleanup(f.Srv.Close)
	client := f.SignIn(t, t.TempDir())
	if err := client.SignOut(context.Background()); err != nil || client.SignedIn() {
		t.Fatalf("sign out: %v, still signed in %v", err, client.SignedIn())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started, err := client.StartSignIn(ctx, "barad on test")
	if err != nil {
		t.Fatal(err)
	}
	if started.UserCode != "BCDF-GHJK" || !strings.HasSuffix(started.VerificationURL, "/?cli=BCDF-GHJK") {
		t.Fatalf("started = %+v", started)
	}
	if err := client.FinishSignIn(ctx, started); err != nil {
		t.Fatal(err)
	}
	if !client.SignedIn() || client.Email() != "you@example.com" || f.Polls != 2 {
		t.Fatalf("signed in %v as %q after %d polls", client.SignedIn(), client.Email(), f.Polls)
	}
	// The stored session is the one the app uses.
	if _, err := client.Sessions(ctx); err != nil {
		t.Fatal(err)
	}
	// A wrong device code is refused, not waited on.
	started.DeviceCode = "guess"
	if err := client.FinishSignIn(ctx, started); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("a wrong device code: %v", err)
	}
}

func TestTheComposerKeepsTheCursorInSightAndMovesByRows(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.keys("first line")
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	// The cursor stands at the start of the new, empty line.
	field := d.a.comp.Lines(d.a.composerWidth(), 8, SText, "", true)
	if len(field) != 2 || len(field[1]) < 2 || field[1][1].Text != " " || field[1][1].Style.GetBackground() != SCursor.GetBackground() {
		t.Fatalf("after shift+enter the field is %+v", field)
	}
	d.keys("  indented")
	if got := d.a.comp.Value(); got != "first line\n  indented" {
		t.Fatalf("value = %q", got)
	}
	d.sees("› first line", "  indented") // leading spaces are kept
	d.keys("up")
	if d.a.comp.Pos != len("first line") {
		t.Fatalf("up went to %d", d.a.comp.Pos)
	}
	d.keys("down")
	if d.a.comp.Pos != len("first line\n  indented") {
		t.Fatalf("down went to %d", d.a.comp.Pos)
	}
	d.send(tea.KeyPressMsg{Code: tea.KeyLeft})
	d.keys("X")
	if got := d.a.comp.Value(); got != "first line\n  indenteXd" {
		t.Fatalf("value = %q", got)
	}
}

func TestEditorRowsWrapLongLinesAndKeepEveryRune(t *testing.T) {
	e := &Editor{}
	e.Insert("aaaa bbbb cccc\n\ndd")
	rows, cur := e.rows(8) // rows of 7 cells
	var got []string
	for _, r := range rows {
		got = append(got, string(e.Text[r[0]:r[1]]))
	}
	if strings.Join(got, "|") != "aaaa |bbbb |cccc||dd" || cur != 4 {
		t.Fatalf("rows = %q, cursor row %d", got, cur)
	}
	e.Pos = 5 // the start of "bbbb": a wrapped row's end is the next row's start
	if _, cur := e.rows(8); cur != 1 {
		t.Fatalf("cursor row = %d", cur)
	}
	e.vertical(-1, 8)
	if e.Pos != 0 {
		t.Fatalf("up from the second row went to %d", e.Pos)
	}
	e.Pos = 13
	e.vertical(1, 8) // from "cccc" down to the empty line
	if e.Pos != 15 {
		t.Fatalf("down to the empty line went to %d", e.Pos)
	}
}

func TestTurnsSayWhenTheyEndedAndHowLongTheyTook(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	turn := d.a.conv.turns["turn_1"]
	when := clock(turn.CompletedAt, d.a.now)
	d.reads("✓  done  " + when + "  ·  took 6m 00s")
	d.sees("last active  " + when)
	d.misses("activity")
}

func TestFinishedSessionsAreAnnouncedBriefly(t *testing.T) {
	d := newDriver(t)
	d.a.online = true // the driver has no update socket
	d.openSession("checkout")
	d.sees("1 need you", "1 working") // the failed SDK upgrade; the cron move
	d.f.Mu.Lock()
	cron := d.f.Find("ses_cron")
	cron.Finish("turn_c1", wire.TurnStatusCompleted, nil, time.Now())
	d.f.Mu.Unlock()
	d.send(dirtyMsg(api.Dirty{Summaries: []string{"ses_cron"}}))
	d.sees("Move the billing cron to the queue is done") // in place of the counts, briefly
	d.a.toastAt = d.a.toastAt.Add(-4 * time.Second)
	d.sees("2 need you")
	d.misses("is done", "working")
}

func TestRexDrawsPicturesInHalfBlocks(t *testing.T) {
	t.Setenv("BARAD_GRAPHICS", "")
	t.Setenv("TERM", "xterm-ghostty")
	t.Setenv("TERM_PROGRAM", "rex")
	if DetectGraphics() != GraphicsBlocks {
		t.Fatal("Rex does not draw kitty placeholders, so it gets half blocks")
	}
	t.Setenv("TERM_PROGRAM", "ghostty")
	if DetectGraphics() != GraphicsKitty {
		t.Fatal("Ghostty gets kitty graphics")
	}
}

// at finds text on the screen: its first cell.
func (d *driver) at(text string) (x, y int) {
	d.t.Helper()
	for y, line := range strings.Split(d.screen(), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	d.t.Fatalf("%q is not on the screen", text)
	return 0, 0
}

func TestDraggingSelectsAndCopiesText(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	text := "The checkout test fails about one run in five"
	x, y := d.at(text)
	d.send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	d.send(tea.MouseMotionMsg{X: x + 10, Y: y, Button: tea.MouseLeft})
	_, cmd := d.a.Update(tea.MouseReleaseMsg{X: x + len(text) - 1, Y: y, Button: tea.MouseLeft})
	if cmd == nil || d.a.span == nil {
		t.Fatal("letting go of a drag copied nothing")
	}
	if got := selected(d.a.grid(d.a.compose()), *d.a.span); got != text {
		t.Fatalf("copied %q", got)
	}
	d.sees("Copied 45 characters")
	if !strings.Contains(d.a.View().Content, "\x1b[") {
		t.Fatal("the selection is not drawn")
	}
	d.keys("x") // a key clears it
	if d.a.span != nil {
		t.Fatal("the selection survived a key")
	}

	// Across rows, the copy loses the surfaces' edges and shared indent.
	x0, y0 := d.at("WHAT CHANGED")
	x1, y1 := d.at("The test runs 50 times")
	d.send(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	d.send(tea.MouseReleaseMsg{X: x1 + 200, Y: y1, Button: tea.MouseLeft})
	want := "WHAT CHANGED\n\n• `Cart` now holds a `sync.Mutex`, taken by `Add` and `Total`.\n• The test runs 50 times under `-race` without a failure."
	if got := selected(d.a.grid(d.a.compose()), *d.a.span); got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
}

func TestLinksInRepliesAndMessagesAreClickable(t *testing.T) {
	d := newDriver(t)
	d.openSession("search")
	x, y := d.at("pg_trgm")
	grid := d.a.grid(d.a.compose())
	if c := grid.CellAt(x, y); c == nil || c.Link.URL != "https://www.postgresql.org/docs/current/pgtrgm.html" {
		t.Fatalf("the link's label carries %+v", c)
	}
	d.keys("see https://example.com/a?b=1.", "enter")
	x, y = d.at("https://example.com/a?b=1")
	if c := d.a.grid(d.a.compose()).CellAt(x+3, y); c == nil || c.Link.URL != "https://example.com/a?b=1" {
		t.Fatalf("a bare address in your message carries %+v", c)
	}
	if seg := linkify("run javascript:alert(1) or file:///etc/passwd", SText); len(seg) != 1 {
		t.Fatalf("only web addresses link: %+v", seg)
	}
}

func TestCtrlPAndCtrlNJumpBetweenYourMessages(t *testing.T) {
	d := newDriver(t)
	d.f.Long(6)
	d.run(d.a.list())
	d.openSession("long session")
	d.screen()
	top := func() int {
		lines, _ := d.a.transcript()
		return max(0, len(lines)-d.a.chatRows()-d.a.scroll)
	}
	_, yours := d.a.transcript()
	if len(yours) != 6 {
		t.Fatalf("your messages begin at rows %v", yours)
	}
	ctrl := func(r rune) { d.send(tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}) }
	start := top()
	ctrl('p')
	if got := top(); got >= start || !slices.Contains(yours, got) {
		t.Fatalf("ctrl+p put row %d at the top (was %d; yours at %v)", got, start, yours)
	}
	ctrl('p')
	second := top()
	if !slices.Contains(yours, second) || d.a.scroll == 0 {
		t.Fatalf("a second ctrl+p put row %d at the top", second)
	}
	d.sees(fmt.Sprintf("Step %d:", slices.Index(yours, second)))
	ctrl('n')
	if got := top(); got <= second {
		t.Fatalf("ctrl+n went from %d to %d", second, got)
	}
	for range 6 {
		ctrl('n')
	}
	if d.a.scroll != 0 {
		t.Fatalf("past your last message ctrl+n leaves the view %d rows up", d.a.scroll)
	}
}

func TestHomeMentionsANewerRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/barad-io/barad/releases/tag/v0.5.0")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	prev := update.Releases
	update.Releases = srv.URL
	defer func() { update.Releases = prev }()

	d := newDriver(t)
	d.a.online = true  // the driver has no update socket
	d.misses("is out") // a development build never looks
	d.a.version = "v0.4.0"
	d.run(d.a.checkLatest())
	d.sees("v0.5.0 is out  ·  barad update")
	if d.a.checkLatest() != nil {
		t.Fatal("it looked again within a day")
	}
	d.a.version = "v0.5.0"
	d.misses("is out")
}

func TestTheMenuGroupsBySubjectAndChangesRepositories(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.keys("?")
	d.sees("THIS CONVERSATION", "Copy the last reply", "SESSION  ·  Fix the flaky checkout test", "Rename", "Repositories", "1",
		"Details", "Delete", "BARAD", "Usage", "Settings", "Back to sessions")

	// Repositories: acme/web is reached; give acme/api, take acme/web away.
	d.keys("repositories", "enter")
	d.sees("Gives the session's Git credentials access", "✓ acme/web", "acme/api")
	d.keys("acme/api", "enter")
	if got := d.f.Find("ses_checkout").Repos; !slices.Equal(got, []string{"acme/web", "acme/api"}) {
		t.Fatalf("repositories = %v", got)
	}
	d.sees("Gave access to acme/api", "repo  acme/web, acme/api")
	d.keys("?", "repositories", "enter", "acme/web", "enter")
	d.sees("Take away access", "its clone in the workspace stays")
	d.keys("enter")
	if got := d.f.Find("ses_checkout").Repos; !slices.Equal(got, []string{"acme/api"}) {
		t.Fatalf("repositories = %v", got)
	}

	// Details: the session's facts, and the instruction files its agent reads.
	d.keys("?", "details", "enter")
	d.sees("Session ID", "ses_checkout", "AGENT INSTRUCTIONS", "AGENTS.md", "2.0 KB", "1 more found, not read")
	d.keys("esc", "esc")

	// Project: the session moves.
	d.keys("?", "project", "enter")
	d.sees("Default", "Acme", "Ops")
	d.keys("ops", "enter")
	if got := d.f.Find("ses_checkout").S.ProjectID; got != "proj_ops" {
		t.Fatalf("project = %q", got)
	}
	d.sees("Moved to Ops")
}

func TestFastModeAndDeleteFromTheSessionMenu(t *testing.T) {
	d := newDriver(t)
	d.openSession("payments")
	d.keys("?", "fast", "enter")
	if !d.f.Find("ses_sdk").S.FastMode {
		t.Fatal("fast mode is off")
	}
	d.sees("Fast mode on")
	d.keys("?", "delete", "enter")
	d.sees("Delete?", "permanently removes the session")
	d.keys("enter")
	if d.f.Find("ses_sdk").S.MachineStatus != wire.MachineStatusDeleting {
		t.Fatal("the session was not deleted")
	}
	d.misses("Upgrade the payments SDK")
}

func TestSettingsListTheProvidersAndTheAccount(t *testing.T) {
	d := newDriver(t)
	d.keys("?", "settings", "enter")
	d.sees("THIS COMPUTER", "Theme", "night", "PROVIDERS", "Claude Max", "SOURCE CONTROL", "@you", "Git identity", "BILLING", "$8.20 left",
		"ACCOUNT", "Signed in as", "you@example.com", "Sign out")
	d.keys("theme", "enter")
	d.sees("paper")
}

// Every cell of a frame carries the theme's colours itself, so a terminal
// that keeps its own default background (Rex) still shows the paper theme.
func TestEveryCellIsPaintedInTheTheme(t *testing.T) {
	d := newDriver(t)
	d.a.toggleTheme()
	for _, open := range []string{"", "checkout"} {
		if open != "" {
			d.openSession(open)
		}
		d.screen()
		grid := d.a.grid(d.a.View().Content)
		for y := range d.a.h {
			for x := range d.a.w {
				if c := grid.CellAt(x, y); c != nil && c.Width > 0 && (c.Style.Bg == nil || c.Style.Fg == nil) {
					t.Fatalf("%q: the cell at %d,%d (%q) keeps the terminal's colours", open, x, y, c.Content)
				}
			}
		}
	}
}

func TestProjectsAreMadeAndSetUpWithP(t *testing.T) {
	d := newDriver(t)
	d.keys("p")
	d.sees("Projects", "Acme", "Ops", "New project", "PROJECT  ·  Acme", "Agent", "claude-opus-5", "Repositories", "Environment", "Delete")
	d.keys("new project", "enter", "Payments", "enter")
	d.sees("Made Payments", "No sessions here yet")
	if d.a.projectID() == "proj_acme" {
		t.Fatal("the new project is not shown")
	}

	// Its agent, effort and repositories.
	d.keys("p", "agent", "enter")
	d.sees("Claude Max", "OpenAI", "None: each session chooses")
	d.keys("openai", "enter", "enter")
	d.keys("p", "repositories", "enter")
	d.sees("New sessions in this project clone these")
	d.keys("api", "enter", "esc", "esc")
	p := d.f.Projects[len(d.f.Projects)-1]
	if p.Name != "Payments" || p.Agent == nil || p.Agent.Model != "gpt-5.5" || p.Agent.ReasoningEffort != "medium" || describeRepos(p.Repositories) != "acme/api" {
		t.Fatalf("project %+v", p)
	}

	// Its variables.
	d.keys("p", "environment", "enter", "a variable", "enter", "STAGE=prod", "enter")
	d.sees("Set STAGE")
	if env := d.f.Environments["projects/"+p.ProjectID]; env == nil || len(env.Variables) != 1 || env.Variables[0].Value != "prod" {
		t.Fatalf("environment %+v", env)
	}

	// Deleting it moves you back.
	d.keys("p", "delete", "enter")
	d.sees("Delete Payments?", "Its sessions move to Default")
	d.keys("enter")
	d.sees("Deleted Payments")
	if d.f.Projects[len(d.f.Projects)-1].ClosingAt.IsZero() {
		t.Fatal("the project was not deleted")
	}
}

func TestASessionsVariablesChangeFromItsMenu(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.keys("?", "environment", "enter")
	d.sees("Its agent's commands and terminal shells receive these", "A variable", "A secret")
	d.keys("secret", "enter", "API_KEY", "enter", "hunter2", "enter")
	d.sees("Set API_KEY")
	d.keys("?", "environment", "enter")
	d.sees("API_KEY", "secret")
	d.misses("hunter2")
	d.keys("api_key", "enter", "remove", "enter")
	d.sees("Remove API_KEY?")
	d.keys("enter")
	d.sees("Removed API_KEY")
	if env := d.f.Environments["sessions/ses_checkout"]; len(env.Variables) != 0 {
		t.Fatalf("environment %+v", env.Variables)
	}
}

func describeRepos(rs []wire.ProjectRepository) string {
	var names []string
	for _, r := range rs {
		names = append(names, r.Repository)
	}
	return strings.Join(names, ", ")
}

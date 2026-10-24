package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/wire"
)

func TestReadingAndTheArchive(t *testing.T) {
	d := newDriver(t)
	d.misses("mark 1 read")
	// r marks the selected session unread: it waits for you, and R reads it all.
	d.keys("/", "flaky", "enter", "esc")
	d.keys("r")
	d.sees("NEEDS YOU", "ready", "R mark 1 read")
	if p := prefs.Load(); p.Reviewed["ses_checkout"] != "" {
		t.Fatalf("not saved for the commands: %v", p.Reviewed)
	}
	d.keys("R")
	d.sees("Marked 1 read")
	d.misses("mark 1 read")

	// A search looks across projects, and says whose a session is.
	d.keys("/", "staging")
	d.sees("Rotate the staging keys", "Ops", "found in every project")
	d.keys("esc")

	// a archives the selected session, after asking; tab shows the archive.
	d.keys("/", "search api", "enter", "esc")
	d.keys("a")
	d.sees("Archive Profile the search API?")
	d.keys("enter")
	d.misses("Profile the search API")
	d.keys("tab")
	d.sees("Archived", "ARCHIVED", "Profile the search API", "archived")
	d.keys("enter")
	d.sees("Archived", "takes no messages")
	d.keys("h", "i", "enter")
	if n := len(d.f.Find("ses_search").Pending); n != 0 {
		t.Fatal("an archived session took a message")
	}
}

func TestYourMessagesOutline(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	d.send(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	d.sees("Your messages", "The checkout test fails about one run in five")
	d.keys("enter")
	if d.a.menu != nil {
		t.Fatal("the outline stayed open")
	}
}

func TestEnterStartsALineWhenAsked(t *testing.T) {
	d := newDriver(t)
	d.a.setPrefs(func(p *prefs.Prefs) { p.Newline = true })
	d.openSession("checkout")
	d.keys("one", "enter", "two")
	if got := d.a.comp.Value(); got != "one\\ntwo" && got != "one\ntwo" {
		t.Fatalf("composer %q", got)
	}
	d.sees("alt+enter send")
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	if d.a.comp.Value() != "" {
		t.Fatal("alt+enter did not send")
	}
}

func TestANotificationWhenASessionFinishes(t *testing.T) {
	d := newDriver(t)
	fs := d.f.Find("ses_cron")
	d.f.Mu.Lock()
	fs.Finish(fs.S.CurrentTurnID, wire.TurnStatusCompleted, nil, time.Now())
	d.f.Mu.Unlock()
	d.run(d.a.list())
	if !strings.Contains(d.a.notice, "Move the billing cron to the queue is done") {
		t.Fatalf("notice %q", d.a.notice)
	}
	d.a.setPrefs(func(p *prefs.Prefs) { p.Silent = true })
	d.a.notice = ""
	d.a.announce([]wire.Session{fs.S})
	if d.a.notice != "" {
		t.Fatal("a silenced app notified")
	}
}

func TestConnectingProviders(t *testing.T) {
	d := newDriver(t)
	d.keys("?", "settings", "enter", "connect a provider", "enter", "openai api", "enter")
	d.sees("Name the connection")
	d.keys("ctrl+u", "Work OpenAI", "enter", "sk-123", "enter")
	d.sees("Connected Work OpenAI")
	if !slices.ContainsFunc(d.f.Providers, func(p wire.ProviderView) bool { return p.Name == "Work OpenAI" && p.Ready }) {
		t.Fatalf("providers %+v", d.f.Providers)
	}
	// A subscription: the code to enter, then connected on its own.
	d.keys("?", "settings", "enter", "connect a provider", "enter", "chatgpt", "enter", "enter")
	d.sees("Connected ChatGPT subscription")
	// Claude: its page gives a code to paste here.
	d.keys("esc", "?", "settings", "enter", "connect a provider", "enter", "claude", "enter", "enter")
	d.sees("Paste the code Claude shows")
	d.keys("abc-123", "enter")
	d.sees("Connected Claude subscription")

	// One connection: who uses it, signing out, removing.
	d.keys("esc", "?", "settings", "enter", "claude max", "enter")
	d.sees("Claude subscription", "Used by", "3 sessions", "Sign out", "Remove")
	d.keys("sign out", "enter", "enter")
	if p := d.f.Providers[0]; p.Ready {
		t.Fatal("Claude Max is still signed in")
	}
}

func TestSourceControlAndIdentity(t *testing.T) {
	d := newDriver(t)
	d.keys("?", "settings", "enter", "github", "enter")
	d.sees("Signed in as @you", "acme", "connected", "Connect you")
	d.keys("connect you", "enter")
	d.sees("Connected you")
	if len(d.f.Sources) != 2 {
		t.Fatalf("sources %+v", d.f.Sources)
	}
	d.keys("?", "settings", "enter", "git identity", "enter")
	d.sees("Author You <you@example.com>")
	d.keys("barad is the author", "enter")
	d.sees("Author Barad")
	if !d.f.Git.BaradIsAuthor {
		t.Fatal("not saved")
	}
	d.keys("esc", "esc", "esc")
	d.keys("?", "settings", "enter", "acme", "enter")
	d.sees("Repositories", "3 repositories", "Disconnect")
}

func TestBuyingCreditsAndThePassword(t *testing.T) {
	d := newDriver(t)
	d.keys("?", "settings", "enter", "buy credits", "enter")
	d.sees("$8.20 left", "$25.00", "Another amount")
	d.keys("$25", "enter")
	d.sees("Paid: $33.20 of credits")
	if !slices.Equal(d.f.Bought, []int64{2500}) {
		t.Fatalf("bought %v", d.f.Bought)
	}

	d.keys("?", "settings", "enter", "change password", "enter", "wrong", "enter", "new one", "enter", "new one", "enter")
	d.sees("Could not change the password")
	d.keys("?", "settings", "enter", "change password", "enter", "correct horse", "enter", "battery staple", "enter", "battery staple", "enter")
	d.sees("Changed your password")
	if d.f.Password != "battery staple" {
		t.Fatal("the password did not change")
	}
}

func TestUsageRanges(t *testing.T) {
	d := newDriver(t)
	d.keys("?", "usage", "enter")
	d.sees("last 30 days", "BY SESSION", "Fix the flaky checkout test", "BALANCE ACTIVITY")
	d.send(tea.KeyPressMsg{Code: tea.KeyRight})
	d.sees("last 90 days")
	d.keys("tab")
	d.sees("BY PROVIDER", "Claude Max", "BY MODEL", "claude-opus-5")
}

func TestSwitchingSessionsWithCtrlS(t *testing.T) {
	d := newDriver(t)
	d.openSession("checkout")
	ctrlS := tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	d.send(ctrlS)
	d.sees("Switch to a session", "NEEDS YOU", "Upgrade the payments SDK", "failed  ·  Acme", "RUNNING", "Rotate the staging keys", "Ops")
	if slices.ContainsFunc(d.a.menu.items, func(it item) bool { return it.label == "Fix the flaky checkout test" }) {
		t.Fatal("the open session is offered")
	}
	// Typing narrows it, across projects; Enter opens the one chosen.
	d.keys("stag")
	d.misses("Upgrade the payments SDK")
	d.keys("enter")
	if d.a.menu != nil || d.a.screen != screenChat || d.a.conv.id != "ses_other" {
		t.Fatalf("opened %v", d.a.conv)
	}
	// From home too; Esc leaves it.
	d.keys("esc")
	d.send(ctrlS)
	d.sees("Fix the flaky checkout test")
	d.keys("esc")
	if d.a.menu != nil {
		t.Fatal("esc did not close the switcher")
	}
}

func TestMovingASessionToAnotherProject(t *testing.T) {
	d := newDriver(t)
	d.keys("/", "flaky", "enter", "esc")
	d.keys("m")
	d.sees("Move Fix the flaky checkout test to", "Default", "Acme", "Ops")
	d.keys("ops", "enter")
	if got := d.f.Find("ses_checkout").S.ProjectID; got != "proj_ops" {
		t.Fatalf("project = %q", got)
	}
	d.sees("Moved to Ops")
	d.misses("Fix the flaky checkout test")
	// The menu says so too, by its key.
	d.keys("?")
	d.sees("Move to a project", "Projects: show, make, set up")
}

// The newest exchange sits right above the composer: one blank row between
// them, as between the bar and the transcript, not the space between
// exchanges.
func TestTheWorkingLineSitsAboveTheComposer(t *testing.T) {
	d := newDriver(t)
	d.openSession("cron")
	lines := strings.Split(d.screen(), "\n")
	working, composer := -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "working") && strings.Contains(l, "step"):
			working = i
		case strings.Contains(l, "› ") && composer < 0 && working >= 0:
			composer = i
		}
	}
	if working < 0 || composer < 0 {
		t.Fatalf("working at %d, composer at %d:\n%s", working, composer, strings.Join(lines, "\n"))
	}
	// the composer's block starts with a padding row above its text
	if gap := composer - working; gap > 3 {
		t.Fatalf("%d rows between the working line and the composer's text:\n%s", gap-1, strings.Join(lines[working:composer+1], "\n"))
	}
}

// Writing a message keeps an awake session awake: a change to the draft is
// reported, at most once a minute, and never wakes an asleep computer.
func TestWritingAMessageReportsActivity(t *testing.T) {
	d := newDriver(t)
	reported := func() []string {
		d.f.Mu.Lock()
		defer d.f.Mu.Unlock()
		return slices.Clone(d.f.Activity)
	}
	d.openSession("checkout") // awake
	d.keys("Look at")
	if got := reported(); !slices.Equal(got, []string{"ses_checkout"}) {
		t.Fatalf("reported %v", got)
	}
	d.keys("left", "up") // moving the cursor is not writing
	d.a.now = d.a.now.Add(61 * time.Second)
	d.keys("left")
	if got := reported(); len(got) != 1 {
		t.Fatalf("a key that changed nothing reported: %v", got)
	}
	d.send(tea.PasteMsg{Content: " the logs"})
	if got := reported(); len(got) != 2 {
		t.Fatalf("a paste after a minute was not reported: %v", got)
	}

	d.keys("esc")
	d.openSession("search api") // asleep
	d.keys("Why")
	if got := reported(); len(got) != 2 {
		t.Fatalf("typing to an asleep session reported: %v", got)
	}
}

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

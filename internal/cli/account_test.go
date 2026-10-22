package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/wire"
)

func TestReadUnreadAndArchived(t *testing.T) {
	b := newBarad(t)
	// The first look counts everything so far as read, as the app does.
	out := b.ok("sessions")
	contains(t, out, "Fix the flaky checkout test", "idle", "Upgrade the payments SDK", "failed")
	contains(t, b.ok("sessions", "--unread"), "Nothing unread.")

	contains(t, b.ok("sessions", "unread", "checkout"), "Marked Fix the flaky checkout test unread")
	out = b.ok("sessions", "--unread")
	contains(t, out, "Fix the flaky checkout test", "ready")
	if strings.Contains(out, "Upgrade the payments SDK") {
		t.Fatalf("a read session is listed as unread:\n%s", out)
	}
	if first := strings.Split(b.ok("sessions"), "\n")[1]; !strings.Contains(first, "Fix the flaky") && !strings.Contains(first, "payments SDK") {
		t.Fatalf("what needs you is not first: %q", first)
	}
	contains(t, b.ok("sessions", "read", "--all"), "Marked 1 session read")
	contains(t, b.ok("sessions", "--unread"), "Nothing unread.")
	if p := prefs.Load(); p.Reviewed["ses_checkout"] == "" {
		t.Fatalf("not recorded for the app: %v", p.Reviewed)
	}

	b.ok("sessions", "archive", "search", "--yes")
	contains(t, b.ok("sessions", "--archived"), "Profile the search API", "archived")
	contains(t, b.ok("sessions", "show", "search"), "archived")
	b.fails("is archived", "send", "search", "hello")
	b.fails("is archived", "sessions", "wake", "search")
}

func TestLogAndWaitingForAReply(t *testing.T) {
	b := newBarad(t)
	out := b.ok("sessions", "log", "checkout")
	contains(t, out, "You", "The checkout test fails about one run in five", "Agent", "Cart.Add")
	contains(t, b.ok("sessions", "log", "checkout", "--last"), "The test was flaky because")

	out = b.ok("send", "checkout", "Add", "a", "regression", "test", "--wait")
	contains(t, out, "Agent", "Done. I made the change and ran the tests")
	if strings.Contains(out, "The test was flaky") {
		t.Fatalf("--wait printed more than the new reply:\n%s", out)
	}
	// Piped input is the message.
	if _, err := b.run("What changed?\n", "send", "checkout"); err != nil {
		t.Fatal(err)
	}
	if s := b.f.Find("ses_checkout"); !strings.Contains(string(s.Items[len(s.Items)-1].Payload), "Done.") {
		t.Fatal("the piped message was not answered")
	}
	contains(t, b.ok("sessions", "new", "Write", "the", "changelog", "--project", "acme", "--wait"), "Done. I made the change")
	// A file goes with the message.
	note := filepath.Join(t.TempDir(), "notes.md")
	_ = os.WriteFile(note, []byte("# Notes\n"), 0o600)
	b.ok("send", "checkout", "Read this", "--file", note)
	b.fails("neither an image", "send", "checkout", "x", "--file", os.Args[0])
}

func TestProviderConnections(t *testing.T) {
	b := newBarad(t)
	contains(t, b.ok("providers"), "Claude Max", "Claude subscription", "ready", "OpenAI", "OpenAI API")
	contains(t, b.ok("providers", "show", "claude max"), "used by", "sessions", "Fix the flaky checkout test")

	if _, err := b.run("xai-key\n", "providers", "add", "xai_api", "--name", "Grok API"); err != nil {
		t.Fatal(err)
	}
	contains(t, b.ok("providers"), "Grok API", "xAI API")
	b.ok("providers", "rename", "grok api", "xAI")
	b.ok("providers", "logout", "xai", "--yes")
	contains(t, b.ok("providers", "show", "xai"), "signed out")
	if _, err := b.run("new-key\n", "providers", "login", "xai"); err != nil {
		t.Fatal(err)
	}
	contains(t, b.ok("providers", "show", "xai"), "ready")

	// A subscription signs in with a code in the browser.
	out := b.ok("providers", "add", "chatgpt")
	contains(t, out, "WXYZ-1234", "Connected ChatGPT subscription")
	if len(b.opened) == 0 || !strings.HasSuffix(b.opened[len(b.opened)-1], "/device") {
		t.Fatalf("opened %v", b.opened)
	}
	// Claude's page gives a code to paste back.
	out, err := b.run("claude-code-42\n", "providers", "add", "claude", "--name", "Claude Pro")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Paste the code", "Connected Claude Pro")
	if !slices.ContainsFunc(b.f.Providers, func(p wire.ProviderView) bool { return p.Name == "Claude Pro" && p.Ready }) {
		t.Fatalf("providers %+v", b.f.Providers)
	}
	b.fails("no provider type", "providers", "add", "gemini")
	b.fails("Run again with --yes", "providers", "remove", "claude pro")
	b.ok("providers", "remove", "claude pro", "--yes")
	if strings.Contains(b.ok("providers"), "Claude Pro") {
		t.Fatal("a removed connection is listed")
	}
}

func TestSourceControl(t *testing.T) {
	b := newBarad(t)
	contains(t, b.ok("git"), "signed in as you", "acme", "src_1", "not connected: barad git connect you", "authored by You <you@example.com>, committed by You")
	b.ok("git", "identity", "--separate", "on", "--barad-author", "on")
	contains(t, b.ok("git", "identity"), "authored by Barad, committed by You <you@example.com>")
	b.fails("is on or off", "git", "identity", "--separate", "yes")

	contains(t, b.ok("git", "connect", "you"), "Connected you")
	contains(t, b.ok("git", "repos", "acme"), "acme/web", "main", "acme/infra", "trunk")
	b.ok("git", "rename", "you", "Personal")
	contains(t, b.ok("git"), "Personal")
	b.ok("git", "remove", "personal", "--yes")

	b.ok("git", "logout", "--yes")
	contains(t, b.ok("git"), "you must sign in again")
	contains(t, b.ok("git", "login"), "Signed in to GitHub as you")
}

func TestBillingAndUsage(t *testing.T) {
	b := newBarad(t)
	contains(t, b.ok("billing"), "$8.20 left", "$0.20 an hour", "2026-07-01, accepted")
	contains(t, b.ok("billing", "history"), "credits bought", "$10.00", "workspace time", "-$0.40", "ses_checkout")
	b.fails("buy between $5.00 and $500.00", "billing", "buy", "2")

	b.f.Billing.TermsAccepted = false
	b.fails("Run again with --yes", "billing", "buy", "25")
	contains(t, b.ok("billing", "buy", "$25", "--accept-terms"), "Pay $25.00", "Paid: you have $33.20 of credits")
	if !slices.Equal(b.f.Bought, []int64{2500}) || !b.f.Billing.TermsAccepted {
		t.Fatalf("bought %v, terms %v", b.f.Bought, b.f.Billing.TermsAccepted)
	}

	contains(t, b.ok("usage"), "The last 30 days", "BARAD", "$1.84 of credits for 41h 12m", "61 runs", "INFERENCE", "72.0M tokens")
	contains(t, b.ok("usage", "sessions", "--days", "7"), "Fix the flaky checkout test", "25h 00m", "$1.00")
	contains(t, b.ok("usage", "providers"), "Claude Max", "OpenAI")
	contains(t, b.ok("usage", "models"), "claude-opus-5", "gpt-5.5")
	contains(t, b.ok("usage", "daily"), "DAY", "WORKSPACE TIME")
	b.fails("--days is 7, 30 or 90", "usage", "--days", "12")
}

func TestPreferences(t *testing.T) {
	b := newBarad(t)
	contains(t, b.ok("config"), "theme", "night", "enter", "send", "notify", "on", "steps", "folded")
	b.ok("config", "enter", "newline")
	b.ok("config", "notify", "off")
	b.ok("config", "steps", "full")
	b.ok("config", "theme", "paper")
	if p := prefs.Load(); !p.Newline || !p.Silent || p.Work != prefs.WorkFull || p.Theme != "paper" {
		t.Fatalf("prefs %+v", p)
	}
	contains(t, b.ok("config", "enter"), "newline")
	b.fails("enter is send or newline", "config", "enter", "maybe")
	b.fails("no preference", "config", "colour", "red")
}

func TestFilesInAWorkspace(t *testing.T) {
	b := newBarad(t)
	contains(t, b.ok("files", "checkout"), "notes.txt", "18", "web/")
	contains(t, b.ok("files", "checkout", "/workspace/web"), "README.md", "checkout/")
	contains(t, b.ok("files", "cat", "checkout", "web/README.md"), "The storefront.")
	b.fails("no such file", "files", "cat", "checkout", "nope.txt")

	here := t.TempDir()
	contains(t, b.ok("files", "get", "checkout", "notes.txt", "web/README.md", "--to", here), "notes.txt", "README.md")
	if data, _ := os.ReadFile(filepath.Join(here, "README.md")); string(data) != "# Web\n\nThe storefront.\n" {
		t.Fatalf("got %q", data)
	}

	plan := filepath.Join(here, "plan.md")
	_ = os.WriteFile(plan, []byte("ship it\n"), 0o600)
	contains(t, b.ok("files", "put", "checkout", plan, "--to", "web"), "Put 1 file in /workspace/web")
	contains(t, b.ok("files", "cat", "checkout", "web/plan.md"), "ship it")
	b.fails("exists", "files", "put", "checkout", plan, "--to", "web")
	b.ok("files", "put", "checkout", plan, "--to", "web", "--overwrite")
}

func TestAProposedSession(t *testing.T) {
	b := newBarad(t)
	contains(t, b.ok("sessions", "proposal", "guide"), "Write the deploy guide", "Deploys deserve their own page", "acme/web", "Write docs/deploy.md.", "--approve")
	contains(t, b.ok("sessions", "proposal", "guide", "--approve"), `Started "Write the deploy guide"`)
	b.fails("proposes nothing now", "sessions", "proposal", "onboarding")
}

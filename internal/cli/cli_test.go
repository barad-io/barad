package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/barad-io/barad/internal/fakeapi"
	"github.com/barad-io/barad/internal/tui"
	"github.com/barad-io/barad/wire"
)

// barad runs command lines against a fake Barad, from one computer: its
// files (what you have read, the preferences) last between commands.
type barad struct {
	t      *testing.T
	f      *fakeapi.API
	dir    string
	opened []string // pages it opened in the browser
}

func newBarad(t *testing.T) *barad {
	f := fakeapi.New(0)
	t.Cleanup(f.Srv.Close)
	b := &barad{t: t, f: f, dir: t.TempDir()}
	pollEvery = 5 * time.Millisecond
	open := tui.OpenURL
	tui.OpenURL = func(u string) error { b.opened = append(b.opened, u); return nil }
	t.Cleanup(func() { tui.OpenURL = open })
	return b
}

// run runs a command line with stdin, returning what it printed.
func (b *barad) run(stdin string, args ...string) (string, error) {
	b.t.Helper()
	client := b.f.SignIn(b.t, b.dir)
	var out bytes.Buffer
	err := Run(context.Background(), client, args, strings.NewReader(stdin), &out)
	return out.String(), err
}

func (b *barad) ok(args ...string) string {
	b.t.Helper()
	out, err := b.run("", args...)
	if err != nil {
		b.t.Fatalf("barad %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func (b *barad) fails(want string, args ...string) {
	b.t.Helper()
	if _, err := b.run("", args...); err == nil || !strings.Contains(err.Error(), want) {
		b.t.Fatalf("barad %s: %v, want an error saying %q", strings.Join(args, " "), err, want)
	}
}

func (b *barad) project(name string) wire.Project {
	b.t.Helper()
	for _, p := range b.f.Projects {
		if p.Name == name {
			return p
		}
	}
	b.t.Fatalf("no project %q", name)
	return wire.Project{}
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("the output does not show %q:\n%s", w, out)
		}
	}
}

func TestProjectsFromMakingToDeleting(t *testing.T) {
	b := newBarad(t)
	contains(t, b.ok("projects"), "NAME", "Acme", "Claude Max · claude-opus-5 · high", "acme/web", "Ops", "chosen per session")

	contains(t, b.ok("projects", "new", "Payments", "--provider", "openai", "--effort", "high", "--repo", "acme/api", "--repo", "acme/web"), "Made Payments")
	p := b.project("Payments")
	if p.Agent == nil || p.Agent.ProviderResourceID != "prov_openai" || p.Agent.Model != "gpt-5.5" || p.Agent.ReasoningEffort != "high" ||
		len(p.Repositories) != 2 || p.Repositories[0] != (wire.ProjectRepository{SourceID: "src_1", Repository: "acme/api"}) {
		t.Fatalf("made %+v", p)
	}
	b.fails("takes reasoning effort", "projects", "new", "X", "--provider", "openai", "--effort", "turbo")
	b.fails("none of your Git sources reaches", "projects", "new", "X", "--repo", "acme/secret")
	b.fails("--model and --effort need --provider", "projects", "new", "X", "--effort", "high")

	b.ok("projects", "edit", "payments", "--name", "Billing", "--remove-repo", "acme/web", "--add-repo", "acme/infra", "--effort", "low")
	p = b.project("Billing")
	if p.Agent.ReasoningEffort != "low" || p.Agent.Model != "gpt-5.5" || describeRepos(p.Repositories) != "acme/api, acme/infra" {
		t.Fatalf("edited %+v", p)
	}
	b.ok("projects", "edit", "billing", "--no-agent")
	if b.project("Billing").Agent != nil {
		t.Fatal("the agent stayed")
	}
	b.fails("could be any of", "projects", "show", "a") // Acme and Billing both have an a

	b.ok("env", "set", "STAGE=prod", "--project", "billing")
	contains(t, b.ok("projects", "show", "billing"), "Billing", "acme/api, acme/infra", "STAGE")

	b.fails("Run again with --yes", "projects", "delete", "billing")
	contains(t, b.ok("projects", "delete", "billing", "--yes"), "Deleted Billing")
	if out := b.ok("projects"); strings.Contains(out, "Billing") {
		t.Fatalf("a deleted project is listed:\n%s", out)
	}
	b.fails("cannot be deleted", "projects", "delete", "default", "--yes")
}

func TestVariablesForASessionAndAProject(t *testing.T) {
	b := newBarad(t)
	b.ok("env", "set", "REGION=eu-west-1", "--session", "checkout")
	if _, err := b.run("sk-live-123\n", "env", "set", "STRIPE_KEY", "--secret", "--session", "checkout"); err != nil {
		t.Fatal(err)
	}
	out := b.ok("env", "--session", "checkout")
	contains(t, out, "REGION", "eu-west-1", "STRIPE_KEY", "(secret)")
	if strings.Contains(out, "sk-live") {
		t.Fatal("a secret value was printed")
	}
	stored := b.f.Environments["sessions/ses_checkout"].Variables
	if i := slices.IndexFunc(stored, func(v wire.EnvironmentVariable) bool { return v.Name == "STRIPE_KEY" }); i < 0 || stored[i].Value != "sk-live-123" || !stored[i].Secret {
		t.Fatalf("stored %+v", stored)
	}
	b.ok("env", "unset", "REGION", "--session", "checkout")
	b.fails("has no variable", "env", "unset", "REGION", "--session", "checkout")
	b.fails("either --session or --project", "env", "--session", "checkout", "--project", "acme")
	b.fails("name the --session or the --project", "env", "list")
}

func TestStartingWorkingWithAndEndingSessions(t *testing.T) {
	b := newBarad(t)
	// From the project: its agent and repositories.
	b.ok("sessions", "new", "Add", "a", "queue", "--project", "acme", "--env", "MODE=test", "--secret-env", "TOKEN=x")
	s := b.f.List[len(b.f.List)-1]
	if s.S.Model != "claude-opus-5" || s.S.ReasoningEffort != "high" || !slices.Equal(s.Repos, []string{"acme/web"}) || s.S.ProjectID != "proj_acme" {
		t.Fatalf("started %+v with %v", s.S, s.Repos)
	}
	if env, _ := json.Marshal(s.Created["environment"]); string(env) != `[{"name":"MODE","secret":false,"value":"test"},{"name":"TOKEN","secret":true,"value":"x"}]` {
		t.Fatalf("environment %s", env)
	}
	if n := len(s.Items); n == 0 {
		t.Fatal("the task was not sent")
	}

	// Named outright, at a branch.
	out := b.ok("sessions", "new", "--provider", "openai", "--repo", "acme/api@release", "--repo", "acme/infra", "--json")
	var started wire.Session
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	s = b.f.List[len(b.f.List)-1]
	repos, _ := s.Created["repositories"].([]any)
	if started.Model != "gpt-5.5" || len(repos) != 2 || repos[0].(map[string]any)["requested_ref"] != "release" {
		t.Fatalf("started %+v from %+v", started, s.Created)
	}
	b.fails("has no agent", "sessions", "new", "--project", "ops")

	contains(t, b.ok("sessions"), "NAME", "Fix the flaky checkout test", "idle", "Acme", "Move the billing cron to the queue", "working")
	contains(t, b.ok("send", "checkout", "Add", "a", "regression", "test"), "Sent to Fix the flaky checkout test")
	b.ok("sessions", "edit", "payments", "--fast", "on", "--name", "SDK upgrade", "--project", "ops")
	sdk := b.f.Find("ses_sdk").S
	if !sdk.FastMode || sdk.Name != "SDK upgrade" || sdk.ProjectID != "proj_ops" {
		t.Fatalf("edited %+v", sdk)
	}
	contains(t, b.ok("sessions", "repos", "checkout", "--add", "acme/api", "--remove", "acme/web"), "acme/api")
	if got := b.f.Find("ses_checkout").Repos; !slices.Equal(got, []string{"acme/api"}) {
		t.Fatalf("repositories %v", got)
	}
	contains(t, b.ok("sessions", "show", "checkout"), "Fix the flaky checkout test", "ses_checkout", "repositories", "acme/api", "Acme")
	b.fails("is not working", "sessions", "stop", "checkout")
	contains(t, b.ok("sessions", "stop", "cron"), "Stopping")
	contains(t, b.ok("sessions", "share", "checkout"), "/s/abc")
	b.ok("sessions", "archive", "search", "--yes")
	b.ok("sessions", "delete", "guide", "--yes")
	if out := b.ok("sessions"); strings.Contains(out, "Profile the search API") || strings.Contains(out, "onboarding") {
		t.Fatalf("archived or deleted sessions are listed:\n%s", out)
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	var out bytes.Buffer
	if err := Help(&out, ""); err != nil {
		t.Fatal(err)
	}
	for _, v := range verbs {
		if !strings.Contains(out.String(), "barad "+v.cmd.usage) {
			t.Fatalf("help leaves out %s %s", v.noun, v.verb)
		}
	}
}

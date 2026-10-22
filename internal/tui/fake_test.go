package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// fakeAPI is a Barad API in memory, with the routes the app uses and an
// agent that answers messages with a run of work. It also answers Cognito,
// and approves a device-code sign-in at its second poll, so `barad login`
// works against it.
type fakeAPI struct {
	srv     *httptest.Server
	pace    time.Duration // between an agent's steps; 0 answers at once
	mu      sync.Mutex
	list    []*fakeSession
	media   map[string][]byte
	refs    map[string]wire.PromptMediaRef // reserved media, by id
	socks   map[chan api.Dirty]bool
	calls   []string // "METHOD path" of every API call, for assertions
	polls   int      // sign-in polls so far
	started wire.CLISignInStarted
}

type fakeSession struct {
	s         wire.Session
	items     []wire.TranscriptItem
	turns     map[string]wire.Turn
	events    []wire.Event
	pending   []api.PendingMessage
	proposals map[int64]wire.SessionProposalView
	repos     []string
	seq, ev   int64
}

func newFakeAPI(pace time.Duration) *fakeAPI {
	f := &fakeAPI{pace: pace, media: map[string][]byte{}, refs: map[string]wire.PromptMediaRef{}, socks: map[chan api.Dirty]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.seed()
	return f
}

// signIn points a client at the fake, signed in, with its files in dir.
func (f *fakeAPI) signIn(t testing.TB, dir string) *api.Client {
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("BARAD_API_URL", f.srv.URL)
	creds, _ := json.Marshal(api.Tokens{AccessToken: "fake", RefreshToken: "fake", ExpiresAt: time.Now().Add(24 * time.Hour), Email: "you@example.com",
		ClientID: "fake-cli", CognitoURL: f.srv.URL + "/cognito"})
	_ = os.MkdirAll(filepath.Join(dir, "barad"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "barad", "credentials.json"), creds, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := api.New()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ---------------------------------------------------------------- the account

var fakeStart = time.Now()

func (f *fakeAPI) seed() {
	ago := func(d time.Duration) time.Time { return fakeStart.Add(-d) }
	base := func(id, name, provider, mdl string, effort wire.ReasoningEffort, machine wire.MachineStatus, created time.Time) *fakeSession {
		return &fakeSession{s: wire.Session{SessionID: id, Name: name, ProviderResourceID: "prov_" + provider, Provider: wire.Provider(provider), Model: mdl,
			ReasoningEffort: effort, SupportsReasoningEffort: effort != "", SupportedReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high"},
			MachineStatus: machine, ContextWindowTokens: 200_000, AutoCompactTokenLimit: 160_000, ProjectID: "proj_acme", CreatedAt: created, UpdatedAt: created},
			turns: map[string]wire.Turn{}, proposals: map[int64]wire.SessionProposalView{}, repos: []string{"acme/web"}}
	}

	checkout := base("ses_checkout", "Fix the flaky checkout test", "claude", "claude-opus-5", "high", wire.MachineStatusHydrated, ago(3*time.Hour))
	checkout.user("The checkout test fails about one run in five on CI. Find out why and fix it.", ago(40*time.Minute), nil)
	t := checkout.turn("turn_1", ago(40*time.Minute))
	checkout.think(t, "The failure is intermittent, so it is likely a race or an ordering assumption. Run the test with -race and -count to reproduce it.", ago(39*time.Minute))
	checkout.tool(t, "bash", map[string]any{"command": "go test ./checkout/ -race -count=20"}, map[string]any{"exit_code": 1, "output": "--- FAIL: TestCartTotals (0.02s)\n    cart_test.go:41: total = 1998, want 2000\nWARNING: DATA RACE\nWrite at 0x00c0001a4018 by goroutine 9:\n  checkout.(*Cart).Add()\nFAIL\nFAIL\tacme/checkout\t1.204s"}, ago(39*time.Minute), 3*time.Second)
	checkout.tool(t, "read", map[string]any{"file_path": "/workspace/web/checkout/cart.go"}, map[string]any{"output": "package checkout\n\ntype Cart struct {\n\tItems []Item\n\ttotal int\n}\n"}, ago(38*time.Minute), 200*time.Millisecond)
	checkout.tool(t, "edit", map[string]any{"file_path": "/workspace/web/checkout/cart.go", "edits": []any{map[string]any{"old_text": "type Cart struct {\n\tItems []Item\n\ttotal int\n}", "new_text": "type Cart struct {\n\tmu    sync.Mutex\n\tItems []Item\n\ttotal int\n}"}}}, map[string]any{"output": "Edited cart.go"}, ago(37*time.Minute), 100*time.Millisecond)
	checkout.tool(t, "bash", map[string]any{"command": "go test ./checkout/ -race -count=50"}, map[string]any{"exit_code": 0, "output": "ok  \tacme/checkout\t3.911s"}, ago(36*time.Minute), 4*time.Second)
	checkout.tool(t, "screenshot", map[string]any{}, map[string]any{"screenshot": map[string]any{"width": 1440, "height": 900}}, ago(35*time.Minute), 900*time.Millisecond)
	checkout.reply(t, "The test was flaky because `Cart.Add` updated `total` from two goroutines without a lock.\n\n## What changed\n\n- `Cart` now holds a `sync.Mutex`, taken by `Add` and `Total`.\n- The test runs 50 times under `-race` without a failure.\n\n```go\nfunc (c *Cart) Add(it Item) {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\tc.Items = append(c.Items, it)\n\tc.total += it.Price\n}\n```\n\nThe checkout page still renders the same; the screenshot above is from the dev server.", ago(34*time.Minute))
	checkout.finish(t, wire.TurnStatusCompleted, nil, ago(34*time.Minute))
	checkout.s.LatestProviderUsage = wire.TokenUsage{ContextTokens: 61_000}

	sdk := base("ses_sdk", "Upgrade the payments SDK", "openai", "gpt-5.5", "medium", wire.MachineStatusHydrated, ago(5*time.Hour))
	sdk.user("Upgrade stripe-go to v86 and fix whatever breaks.", ago(2*time.Hour), nil)
	t = sdk.turn("turn_s1", ago(2*time.Hour))
	sdk.tool(t, "bash", map[string]any{"command": "go get github.com/stripe/stripe-go/v86 && go build ./..."}, map[string]any{"exit_code": 1, "output": "payments/charge.go:22:9: undefined: stripe.ChargeParams"}, ago(2*time.Hour), 6*time.Second)
	sdk.finish(t, wire.TurnStatusFailed, &wire.ErrorInfo{Code: "provider_error", Message: "The provider returned an error twice in a row: the request was too large for the model's context."}, ago(119*time.Minute))
	sdk.s.LatestProviderUsage = wire.TokenUsage{ContextTokens: 171_000}

	guide := base("ses_guide", "Write the onboarding guide", "claude", "claude-sonnet-5", "medium", wire.MachineStatusHydrated, ago(26*time.Hour))
	guide.user("Draft docs/onboarding.md from what new people ask in #help.", ago(25*time.Hour), nil)
	t = guide.turn("turn_g1", ago(25*time.Hour))
	guide.reply(t, "I drafted **docs/onboarding.md**. The deploy section is long enough to be its own guide, so I am proposing a separate session for it.", ago(25*time.Hour))
	guide.propose(t, wire.SessionProposal{Title: "Write the deploy guide", Reason: "Deploys deserve their own page: environments, rollbacks, and who to call.", Prompt: "Write docs/deploy.md.",
		Repositories: []wire.SessionProposalRepository{{Repository: "acme/web"}}}, ago(25*time.Hour))
	guide.finish(t, wire.TurnStatusCompleted, nil, ago(25*time.Hour))
	guide.s.LatestProviderUsage = wire.TokenUsage{ContextTokens: 22_000}

	search := base("ses_search", "Profile the search API", "xai", "grok-4", "", wire.MachineStatusDehydrated, ago(72*time.Hour))
	search.user("Why is /search slow for long queries?", ago(70*time.Hour), nil)
	t = search.turn("turn_x1", ago(70*time.Hour))
	search.reply(t, "Long queries skip the prefix index. A trigram index on `title` brings p95 from 840 ms to 95 ms.", ago(70*time.Hour))
	search.finish(t, wire.TurnStatusCompleted, nil, ago(70*time.Hour))

	cron := base("ses_cron", "Move the billing cron to the queue", "claude", "claude-opus-5", "high", wire.MachineStatusHydrated, ago(20*time.Minute))
	cron.user("Move the nightly billing cron onto the job queue.", ago(2*time.Minute), nil)
	cron.s.CurrentTurnID = "turn_c1"
	t = cron.turn("turn_c1", ago(2*time.Minute))
	cron.think(t, "Find where the cron is scheduled and what it calls.", ago(110*time.Second))
	cron.tool(t, "bash", map[string]any{"command": "rg -n 'billing' deploy/cron.yaml"}, map[string]any{"exit_code": 0, "output": "12:  - name: billing-nightly"}, ago(100*time.Second), 300*time.Millisecond)
	cron.call(t, "bash", map[string]any{"command": "go test ./billing/..."}, ago(10*time.Second))

	other := base("ses_other", "Rotate the staging keys", "openai", "gpt-5.5", "medium", wire.MachineStatusDehydrated, ago(30*time.Hour))
	other.s.ProjectID = "proj_ops"
	other.user("Rotate the staging API keys.", ago(29*time.Hour), nil)
	t = other.turn("turn_o1", ago(29*time.Hour))
	other.reply(t, "Rotated.", ago(29*time.Hour))
	other.finish(t, wire.TurnStatusCompleted, nil, ago(29*time.Hour))

	f.list = []*fakeSession{checkout, sdk, guide, search, cron, other}
}

// ---------------------------------------------------------------- building transcripts

func (fs *fakeSession) next() int64 { fs.seq++; return fs.seq }

func (fs *fakeSession) add(it wire.TranscriptItem) *wire.TranscriptItem {
	it.SessionID, it.Sequence = fs.s.SessionID, fs.next()
	fs.items = append(fs.items, it)
	if it.CreatedAt.After(fs.s.UpdatedAt) {
		fs.s.UpdatedAt = it.CreatedAt
	}
	return &fs.items[len(fs.items)-1]
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (fs *fakeSession) user(text string, at time.Time, media []wire.PromptMediaRef) {
	fs.prompt(fmt.Sprintf("msg_%d", fs.seq), text, at, media)
}

func (fs *fakeSession) prompt(requestID, text string, at time.Time, media []wire.PromptMediaRef) {
	fs.add(wire.TranscriptItem{Kind: wire.ItemKindUser, Payload: raw(wire.UserPromptPayload{Text: text, RequestID: requestID, Media: media}), CreatedAt: at})
	fs.s.LatestUserPromptAt = at
}

func (fs *fakeSession) turn(id string, at time.Time) string {
	fs.turns[id] = wire.Turn{TurnID: id, Status: wire.TurnStatusActive, CreatedAt: at}
	return id
}

func (fs *fakeSession) think(turn, text string, at time.Time) {
	fs.add(wire.TranscriptItem{TurnID: turn, Kind: wire.ItemKindReasoning, Payload: raw(map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": text}}}), CreatedAt: at})
}

func (fs *fakeSession) call(turn, kind string, args map[string]any, at time.Time) string {
	id := fmt.Sprintf("call_%d", fs.seq+1)
	fs.add(wire.TranscriptItem{TurnID: turn, Kind: wire.ItemKindToolCall, ToolCallID: id, Payload: raw(map[string]any{"tool_call_id": id, "kind": kind, "arguments": args}), CreatedAt: at})
	return id
}

func (fs *fakeSession) result(turn, id string, result map[string]any, at time.Time) {
	status := "completed"
	if code, ok := result["exit_code"].(int); ok && code != 0 {
		status = "failed"
	}
	fs.add(wire.TranscriptItem{TurnID: turn, Kind: wire.ItemKindToolResult, ToolCallID: id, Payload: raw(map[string]any{"status": status, "result": result}), CreatedAt: at})
}

func (fs *fakeSession) tool(turn, kind string, args, result map[string]any, at time.Time, took time.Duration) {
	id := fs.call(turn, kind, args, at)
	fs.result(turn, id, result, at.Add(took))
}

func (fs *fakeSession) reply(turn, text string, at time.Time) {
	fs.add(wire.TranscriptItem{TurnID: turn, StreamID: "stream_" + turn, Kind: wire.ItemKindProviderOutput,
		Payload: raw(map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}), CreatedAt: at})
}

func (fs *fakeSession) propose(turn string, p wire.SessionProposal, at time.Time) {
	id := fs.call(turn, string(wire.CommandKindNewSession), map[string]any{"title": p.Title, "reason": p.Reason}, at)
	fs.result(turn, id, map[string]any{"output": "Proposed."}, at)
	seq := fs.items[len(fs.items)-1].Sequence
	fs.proposals[seq] = wire.SessionProposalView{Sequence: seq, Proposal: p}
}

func (fs *fakeSession) finish(turn string, status wire.TurnStatus, failure *wire.ErrorInfo, at time.Time) {
	t := fs.turns[turn]
	t.Status, t.Failure, t.CompletedAt = status, failure, at
	fs.turns[turn] = t
	fs.s.CurrentTurnID = ""
	fs.s.LatestTerminalTurnID, fs.s.LatestTerminalTurnStatus, fs.s.LatestTerminalTurnAt = turn, status, at
	if at.After(fs.s.UpdatedAt) {
		fs.s.UpdatedAt = at
	}
}

func (fs *fakeSession) event(turn, stream string, kind wire.EventKind, payload any) {
	fs.ev++
	fs.events = append(fs.events, wire.Event{SessionID: fs.s.SessionID, TurnID: turn, StreamID: stream, Sequence: fs.ev, Kind: kind, Payload: raw(payload), CreatedAt: time.Now()})
}

// respond is the fake agent: a thought, a command, an edit, a screenshot,
// then a streamed reply. Every change is announced on the update sockets.
func (f *fakeAPI) respond(id string, msg api.PendingMessage) {
	step := func(change func(fs *fakeSession)) {
		if f.pace > 0 {
			time.Sleep(f.pace)
		}
		f.mu.Lock()
		fs := f.find(id)
		change(fs)
		fs.s.UpdatedAt = time.Now()
		f.mu.Unlock()
		f.notify(id)
	}
	turn := fmt.Sprintf("turn_%d", time.Now().UnixNano())
	var call string
	step(func(fs *fakeSession) {
		fs.pending = nil
		fs.prompt(msg.RequestID, msg.Text, time.Now(), msg.Media)
		fs.s.CurrentTurnID = fs.turn(turn, time.Now())
		fs.s.PendingTurn = false
		fs.s.MachineStatus = wire.MachineStatusHydrated
	})
	step(func(fs *fakeSession) {
		fs.think(turn, "Look at the code this touches, then make the change and test it.", time.Now())
	})
	step(func(fs *fakeSession) {
		call = fs.call(turn, "bash", map[string]any{"command": "go test ./..."}, time.Now())
	})
	step(func(fs *fakeSession) {
		fs.result(turn, call, map[string]any{"exit_code": 0, "output": "ok  \tacme/web\t2.114s\nok  \tacme/checkout\t1.402s"}, time.Now())
	})
	step(func(fs *fakeSession) {
		fs.tool(turn, "edit", map[string]any{"file_path": "/workspace/web/README.md", "edits": []any{map[string]any{"old_text": "Old line", "new_text": "New line\nAnother line"}}}, map[string]any{"output": "Edited"}, time.Now(), 80*time.Millisecond)
	})
	words := strings.Fields("Done. I made the change and ran the tests; everything passes. The README now describes the new behaviour.")
	stream := "stream_" + turn
	for i := 0; i < len(words); i += 3 {
		chunk := strings.Join(words[i:min(len(words), i+3)], " ") + " "
		step(func(fs *fakeSession) {
			fs.event(turn, stream, wire.EventKindModelDelta, map[string]any{"delta": chunk})
		})
	}
	step(func(fs *fakeSession) {
		fs.add(wire.TranscriptItem{TurnID: turn, StreamID: stream, Kind: wire.ItemKindProviderOutput,
			Payload: raw(map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": strings.Join(words, " ")}}}), CreatedAt: time.Now()})
		fs.finish(turn, wire.TurnStatusCompleted, nil, time.Now())
		fs.s.LatestProviderUsage.ContextTokens += 4_000
	})
}

func (f *fakeAPI) find(id string) *fakeSession {
	for _, fs := range f.list {
		if fs.s.SessionID == id {
			return fs
		}
	}
	return nil
}

func (f *fakeAPI) notify(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.socks {
		select {
		case ch <- api.Dirty{Sessions: []string{id}, Summaries: []string{id}}:
		default:
		}
	}
}

// ---------------------------------------------------------------- routes

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, code, msg string) {
		reply(status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
	}
	switch {
	case path == "/cognito":
		var in struct {
			AuthFlow string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		reply(200, map[string]any{"AuthenticationResult": map[string]any{"AccessToken": "fake", "IdToken": "fake", "RefreshToken": "fake", "ExpiresIn": 3600}})
		return
	case path == "/updates":
		f.socket(w, r)
		return
	case path == "/cli/sign-in" && r.Method == http.MethodPost:
		f.mu.Lock()
		f.started = wire.CLISignInStarted{UserCode: "BCDF-GHJK", DeviceCode: "device-secret", ExpiresIn: 600, Interval: 1,
			VerificationURL: f.srv.URL + "/?cli=BCDF-GHJK"}
		f.polls = 0
		started := f.started
		f.mu.Unlock()
		reply(200, started)
		return
	case path == "/cli/sign-in/token" && r.Method == http.MethodPost:
		var in struct {
			UserCode   string `json:"user_code"`
			DeviceCode string `json:"device_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.polls++
		polls, ok := f.polls, in.UserCode == f.started.UserCode && in.DeviceCode == f.started.DeviceCode && f.started.UserCode != ""
		f.mu.Unlock()
		switch {
		case !ok:
			fail(400, "invalid_grant", "no such sign-in request")
		case polls < 2:
			fail(400, "authorization_pending", "waiting for you to approve the sign-in")
		default:
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"you@example.com"}`))
			reply(200, wire.CLITokens{AccessToken: "fake", IDToken: "e30." + claims + ".sig", RefreshToken: "fake", ExpiresIn: 3600,
				ClientID: "fake-cli", CognitoURL: f.srv.URL + "/cognito"})
		}
		return
	case strings.HasPrefix(path, "/blob/"):
		f.mu.Lock()
		data := f.media[strings.TrimPrefix(path, "/blob/")]
		f.mu.Unlock()
		if data == nil {
			data = picture(1440, 900, 7)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
		return
	case strings.HasPrefix(path, "/upload/") && r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.media[strings.TrimPrefix(path, "/upload/")] = data
		f.mu.Unlock()
		w.WriteHeader(200)
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake" {
		fail(401, "unauthorized", "missing token")
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+path)
	f.mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	f.mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			f.mu.Unlock()
		}
	}()
	switch {
	case path == "/account/initialize":
		reply(200, map[string]any{})
	case path == "/account/update-ticket":
		reply(200, map[string]any{"protocol_version": 1, "ticket": "t", "connection_expires_at": time.Now().Add(time.Hour), "websocket_url": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/updates"})
	case path == "/sessions" && r.Method == http.MethodGet:
		var out []wire.Session
		for _, fs := range f.list {
			out = append(out, fs.s)
		}
		reply(200, map[string]any{"sessions": out})
	case path == "/sessions" && r.Method == http.MethodPost:
		id := fmt.Sprintf("ses_new%d", len(f.list))
		provider := strings.TrimPrefix(fmt.Sprint(body["provider_resource_id"]), "prov_")
		fs := &fakeSession{s: wire.Session{SessionID: id, ProviderResourceID: "prov_" + provider, Provider: wire.Provider(provider), Model: fmt.Sprint(body["model"]),
			ReasoningEffort: wire.ReasoningEffort(fmt.Sprint(body["reasoning_effort"])), MachineStatus: wire.MachineStatusHydrating, ContextWindowTokens: 200_000,
			ProjectID: fmt.Sprint(body["project_id"]), CreatedAt: time.Now(), UpdatedAt: time.Now()}, turns: map[string]wire.Turn{}, proposals: map[int64]wire.SessionProposalView{}}
		if repos, _ := body["repositories"].([]any); len(repos) > 0 {
			fs.repos = []string{fmt.Sprint(repos[0].(map[string]any)["repository"])}
		}
		f.list = append(f.list, fs)
		reply(201, fs.s)
	case path == "/projects":
		reply(200, map[string]any{"projects": []wire.Project{
			{ProjectID: "proj_acme", Name: "Acme", Agent: &wire.ProjectAgent{ProviderResourceID: "prov_claude", Model: "claude-opus-5", ReasoningEffort: "high"},
				Repositories: []wire.ProjectRepository{{SourceID: "src_1", Repository: "acme/web"}}},
			{ProjectID: "proj_ops", Name: "Ops"},
		}})
	case path == "/account/providers":
		reply(200, map[string]any{"providers": []wire.ProviderView{
			{AccountResource: wire.AccountResource{ResourceID: "prov_claude", Name: "Claude Max", ProviderType: "claude"}, Ready: true},
			{AccountResource: wire.AccountResource{ResourceID: "prov_openai", Name: "OpenAI", ProviderType: "openai"}, Ready: true},
		}})
	case len(parts) == 4 && parts[0] == "account" && parts[1] == "providers" && parts[3] == "models":
		models := []wire.ModelCapabilities{{Model: "claude-opus-5", SupportsReasoningEffort: true, ReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high"}, DefaultReasoningEffort: "high"},
			{Model: "claude-sonnet-5", SupportsReasoningEffort: true, ReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high"}, DefaultReasoningEffort: "medium"}}
		def := "claude-opus-5"
		if parts[2] != "prov_claude" {
			models = []wire.ModelCapabilities{{Model: "gpt-5.5", SupportsReasoningEffort: true, ReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium"}}
			def = "gpt-5.5"
		}
		reply(200, api.Models{Models: models, DefaultModel: def})
	case path == "/account/git-sources":
		reply(200, map[string]any{"sources": []wire.AccountResource{{ResourceID: "src_1", Name: "acme"}}})
	case len(parts) == 4 && parts[1] == "git-sources":
		reply(200, map[string]any{"repositories": []map[string]any{{"full_name": "acme/web", "default_branch": "main"}, {"full_name": "acme/api", "default_branch": "main"}, {"full_name": "acme/infra", "default_branch": "trunk"}}})
	case path == "/account/usage":
		reply(200, fakeUsage())
	case path == "/account/billing/usage":
		days := make([]map[string]any, 30)
		for i := range days {
			days[i] = map[string]any{"date": fakeStart.AddDate(0, 0, i-29).Format("2006-01-02"), "billed_minutes": 30 + i*3, "charged_usd_nanos": int64(20_000_000 * (1 + i%5)), "runs": 2}
		}
		reply(200, map[string]any{"totals": map[string]any{"billed_minutes": 2472, "charged_usd_nanos": 1_840_000_000, "runs": 61}, "daily": days})
	case path == "/account/billing":
		reply(200, api.Balance{Currency: "usd", BalanceUSDNanos: 8_200_000_000, TermsAccepted: true})
	case parts[0] == "sessions" && len(parts) >= 2:
		fs := f.find(parts[1])
		if fs == nil {
			fail(404, "not_found", "no such session")
			return
		}
		switch {
		case len(parts) == 2 && r.Method == http.MethodPatch:
			fs.s.Name = fmt.Sprint(body["name"])
			fs.s.Model = fmt.Sprint(body["model"])
			fs.s.ReasoningEffort = wire.ReasoningEffort(fmt.Sprint(body["reasoning_effort"]))
			fs.s.UpdatedAt = time.Now()
			reply(200, fs.s)
		case parts[2] == "sync":
			f.sync(fs, r, reply)
		case parts[2] == "transcript":
			reply(200, api.Older{})
		case parts[2] == "repositories":
			var out []wire.SessionGitRepository
			for _, name := range fs.repos {
				out = append(out, wire.SessionGitRepository{CanonicalPath: name})
			}
			reply(200, map[string]any{"repositories": out})
		case parts[2] == "messages":
			msg := api.PendingMessage{RequestID: fmt.Sprint(body["request_id"]), Text: fmt.Sprint(body["text"]), Kind: "inbox", CreatedAt: time.Now()}
			ids, _ := body["media_ids"].([]any)
			for _, id := range ids {
				msg.Media = append(msg.Media, f.refs[fmt.Sprint(id)])
			}
			fs.pending = append(fs.pending, msg)
			fs.s.PendingTurn = true
			session := fs.s
			unlock = false
			f.mu.Unlock()
			reply(202, wire.MessageAdmission{Session: &session})
			if f.pace > 0 {
				go f.respond(session.SessionID, msg)
			} else {
				f.respond(session.SessionID, msg)
			}
		case parts[2] == "cancel":
			if t := fs.s.CurrentTurnID; t != "" {
				fs.finish(t, wire.TurnStatusCancelled, nil, time.Now())
			}
			reply(200, map[string]any{})
		case parts[2] == "archive":
			fs.s.ArchivedAt = time.Now()
			reply(200, fs.s)
		case parts[2] == "hydrate":
			fs.s.MachineStatus = wire.MachineStatusHydrating
			reply(202, map[string]any{})
		case parts[2] == "shares":
			reply(201, api.Share{URL: f.srv.URL + "/s/abc", ExpiresAt: time.Now().Add(24 * time.Hour)})
		case parts[2] == "proposals" && len(parts) == 5:
			var seq int64
			fmt.Sscan(parts[3], &seq)
			v := fs.proposals[seq]
			status := map[string]string{"approve": "approved", "dismiss": "dismissed"}[parts[4]]
			v.Decision = &wire.SessionProposalDecision{Sequence: seq, Status: status, CreatedAt: time.Now()}
			if status == "approved" {
				id := fmt.Sprintf("ses_new%d", len(f.list))
				n := &fakeSession{s: wire.Session{SessionID: id, Name: v.Proposal.Title, ProviderResourceID: fs.s.ProviderResourceID, Provider: fs.s.Provider, Model: fs.s.Model,
					MachineStatus: wire.MachineStatusHydrating, ProjectID: fs.s.ProjectID, CreatedAt: time.Now(), UpdatedAt: time.Now()}, turns: map[string]wire.Turn{}, proposals: map[int64]wire.SessionProposalView{}}
				f.list = append(f.list, n)
				v.Decision.DestinationID = id
			}
			fs.proposals[seq] = v
			reply(200, v.Decision)
		case parts[2] == "prompt-media" && len(parts) == 3:
			id := fmt.Sprint(body["media_id"])
			f.refs[id] = wire.PromptMediaRef{MediaID: id, Filename: fmt.Sprint(body["filename"]), MediaType: fmt.Sprint(body["media_type"])}
			reply(200, map[string]any{"media": wire.PromptMedia{MediaID: id}, "upload": map[string]any{"method": "PUT", "url": f.srv.URL + "/upload/" + id}})
		case parts[2] == "prompt-media" && len(parts) == 5 && parts[4] == "complete":
			reply(200, map[string]any{})
		case parts[2] == "prompt-media" && len(parts) == 5 && parts[4] == "view":
			reply(200, map[string]any{"url": f.srv.URL + "/blob/" + parts[3]})
		case parts[2] == "screenshots":
			reply(200, map[string]any{"url": f.srv.URL + "/blob/shot"})
		default:
			fail(404, "not_found", "no route "+path)
		}
	default:
		fail(404, "not_found", "no route "+path)
	}
}

func (f *fakeAPI) sync(fs *fakeSession, r *http.Request, reply func(int, any)) {
	q := r.URL.Query()
	var afterEvent int64
	fmt.Sscan(q.Get("after_event"), &afterEvent)
	out := api.Sync{Session: fs.s, PendingMessages: fs.pending}
	if q.Has("after_transcript") {
		var after int64
		fmt.Sscan(q.Get("after_transcript"), &after)
		for _, it := range fs.items {
			if it.Sequence > after {
				out.Items = append(out.Items, it)
			}
		}
		out.TranscriptNextCursor = after
	} else {
		out.Items = fs.items
		if len(fs.items) > 0 {
			out.TranscriptBeforeCursor = fs.items[0].Sequence
		}
	}
	if n := len(out.Items); n > 0 {
		out.TranscriptNextCursor = out.Items[n-1].Sequence
	}
	for _, e := range fs.events {
		if e.Sequence > afterEvent {
			out.Events = append(out.Events, e)
		}
	}
	out.EventNextCursor = max(afterEvent, fs.ev)
	for _, t := range fs.turns {
		out.Turns = append(out.Turns, t)
	}
	for _, p := range fs.proposals {
		out.Proposals = append(out.Proposals, p)
	}
	reply(200, out)
}

func (f *fakeAPI) socket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ch := make(chan api.Dirty, 64)
	f.mu.Lock()
	f.socks[ch] = true
	f.mu.Unlock()
	defer func() { f.mu.Lock(); delete(f.socks, ch); f.mu.Unlock() }()
	ctx := r.Context()
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(data, &m) == nil && m["type"] == "ping" {
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong","version":1}`))
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-ch:
			msg, _ := json.Marshal(map[string]any{"type": "dirty", "version": 1, "sessions": d.Sessions, "summaries": d.Summaries})
			if conn.Write(ctx, websocket.MessageText, msg) != nil {
				return
			}
		}
	}
}

func fakeUsage() wire.UsageReport {
	r := wire.UsageReport{}
	for i := 0; i < 30; i++ {
		cost := int64((0.12 + 0.2*hash(i, 7) + 0.08*math.Sin(float64(i)/3)) * 1e9)
		m := wire.UsageMetrics{InputTokens: 2_000_000, CachedInputTokens: 1_200_000, ProcessedTokens: 2_400_000,
			ActualCost: wire.CostMetrics{ProviderReportedNanodollars: cost}, APIEquivalentCost: wire.CostMetrics{ProviderReportedNanodollars: cost * 60}}
		r.Daily = append(r.Daily, wire.UsageDay{Date: fakeStart.AddDate(0, 0, i-29).Format("2006-01-02"), Metrics: m})
		r.Totals.InputTokens += m.InputTokens
		r.Totals.CachedInputTokens += m.CachedInputTokens
		r.Totals.ProcessedTokens += m.ProcessedTokens
		r.Totals.ActualCost.ProviderReportedNanodollars += cost
		r.Totals.APIEquivalentCost.ProviderReportedNanodollars += cost * 60
	}
	return r
}

// picture draws a stand-in for a guest display: a desktop with a window.
func picture(w, h, seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(y) / float64(h)
			c := color.RGBA{uint8(30 + 40*t), uint8(40 + 60*t), uint8(70 + 90*t), 255}
			switch {
			case x > w/8 && x < w*7/8 && y > h/8 && y < h*7/8:
				c = color.RGBA{246, 243, 236, 255}
				if y < h/8+36 {
					c = color.RGBA{24, 32, 41, 255}
				}
				if y > h/4 && y < h/4+20 && x < w/2 {
					c = color.RGBA{255, 91, 53, 255}
				}
				if (y-h/3)%28 < 8 && y > h/3 && x < w*3/4 && x > w/6+seed {
					c = color.RGBA{180, 186, 194, 255}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// TestServeFakeAPI runs the fake API for a manual session when
// BARAD_FAKE_API_DIR names a directory: it writes the credentials there and
// serves until interrupted (barad login against it is approved at once).
// Point barad at it with
//
//	XDG_CONFIG_HOME=$BARAD_FAKE_API_DIR BARAD_API_URL=$(cat $BARAD_FAKE_API_DIR/url) barad
func TestServeFakeAPI(t *testing.T) {
	dir := os.Getenv("BARAD_FAKE_API_DIR")
	if dir == "" {
		t.Skip("set BARAD_FAKE_API_DIR to serve the fake API")
	}
	f := newFakeAPI(1200 * time.Millisecond)
	defer f.srv.Close()
	f.signIn(t, dir)
	_ = os.WriteFile(filepath.Join(dir, "url"), []byte(f.srv.URL), 0o600)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
}

package fakeapi

import (
	"bytes"
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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// API is a Barad API in memory, with the routes the app uses and an
// agent that answers messages with a run of work. It also answers Cognito,
// and approves a device-code sign-in at its second poll, so `barad login`
// works against it.
type API struct {
	Srv     *httptest.Server
	pace    time.Duration // between an agent's steps; 0 answers at once
	Mu      sync.Mutex
	List    []*Session
	Media   map[string][]byte
	refs    map[string]wire.PromptMediaRef // reserved media, by id
	socks   map[chan api.Dirty]bool
	Calls   []string // "METHOD path" of every API call, for assertions
	Polls   int      // sign-in polls so far
	started wire.CLISignInStarted

	Projects     []wire.Project
	Environments map[string]*wire.EnvironmentView // by "sessions/ID" or "projects/ID"
	account
}

type Session struct {
	S         wire.Session
	Created   map[string]any // what it was started with: the create request's body
	Items     []wire.TranscriptItem
	Turns     map[string]wire.Turn
	Events    []wire.Event
	Pending   []api.PendingMessage
	Proposals map[int64]wire.SessionProposalView
	Repos     []string
	Files     map[string][]byte // the workspace, by path under /workspace
	Seq, Ev   int64
}

func New(pace time.Duration) *API {
	f := &API{pace: pace, Media: map[string][]byte{}, refs: map[string]wire.PromptMediaRef{}, socks: map[chan api.Dirty]bool{},
		Environments: map[string]*wire.EnvironmentView{}}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.seed()
	f.seedAccount()
	return f
}

// signIn points a client at the fake, signed in, with its files in dir.
func (f *API) SignIn(t testing.TB, dir string) *api.Client {
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("BARAD_API_URL", f.Srv.URL)
	creds, _ := json.Marshal(api.Tokens{AccessToken: "fake", RefreshToken: "fake", ExpiresAt: time.Now().Add(24 * time.Hour), Email: "you@example.com",
		ClientID: "fake-cli", CognitoURL: f.Srv.URL + "/cognito"})
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

var Start = time.Now()

func (f *API) seed() {
	ago := func(d time.Duration) time.Time { return Start.Add(-d) }
	base := func(id, name, provider, mdl string, effort wire.ReasoningEffort, machine wire.MachineStatus, created time.Time) *Session {
		return &Session{S: wire.Session{SessionID: id, Name: name, ProviderResourceID: "prov_" + provider, Provider: wire.Provider(provider), Model: mdl,
			ReasoningEffort: effort, SupportsReasoningEffort: effort != "", SupportedReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high"},
			MachineStatus: machine, ContextWindowTokens: 200_000, AutoCompactTokenLimit: 160_000, ProjectID: "proj_acme", CreatedAt: created, UpdatedAt: created},
			Turns: map[string]wire.Turn{}, Proposals: map[int64]wire.SessionProposalView{}, Repos: []string{"acme/web"},
			Files: map[string][]byte{"web/README.md": []byte("# Web\n\nThe storefront.\n"), "web/checkout/cart.go": []byte("package checkout\n"), "notes.txt": []byte("remember the milk\n")}}
	}

	checkout := base("ses_checkout", "Fix the flaky checkout test", "claude", "claude-opus-5", "high", wire.MachineStatusHydrated, ago(3*time.Hour))
	checkout.User("The checkout test fails about one run in five on CI. Find out why and fix it.", ago(40*time.Minute), nil)
	t := checkout.Turn("turn_1", ago(40*time.Minute))
	checkout.Think(t, "The failure is intermittent, so it is likely a race or an ordering assumption. Run the test with -race and -count to reproduce it.", ago(39*time.Minute))
	checkout.Tool(t, "bash", map[string]any{"command": "go test ./checkout/ -race -count=20"}, map[string]any{"exit_code": 1, "output": "--- FAIL: TestCartTotals (0.02s)\n    cart_test.go:41: total = 1998, want 2000\nWARNING: DATA RACE\nWrite at 0x00c0001a4018 by goroutine 9:\n  checkout.(*Cart).Add()\nFAIL\nFAIL\tacme/checkout\t1.204s"}, ago(39*time.Minute), 3*time.Second)
	checkout.Tool(t, "read", map[string]any{"file_path": "/workspace/web/checkout/cart.go"}, map[string]any{"output": "package checkout\n\ntype Cart struct {\n\tItems []Item\n\ttotal int\n}\n"}, ago(38*time.Minute), 200*time.Millisecond)
	checkout.Tool(t, "edit", map[string]any{"file_path": "/workspace/web/checkout/cart.go", "edits": []any{map[string]any{"old_text": "type Cart struct {\n\tItems []Item\n\ttotal int\n}", "new_text": "type Cart struct {\n\tmu    sync.Mutex\n\tItems []Item\n\ttotal int\n}"}}}, map[string]any{"output": "Edited cart.go"}, ago(37*time.Minute), 100*time.Millisecond)
	checkout.Tool(t, "bash", map[string]any{"command": "go test ./checkout/ -race -count=50"}, map[string]any{"exit_code": 0, "output": "ok  \tacme/checkout\t3.911s"}, ago(36*time.Minute), 4*time.Second)
	checkout.Tool(t, "screenshot", map[string]any{}, map[string]any{"screenshot": map[string]any{"width": 1440, "height": 900}}, ago(35*time.Minute), 900*time.Millisecond)
	checkout.Reply(t, "The test was flaky because `Cart.Add` updated `total` from two goroutines without a lock.\n\n## What changed\n\n- `Cart` now holds a `sync.Mutex`, taken by `Add` and `Total`.\n- The test runs 50 times under `-race` without a failure.\n\n```go\nfunc (c *Cart) Add(it Item) {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\tc.Items = append(c.Items, it)\n\tc.total += it.Price\n}\n```\n\nThe checkout page still renders the same; the screenshot above is from the dev server.", ago(34*time.Minute))
	checkout.Finish(t, wire.TurnStatusCompleted, nil, ago(34*time.Minute))
	checkout.S.LatestProviderUsage = wire.TokenUsage{ContextTokens: 61_000}

	sdk := base("ses_sdk", "Upgrade the payments SDK", "openai", "gpt-5.5", "medium", wire.MachineStatusHydrated, ago(5*time.Hour))
	sdk.User("Upgrade stripe-go to v86 and fix whatever breaks.", ago(2*time.Hour), nil)
	t = sdk.Turn("turn_s1", ago(2*time.Hour))
	sdk.Tool(t, "bash", map[string]any{"command": "go get github.com/stripe/stripe-go/v86 && go build ./..."}, map[string]any{"exit_code": 1, "output": "payments/charge.go:22:9: undefined: stripe.ChargeParams"}, ago(2*time.Hour), 6*time.Second)
	sdk.Finish(t, wire.TurnStatusFailed, &wire.ErrorInfo{Code: "provider_error", Message: "The provider returned an error twice in a row: the request was too large for the model's context."}, ago(119*time.Minute))
	sdk.S.LatestProviderUsage = wire.TokenUsage{ContextTokens: 171_000}

	guide := base("ses_guide", "Write the onboarding guide", "claude", "claude-sonnet-5", "medium", wire.MachineStatusHydrated, ago(26*time.Hour))
	guide.User("Draft docs/onboarding.md from what new people ask in #help.", ago(25*time.Hour), nil)
	t = guide.Turn("turn_g1", ago(25*time.Hour))
	guide.Reply(t, "I drafted **docs/onboarding.md**. The deploy section is long enough to be its own guide, so I am proposing a separate session for it.", ago(25*time.Hour))
	guide.Propose(t, wire.SessionProposal{Title: "Write the deploy guide", Reason: "Deploys deserve their own page: environments, rollbacks, and who to call.", Prompt: "Write docs/deploy.md.",
		Repositories: []wire.SessionProposalRepository{{Repository: "acme/web"}}}, ago(25*time.Hour))
	guide.Finish(t, wire.TurnStatusCompleted, nil, ago(25*time.Hour))
	guide.S.LatestProviderUsage = wire.TokenUsage{ContextTokens: 22_000}

	search := base("ses_search", "Profile the search API", "xai", "grok-4", "", wire.MachineStatusDehydrated, ago(72*time.Hour))
	search.User("Why is /search slow for long queries?", ago(70*time.Hour), nil)
	t = search.Turn("turn_x1", ago(70*time.Hour))
	search.Reply(t, "Long queries skip the prefix index. A trigram index on `title` brings p95 from 840 ms to 95 ms ([pg_trgm](https://www.postgresql.org/docs/current/pgtrgm.html)).", ago(70*time.Hour))
	search.Finish(t, wire.TurnStatusCompleted, nil, ago(70*time.Hour))

	cron := base("ses_cron", "Move the billing cron to the queue", "claude", "claude-opus-5", "high", wire.MachineStatusHydrated, ago(20*time.Minute))
	cron.User("Move the nightly billing cron onto the job queue.", ago(2*time.Minute), nil)
	cron.S.CurrentTurnID = "turn_c1"
	t = cron.Turn("turn_c1", ago(2*time.Minute))
	cron.Think(t, "Find where the cron is scheduled and what it calls.", ago(110*time.Second))
	cron.Tool(t, "bash", map[string]any{"command": "rg -n 'billing' deploy/cron.yaml"}, map[string]any{"exit_code": 0, "output": "12:  - name: billing-nightly"}, ago(100*time.Second), 300*time.Millisecond)
	cron.Call(t, "bash", map[string]any{"command": "go test ./billing/..."}, ago(10*time.Second))

	other := base("ses_other", "Rotate the staging keys", "openai", "gpt-5.5", "medium", wire.MachineStatusDehydrated, ago(30*time.Hour))
	other.S.ProjectID = "proj_ops"
	other.User("Rotate the staging API keys.", ago(29*time.Hour), nil)
	t = other.Turn("turn_o1", ago(29*time.Hour))
	other.Reply(t, "Rotated.", ago(29*time.Hour))
	other.Finish(t, wire.TurnStatusCompleted, nil, ago(29*time.Hour))

	f.List = []*Session{checkout, sdk, guide, search, cron, other}
	f.Projects = []wire.Project{
		{ProjectID: "proj_acme", Name: "Acme", Agent: &wire.ProjectAgent{ProviderResourceID: "prov_claude", Model: "claude-opus-5", ReasoningEffort: "high"},
			Repositories: []wire.ProjectRepository{{SourceID: "src_1", Repository: "acme/web"}}},
		{ProjectID: "proj_ops", Name: "Ops"},
	}
}

// ---------------------------------------------------------------- building transcripts

func (fs *Session) next() int64 { fs.Seq++; return fs.Seq }

func (fs *Session) add(it wire.TranscriptItem) *wire.TranscriptItem {
	it.SessionID, it.Sequence = fs.S.SessionID, fs.next()
	fs.Items = append(fs.Items, it)
	if it.CreatedAt.After(fs.S.UpdatedAt) {
		fs.S.UpdatedAt = it.CreatedAt
	}
	return &fs.Items[len(fs.Items)-1]
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (fs *Session) User(text string, at time.Time, media []wire.PromptMediaRef) {
	fs.Prompt(fmt.Sprintf("msg_%d", fs.Seq), text, at, media)
}

func (fs *Session) Prompt(requestID, text string, at time.Time, media []wire.PromptMediaRef) {
	fs.add(wire.TranscriptItem{Kind: wire.ItemKindUser, Payload: raw(wire.UserPromptPayload{Text: text, RequestID: requestID, Media: media}), CreatedAt: at})
	fs.S.LatestUserPromptAt = at
}

func (fs *Session) Turn(id string, at time.Time) string {
	fs.Turns[id] = wire.Turn{TurnID: id, Status: wire.TurnStatusActive, CreatedAt: at}
	return id
}

func (fs *Session) Think(turn, text string, at time.Time) {
	fs.add(wire.TranscriptItem{TurnID: turn, Kind: wire.ItemKindReasoning, Payload: raw(map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": text}}}), CreatedAt: at})
}

func (fs *Session) Call(turn, kind string, args map[string]any, at time.Time) string {
	id := fmt.Sprintf("call_%d", fs.Seq+1)
	fs.add(wire.TranscriptItem{TurnID: turn, Kind: wire.ItemKindToolCall, ToolCallID: id, Payload: raw(map[string]any{"tool_call_id": id, "kind": kind, "arguments": args}), CreatedAt: at})
	return id
}

func (fs *Session) Result(turn, id string, result map[string]any, at time.Time) {
	status := "completed"
	if code, ok := result["exit_code"].(int); ok && code != 0 {
		status = "failed"
	}
	fs.add(wire.TranscriptItem{TurnID: turn, Kind: wire.ItemKindToolResult, ToolCallID: id, Payload: raw(map[string]any{"status": status, "result": result}), CreatedAt: at})
}

func (fs *Session) Tool(turn, kind string, args, result map[string]any, at time.Time, took time.Duration) {
	id := fs.Call(turn, kind, args, at)
	fs.Result(turn, id, result, at.Add(took))
}

func (fs *Session) Reply(turn, text string, at time.Time) {
	fs.add(wire.TranscriptItem{TurnID: turn, StreamID: "stream_" + turn, Kind: wire.ItemKindProviderOutput,
		Payload: raw(map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}), CreatedAt: at})
}

func (fs *Session) Propose(turn string, p wire.SessionProposal, at time.Time) {
	id := fs.Call(turn, string(wire.CommandKindNewSession), map[string]any{"title": p.Title, "reason": p.Reason}, at)
	fs.Result(turn, id, map[string]any{"output": "Proposed."}, at)
	seq := fs.Items[len(fs.Items)-1].Sequence
	fs.Proposals[seq] = wire.SessionProposalView{Sequence: seq, Proposal: p}
}

func (fs *Session) Finish(turn string, status wire.TurnStatus, failure *wire.ErrorInfo, at time.Time) {
	t := fs.Turns[turn]
	t.Status, t.Failure, t.CompletedAt = status, failure, at
	fs.Turns[turn] = t
	fs.S.CurrentTurnID = ""
	fs.S.LatestTerminalTurnID, fs.S.LatestTerminalTurnStatus, fs.S.LatestTerminalTurnAt = turn, status, at
	if at.After(fs.S.UpdatedAt) {
		fs.S.UpdatedAt = at
	}
}

func (fs *Session) Event(turn, stream string, kind wire.EventKind, payload any) {
	fs.Ev++
	fs.Events = append(fs.Events, wire.Event{SessionID: fs.S.SessionID, TurnID: turn, StreamID: stream, Sequence: fs.Ev, Kind: kind, Payload: raw(payload), CreatedAt: time.Now()})
}

// respond is the fake agent: a thought, a command, an edit, a screenshot,
// then a streamed reply. Every change is announced on the update sockets.
func (f *API) respond(id string, msg api.PendingMessage) {
	step := func(change func(fs *Session)) {
		if f.pace > 0 {
			time.Sleep(f.pace)
		}
		f.Mu.Lock()
		fs := f.Find(id)
		change(fs)
		fs.S.UpdatedAt = time.Now()
		f.Mu.Unlock()
		f.notify(id)
	}
	turn := fmt.Sprintf("turn_%d", time.Now().UnixNano())
	var call string
	step(func(fs *Session) {
		fs.Pending = nil
		fs.Prompt(msg.RequestID, msg.Text, time.Now(), msg.Media)
		fs.S.CurrentTurnID = fs.Turn(turn, time.Now())
		fs.S.PendingTurn = false
		fs.S.MachineStatus = wire.MachineStatusHydrated
	})
	step(func(fs *Session) {
		fs.Think(turn, "Look at the code this touches, then make the change and test it.", time.Now())
	})
	step(func(fs *Session) {
		call = fs.Call(turn, "bash", map[string]any{"command": "go test ./..."}, time.Now())
	})
	step(func(fs *Session) {
		fs.Result(turn, call, map[string]any{"exit_code": 0, "output": "ok  \tacme/web\t2.114s\nok  \tacme/checkout\t1.402s"}, time.Now())
	})
	step(func(fs *Session) {
		fs.Tool(turn, "edit", map[string]any{"file_path": "/workspace/web/README.md", "edits": []any{map[string]any{"old_text": "Old line", "new_text": "New line\nAnother line"}}}, map[string]any{"output": "Edited"}, time.Now(), 80*time.Millisecond)
	})
	words := strings.Fields("Done. I made the change and ran the tests; everything passes. The README now describes the new behaviour.")
	stream := "stream_" + turn
	for i := 0; i < len(words); i += 3 {
		chunk := strings.Join(words[i:min(len(words), i+3)], " ") + " "
		step(func(fs *Session) {
			fs.Event(turn, stream, wire.EventKindModelDelta, map[string]any{"delta": chunk})
		})
	}
	step(func(fs *Session) {
		fs.add(wire.TranscriptItem{TurnID: turn, StreamID: stream, Kind: wire.ItemKindProviderOutput,
			Payload: raw(map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": strings.Join(words, " ")}}}), CreatedAt: time.Now()})
		fs.Finish(turn, wire.TurnStatusCompleted, nil, time.Now())
		fs.S.LatestProviderUsage.ContextTokens += 4_000
	})
}

func (f *API) Find(id string) *Session {
	for _, fs := range f.List {
		if fs.S.SessionID == id {
			return fs
		}
	}
	return nil
}

func (f *API) notify(id string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for ch := range f.socks {
		select {
		case ch <- api.Dirty{Sessions: []string{id}, Summaries: []string{id}}:
		default:
		}
	}
}

// ---------------------------------------------------------------- routes

func (f *API) serve(w http.ResponseWriter, r *http.Request) {
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
			AuthFlow                           string
			PreviousPassword, ProposedPassword string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get("X-Amz-Target") == "AWSCognitoIdentityProviderService.ChangePassword" {
			f.Mu.Lock()
			defer f.Mu.Unlock()
			if in.PreviousPassword != f.Password {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"__type": "NotAuthorizedException", "message": "Incorrect username or password."})
				return
			}
			f.Password = in.ProposedPassword
			reply(200, map[string]any{})
			return
		}
		reply(200, map[string]any{"AuthenticationResult": map[string]any{"AccessToken": "fake", "IdToken": "fake", "RefreshToken": "fake", "ExpiresIn": 3600}})
		return
	case strings.HasPrefix(path, "/object/"):
		f.objects(w, r)
		return
	case path == "/relay":
		f.relay(w, r)
		return
	case path == "/updates":
		f.socket(w, r)
		return
	case path == "/cli/sign-in" && r.Method == http.MethodPost:
		f.Mu.Lock()
		f.started = wire.CLISignInStarted{UserCode: "BCDF-GHJK", DeviceCode: "device-secret", ExpiresIn: 600, Interval: 1,
			VerificationURL: f.Srv.URL + "/?cli=BCDF-GHJK"}
		f.Polls = 0
		started := f.started
		f.Mu.Unlock()
		reply(200, started)
		return
	case path == "/cli/sign-in/token" && r.Method == http.MethodPost:
		var in struct {
			UserCode   string `json:"user_code"`
			DeviceCode string `json:"device_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.Mu.Lock()
		f.Polls++
		polls, ok := f.Polls, in.UserCode == f.started.UserCode && in.DeviceCode == f.started.DeviceCode && f.started.UserCode != ""
		f.Mu.Unlock()
		switch {
		case !ok:
			fail(400, "invalid_grant", "no such sign-in request")
		case polls < 2:
			fail(400, "authorization_pending", "waiting for you to approve the sign-in")
		default:
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"you@example.com"}`))
			reply(200, wire.CLITokens{AccessToken: "fake", IDToken: "e30." + claims + ".sig", RefreshToken: "fake", ExpiresIn: 3600,
				ClientID: "fake-cli", CognitoURL: f.Srv.URL + "/cognito"})
		}
		return
	case strings.HasPrefix(path, "/blob/"):
		f.Mu.Lock()
		data := f.Media[strings.TrimPrefix(path, "/blob/")]
		f.Mu.Unlock()
		if data == nil {
			data = Picture(1440, 900, 7)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
		return
	case strings.HasPrefix(path, "/upload/") && r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.Mu.Lock()
		f.Media[strings.TrimPrefix(path, "/upload/")] = data
		f.Mu.Unlock()
		w.WriteHeader(200)
		return
	}
	if r.Header.Get("Authorization") != "Bearer fake" {
		fail(401, "unauthorized", "missing token")
		return
	}
	f.Mu.Lock()
	f.Calls = append(f.Calls, r.Method+" "+path)
	f.Mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	f.Mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			f.Mu.Unlock()
		}
	}()
	if f.serveAccount(w, r, parts, body, reply, fail) {
		return
	}
	switch {
	case path == "/account/initialize":
		reply(200, map[string]any{})
	case path == "/account/update-ticket":
		reply(200, map[string]any{"protocol_version": 1, "ticket": "t", "connection_expires_at": time.Now().Add(time.Hour), "websocket_url": "ws" + strings.TrimPrefix(f.Srv.URL, "http") + "/updates"})
	case path == "/sessions" && r.Method == http.MethodGet:
		var out []wire.Session
		for _, fs := range f.List {
			out = append(out, fs.S)
		}
		reply(200, map[string]any{"sessions": out})
	case path == "/sessions" && r.Method == http.MethodPost:
		id := fmt.Sprintf("ses_new%d", len(f.List))
		provider := strings.TrimPrefix(fmt.Sprint(body["provider_resource_id"]), "prov_")
		name, _ := body["name"].(string)
		if name == "" {
			name = "brisk otter " + strings.TrimPrefix(id, "ses_new") // as the API names it
		}
		fs := &Session{S: wire.Session{SessionID: id, Name: name, ProviderResourceID: "prov_" + provider, Provider: wire.Provider(provider), Model: fmt.Sprint(body["model"]),
			ReasoningEffort: wire.ReasoningEffort(fmt.Sprint(body["reasoning_effort"])), MachineStatus: wire.MachineStatusHydrating, ContextWindowTokens: 200_000,
			ProjectID: fmt.Sprint(body["project_id"]), CreatedAt: time.Now(), UpdatedAt: time.Now()}, Turns: map[string]wire.Turn{}, Proposals: map[int64]wire.SessionProposalView{}}
		repos, _ := body["repositories"].([]any)
		for _, r := range repos {
			fs.Repos = append(fs.Repos, fmt.Sprint(r.(map[string]any)["repository"]))
		}
		fs.Created = body
		f.List = append(f.List, fs)
		reply(201, fs.S)
	case path == "/projects" && r.Method == http.MethodGet:
		reply(200, map[string]any{"projects": f.Projects})
	case path == "/projects" && r.Method == http.MethodPost:
		var p wire.Project
		_ = json.Unmarshal(raw(body), &p)
		p.ProjectID = fmt.Sprintf("proj_%d", len(f.Projects))
		f.Projects = append(f.Projects, p)
		reply(201, p)
	case len(parts) == 2 && parts[0] == "projects":
		i := slices.IndexFunc(f.Projects, func(p wire.Project) bool { return p.ProjectID == parts[1] })
		if i < 0 {
			fail(404, "not_found", "no such project")
			return
		}
		if r.Method == http.MethodDelete {
			f.Projects[i].ClosingAt = time.Now()
			for _, fs := range f.List {
				if fs.S.ProjectID == parts[1] {
					fs.S.ProjectID = wire.DefaultProjectID
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var p wire.Project
		_ = json.Unmarshal(raw(body), &p)
		p.ProjectID = parts[1]
		f.Projects[i] = p
		reply(200, p)
	case len(parts) == 3 && (parts[0] == "projects" || parts[0] == "sessions") && parts[2] == "environment":
		key := parts[0] + "/" + parts[1]
		env := f.Environments[key]
		if env == nil {
			env = &wire.EnvironmentView{Variables: []wire.EnvironmentVariable{}}
			f.Environments[key] = env
		}
		if r.Method == http.MethodPut {
			var m wire.EnvironmentMutation
			_ = json.Unmarshal(raw(body), &m)
			if m.ExpectedRevision != env.Revision {
				fail(409, "conflict", "the environment changed")
				return
			}
			for _, v := range m.SetVariables {
				env.Variables = slices.DeleteFunc(env.Variables, func(x wire.EnvironmentVariable) bool { return x.Name == v.Name })
				env.Variables = append(env.Variables, v)
			}
			for _, name := range m.RemoveVariables {
				env.Variables = slices.DeleteFunc(env.Variables, func(x wire.EnvironmentVariable) bool { return x.Name == name })
			}
			slices.SortFunc(env.Variables, func(a, b wire.EnvironmentVariable) int { return strings.Compare(a.Name, b.Name) })
			env.Revision++
		}
		out := wire.EnvironmentView{Revision: env.Revision}
		for _, v := range env.Variables {
			if v.Secret {
				v.Value = ""
			}
			out.Variables = append(out.Variables, v)
		}
		reply(200, out)
	case len(parts) == 4 && parts[1] == "git-sources" && parts[3] == "branches":
		reply(200, wire.GitBranches{DefaultBranch: "main", Branches: []string{"main", "release", "spike/queue"}})
	case len(parts) == 4 && parts[0] == "account" && parts[1] == "providers" && parts[3] == "models":
		models := []wire.ModelCapabilities{{Model: "claude-opus-5", SupportsReasoningEffort: true, ReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high"}, DefaultReasoningEffort: "high"},
			{Model: "claude-sonnet-5", SupportsReasoningEffort: true, ReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high"}, DefaultReasoningEffort: "medium"}}
		def := "claude-opus-5"
		if parts[2] != "prov_claude" {
			models = []wire.ModelCapabilities{{Model: "gpt-5.5", SupportsReasoningEffort: true, ReasoningEfforts: []wire.ReasoningEffort{"low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium"}}
			def = "gpt-5.5"
		}
		reply(200, api.Models{Models: models, DefaultModel: def})
	case len(parts) == 4 && parts[1] == "git-sources":
		reply(200, map[string]any{"repositories": []map[string]any{{"full_name": "acme/web", "default_branch": "main"}, {"full_name": "acme/api", "default_branch": "main"}, {"full_name": "acme/infra", "default_branch": "trunk"}}})
	case path == "/account/usage":
		reply(200, Usage())
	case path == "/account/billing/usage":
		days := make([]map[string]any, 30)
		for i := range days {
			days[i] = map[string]any{"date": Start.AddDate(0, 0, i-29).Format("2006-01-02"), "billed_minutes": 30 + i*3, "charged_usd_nanos": int64(20_000_000 * (1 + i%5)), "runs": 2}
		}
		reply(200, map[string]any{"totals": map[string]any{"billed_minutes": 2472, "charged_usd_nanos": 1_840_000_000, "runs": 61}, "daily": days,
			"workspaces": []wire.WorkspaceRun{
				{SessionID: "ses_checkout", BilledMinutes: 1500, ChargedUSDNanos: 1_000_000_000, Runs: 30, LastUsedAt: Start.Add(-time.Hour)},
				{SessionID: "ses_sdk", BilledMinutes: 972, ChargedUSDNanos: 840_000_000, Runs: 31, LastUsedAt: Start.Add(-5 * time.Hour)},
			},
			"balance_activity": f.History})
	case parts[0] == "sessions" && len(parts) >= 2:
		fs := f.Find(parts[1])
		if fs == nil {
			fail(404, "not_found", "no such session")
			return
		}
		switch {
		case len(parts) == 2 && r.Method == http.MethodPatch:
			fs.S.Name = fmt.Sprint(body["name"])
			fs.S.Model = fmt.Sprint(body["model"])
			fs.S.ReasoningEffort = wire.ReasoningEffort(fmt.Sprint(body["reasoning_effort"]))
			fs.S.FastMode = body["fast_mode"] == true
			fs.S.UpdatedAt = time.Now()
			reply(200, fs.S)
		case len(parts) == 2 && r.Method == http.MethodDelete:
			fs.S.MachineStatus = wire.MachineStatusDeleting
			reply(202, fs.S)
		case parts[2] == "project":
			fs.S.ProjectID = fmt.Sprint(body["project_id"])
			fs.S.UpdatedAt = time.Now()
			reply(200, fs.S)
		case parts[2] == "workspace-instructions":
			reply(200, wire.WorkspaceInstructions{ScannedAt: fs.S.CreatedAt, Loaded: []wire.WorkspaceInstructionFile{{Path: "AGENTS.md", LoadedBytes: 2048, SourceBytes: 2048}},
				Discovered: []string{"AGENTS.md", "web/AGENTS.md"}})
		case parts[2] == "repositories" && r.Method == http.MethodPost:
			fs.Repos = append(fs.Repos, fmt.Sprint(body["repository"]))
			reply(201, map[string]any{})
		case parts[2] == "repositories" && r.Method == http.MethodDelete:
			fs.Repos = slices.DeleteFunc(fs.Repos, func(name string) bool { return name == r.URL.Query().Get("repository") })
			w.WriteHeader(http.StatusNoContent)
		case parts[2] == "sync":
			f.sync(fs, r, reply)
		case parts[2] == "transcript":
			reply(200, api.Older{})
		case parts[2] == "repositories":
			var out []wire.SessionGitRepository
			for _, name := range fs.Repos {
				out = append(out, wire.SessionGitRepository{GitSourceResourceID: "src_1", Host: "github.com", CanonicalPath: name})
			}
			reply(200, map[string]any{"repositories": out})
		case parts[2] == "messages":
			msg := api.PendingMessage{RequestID: fmt.Sprint(body["request_id"]), Text: fmt.Sprint(body["text"]), Kind: "inbox", CreatedAt: time.Now()}
			ids, _ := body["media_ids"].([]any)
			for _, id := range ids {
				msg.Media = append(msg.Media, f.refs[fmt.Sprint(id)])
			}
			fs.Pending = append(fs.Pending, msg)
			fs.S.PendingTurn = true
			session := fs.S
			unlock = false
			f.Mu.Unlock()
			reply(202, wire.MessageAdmission{Session: &session})
			if f.pace > 0 {
				go f.respond(session.SessionID, msg)
			} else {
				f.respond(session.SessionID, msg)
			}
		case parts[2] == "cancel":
			if t := fs.S.CurrentTurnID; t != "" {
				fs.Finish(t, wire.TurnStatusCancelled, nil, time.Now())
			}
			reply(200, map[string]any{})
		case parts[2] == "archive":
			fs.S.ArchivedAt = time.Now()
			reply(200, fs.S)
		case parts[2] == "hydrate":
			fs.S.MachineStatus = wire.MachineStatusHydrating
			reply(202, map[string]any{})
		case parts[2] == "shares":
			reply(201, api.Share{URL: f.Srv.URL + "/s/abc", ExpiresAt: time.Now().Add(24 * time.Hour)})
		case parts[2] == "proposals" && len(parts) == 5:
			var seq int64
			fmt.Sscan(parts[3], &seq)
			v := fs.Proposals[seq]
			status := map[string]string{"approve": "approved", "dismiss": "dismissed"}[parts[4]]
			v.Decision = &wire.SessionProposalDecision{Sequence: seq, Status: status, CreatedAt: time.Now()}
			if status == "approved" {
				id := fmt.Sprintf("ses_new%d", len(f.List))
				n := &Session{S: wire.Session{SessionID: id, Name: v.Proposal.Title, ProviderResourceID: fs.S.ProviderResourceID, Provider: fs.S.Provider, Model: fs.S.Model,
					MachineStatus: wire.MachineStatusHydrating, ProjectID: fs.S.ProjectID, CreatedAt: time.Now(), UpdatedAt: time.Now()}, Turns: map[string]wire.Turn{}, Proposals: map[int64]wire.SessionProposalView{}}
				f.List = append(f.List, n)
				v.Decision.DestinationID = id
			}
			fs.Proposals[seq] = v
			reply(200, v.Decision)
		case parts[2] == "prompt-media" && len(parts) == 3:
			id := fmt.Sprint(body["media_id"])
			f.refs[id] = wire.PromptMediaRef{MediaID: id, Filename: fmt.Sprint(body["filename"]), MediaType: fmt.Sprint(body["media_type"])}
			reply(200, map[string]any{"media": wire.PromptMedia{MediaID: id}, "upload": map[string]any{"method": "PUT", "url": f.Srv.URL + "/upload/" + id}})
		case parts[2] == "prompt-media" && len(parts) == 5 && parts[4] == "complete":
			reply(200, map[string]any{})
		case parts[2] == "prompt-media" && len(parts) == 5 && parts[4] == "view":
			reply(200, map[string]any{"url": f.Srv.URL + "/blob/" + parts[3]})
		case parts[2] == "screenshots":
			reply(200, map[string]any{"url": f.Srv.URL + "/blob/shot"})
		default:
			fail(404, "not_found", "no route "+path)
		}
	default:
		fail(404, "not_found", "no route "+path)
	}
}

func (f *API) sync(fs *Session, r *http.Request, reply func(int, any)) {
	q := r.URL.Query()
	var afterEvent int64
	fmt.Sscan(q.Get("after_event"), &afterEvent)
	out := api.Sync{Session: fs.S, PendingMessages: fs.Pending}
	if q.Has("after_transcript") {
		var after int64
		fmt.Sscan(q.Get("after_transcript"), &after)
		for _, it := range fs.Items {
			if it.Sequence > after {
				out.Items = append(out.Items, it)
			}
		}
		out.TranscriptNextCursor = after
	} else {
		out.Items = fs.Items
		if len(fs.Items) > 0 {
			out.TranscriptBeforeCursor = fs.Items[0].Sequence
		}
	}
	if n := len(out.Items); n > 0 {
		out.TranscriptNextCursor = out.Items[n-1].Sequence
	}
	for _, e := range fs.Events {
		if e.Sequence > afterEvent {
			out.Events = append(out.Events, e)
		}
	}
	out.EventNextCursor = max(afterEvent, fs.Ev)
	for _, t := range fs.Turns {
		out.Turns = append(out.Turns, t)
	}
	for _, p := range fs.Proposals {
		out.Proposals = append(out.Proposals, p)
	}
	reply(200, out)
}

func (f *API) socket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ch := make(chan api.Dirty, 64)
	f.Mu.Lock()
	f.socks[ch] = true
	f.Mu.Unlock()
	defer func() { f.Mu.Lock(); delete(f.socks, ch); f.Mu.Unlock() }()
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

func Usage() wire.UsageReport {
	r := wire.UsageReport{}
	for i := 0; i < 30; i++ {
		cost := int64((0.12 + 0.2*hash(i, 7) + 0.08*math.Sin(float64(i)/3)) * 1e9)
		m := wire.UsageMetrics{InputTokens: 2_000_000, CachedInputTokens: 1_200_000, ProcessedTokens: 2_400_000,
			ActualCost: wire.CostMetrics{ProviderReportedNanodollars: cost}, APIEquivalentCost: wire.CostMetrics{ProviderReportedNanodollars: cost * 60}}
		r.Daily = append(r.Daily, wire.UsageDay{Date: Start.AddDate(0, 0, i-29).Format("2006-01-02"), Metrics: m})
		r.Totals.InputTokens += m.InputTokens
		r.Totals.CachedInputTokens += m.CachedInputTokens
		r.Totals.ProcessedTokens += m.ProcessedTokens
		r.Totals.ActualCost.ProviderReportedNanodollars += cost
		r.Totals.APIEquivalentCost.ProviderReportedNanodollars += cost * 60
	}
	share := func(part float64) wire.UsageMetrics {
		t := r.Totals
		return wire.UsageMetrics{InputTokens: int64(float64(t.InputTokens) * part), ProcessedTokens: int64(float64(t.ProcessedTokens) * part),
			ActualCost: wire.CostMetrics{ProviderReportedNanodollars: int64(float64(t.ActualCost.Nanodollars()) * part)}}
	}
	r.Providers = []wire.UsageBucket{
		{ProviderResourceID: "prov_claude", ProviderResourceName: "Claude Max", Provider: "claude", Metrics: share(0.7)},
		{ProviderResourceID: "prov_openai", ProviderResourceName: "OpenAI", Provider: "openai", Metrics: share(0.3)},
	}
	r.Models = []wire.UsageBucket{
		{Provider: "claude", Model: "claude-opus-5", Metrics: share(0.6)},
		{Provider: "claude", Model: "claude-sonnet-5", Metrics: share(0.1)},
		{Provider: "openai", Model: "gpt-5.5", Metrics: share(0.3)},
	}
	return r
}

// picture draws a stand-in for a guest display: a desktop with a window.
func Picture(w, h, seed int) []byte {
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

// long adds a session with a long history: many exchanges, each a run of
// work with long outputs and a reply with code, as real sessions grow.
func (f *API) Long(exchanges int) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	at := Start.Add(-48 * time.Hour)
	fs := &Session{S: wire.Session{SessionID: "ses_long", Name: "A long session", ProviderResourceID: "prov_claude", Provider: "claude", Model: "claude-opus-5",
		MachineStatus: wire.MachineStatusHydrated, ContextWindowTokens: 200_000, ProjectID: "proj_acme", CreatedAt: at, UpdatedAt: at},
		Turns: map[string]wire.Turn{}, Proposals: map[int64]wire.SessionProposalView{}}
	output := strings.Repeat("ok  \tacme/pkg/something\t0.123s\n", 200)
	for i := range exchanges {
		fs.User(fmt.Sprintf("Step %d: look at the failing build and fix what you find. Keep the change small.", i), at, nil)
		t := fs.Turn(fmt.Sprintf("turn_%d", i), at)
		for j := range 12 {
			at = at.Add(10 * time.Second)
			fs.Think(t, "Check what the build says before changing anything, then look at the package it names.", at)
			fs.Tool(t, "bash", map[string]any{"command": fmt.Sprintf("go test ./... -run Test%d", j)}, map[string]any{"exit_code": 0, "output": output}, at, 2*time.Second)
			fs.Tool(t, "read", map[string]any{"file_path": "/workspace/web/checkout/cart.go"}, map[string]any{"output": output}, at, 100*time.Millisecond)
		}
		fs.Reply(t, "Fixed. The build broke because **cart.go** read `total` without the lock.\n\n```go\nfunc (c *Cart) Total() int {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\treturn c.total\n}\n```\n\n- one\n- two\n- three", at)
		fs.Finish(t, wire.TurnStatusCompleted, nil, at)
	}
	f.List = append(f.List, fs)
}

// hash is a repeatable pseudo-random number in [0, 1) for x and y.
func hash(x, y int) float64 {
	h := uint32(x*374761393 + y*668265263)
	h = (h ^ (h >> 13)) * 1274126177
	return float64(h^(h>>16)) / float64(1<<32)
}

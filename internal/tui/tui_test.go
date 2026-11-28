package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/fakeapi"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/wire"
)

func setupApp(t *testing.T, opts Options) (*app, *fakeapi.API) {
	t.Helper()
	f := fakeapi.New(0)
	t.Cleanup(f.Srv.Close)
	open := OpenURL
	OpenURL = func(string) error { return nil }
	t.Cleanup(func() { OpenURL = open })
	c := f.SignIn(t, t.TempDir())
	a := newApp(c, opts)
	a.w, a.h = 100, 32
	// Drive initial commands synchronously.
	runCmd(a, a.list())
	runCmd(a, a.loadAgents())
	runCmd(a, a.loadProjects())
	return a, f
}

func runCmd(a *app, cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmd(a, c)...)
		}
		return out
	}
	_, next := a.Update(msg)
	out := []tea.Msg{msg}
	if next != nil {
		// Avoid infinite tick loops.
		if _, isTick := msg.(tickMsg); !isTick {
			out = append(out, runCmd(a, next)...)
		}
	}
	return out
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+p":
		return tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	case "ctrl+t":
		return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	case "ctrl+n":
		return tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func typeStr(a *app, s string) {
	for _, r := range s {
		_, cmd := a.Update(press(string(r)))
		runCmd(a, cmd)
	}
}

func TestPrimaryBufferInlineAndNoMouseCapture(t *testing.T) {
	a, _ := setupApp(t, Options{SessionQuery: "checkout"})
	if a.conv == nil || a.conv.id != "ses_checkout" {
		t.Fatalf("expected direct attach to ses_checkout, got %+v", a.conv)
	}
	v := a.View()
	if v.AltScreen {
		t.Fatal("TUI must run in primary buffer (AltScreen=false), not alternate screen")
	}
	if v.MouseMode != tea.MouseModeNone {
		t.Fatalf("TUI must not capture mouse (got %v)", v.MouseMode)
	}
	if !strings.Contains(v.WindowTitle, "Fix the flaky checkout test") {
		t.Fatalf("unexpected window title: %q", v.WindowTitle)
	}
	plain := ansi.Strip(v.Content)
	if !strings.Contains(plain, "Fix the flaky checkout test") || !strings.Contains(plain, "claude/claude-opus-5/high") || !strings.Contains(plain, "ctx 30%") || !strings.Contains(plain, "awake") {
		t.Fatalf("footer missing expected metadata:\n%s", plain)
	}
	// Settled exchanges should have been flushed cleanly to primary scrollback.
	if a.flushed == 0 {
		t.Fatal("expected settled exchanges to be flushed via tea.Println")
	}
}

func TestV2InPlaceStreamingMergeAndFileChange(t *testing.T) {
	c := newConversation(wire.Session{
		SessionID: "ses_1", Agent: "codex", RuntimeVersion: 2, Model: "gpt-5.5", CurrentTurnID: "t1",
	})
	// First sync: streaming text item at seq 2 + file_change array tool call at seq 3.
	c.applySync(&api.Sync{
		Session: c.session,
		Items: []wire.TranscriptItem{
			{Sequence: 1, Kind: wire.ItemKindUser, Payload: json.RawMessage(`{"text":"Fix bug"}`), CreatedAt: time.Now()},
			{Sequence: 2, TurnID: "t1", Kind: wire.ItemKindAssistant, Payload: json.RawMessage(`{"text":"Hello"}`), Streaming: true, CreatedAt: time.Now()},
			{Sequence: 3, TurnID: "t1", Kind: wire.ItemKindToolCall, ToolCallID: "tc1", Payload: json.RawMessage(`{"kind":"file_change","arguments":[{"path":"main.go","kind":"update"}]}`), CreatedAt: time.Now()},
			{Sequence: 4, TurnID: "t1", Kind: wire.ItemKindToolResult, ToolCallID: "tc1", Payload: json.RawMessage(`{"result":{"outcome":"success","output":"@@ -1 +1 @@\n-old\n+new"}}`), CreatedAt: time.Now()},
		},
		TranscriptNextCursor: 1,
	})
	exs := c.exchanges()
	if len(exs) != 1 || !exs[0].moving {
		t.Fatalf("expected 1 live moving exchange, got %+v", exs)
	}
	// Second sync: SAME sequence 2 updated in place with more streamed text, then settled.
	c.session.CurrentTurnID = ""
	c.applySync(&api.Sync{
		Session: c.session,
		Turns:   []wire.Turn{{TurnID: "t1", Status: wire.TurnStatusCompleted, CompletedAt: time.Now()}},
		Items: []wire.TranscriptItem{
			{Sequence: 2, TurnID: "t1", Kind: wire.ItemKindAssistant, Payload: json.RawMessage(`{"text":"Hello world, all fixed!"}`), Streaming: false, CreatedAt: time.Now()},
		},
		TranscriptNextCursor: 4,
	})
	exs = c.exchanges()
	if exs[0].moving {
		t.Fatal("expected settled exchange to stop moving")
	}
	rendered := ansi.Strip(strings.Join(renderExchange(c, exs[0], 90, prefs.WorkFull, 0, time.Now(), ""), "\n"))
	if !strings.Contains(rendered, "Hello world, all fixed!") {
		t.Fatalf("in-place streaming update at seq 2 was dropped:\n%s", rendered)
	}
	if !strings.Contains(rendered, "file_change main.go (update)") || !strings.Contains(rendered, "+new") {
		t.Fatalf("expected file_change array args + patch diff:\n%s", rendered)
	}
}

func TestOSC7501PerPaneStatusLifecycle(t *testing.T) {
	var st statusTracker
	// Working
	c := newConversation(wire.Session{SessionID: "s1", Name: "API work", Agent: "claude", RuntimeVersion: 2, CurrentTurnID: "t1"})
	ps := st.compute(c)
	if ps.State != ansi.ProgramStateWorking || ps.App != "barad" || ps.Title != "API work" {
		t.Fatalf("working status = %+v", ps)
	}
	// Auth Gate -> blocked:auth
	unauth := false
	c.session.CurrentTurnID = ""
	c.session.AgentAuthenticated = &unauth
	ps = st.compute(c)
	if ps.State != ansi.ProgramStateBlocked || ps.Kind != ansi.ProgramStatusKindAuth {
		t.Fatalf("auth blocked status = %+v", ps)
	}
	// Completed -> done, then ackUserInput -> idle
	auth := true
	c.session.AgentAuthenticated = &auth
	c.session.LatestTerminalTurnID = "t1"
	c.session.LatestTerminalTurnStatus = wire.TurnStatusCompleted
	st.markTurnCompleted("t1")
	if ps = st.compute(c); ps.State != ansi.ProgramStateDone {
		t.Fatalf("done status = %+v", ps)
	}
	st.ackUserInput()
	if ps = st.compute(c); ps.State != ansi.ProgramStateIdle {
		t.Fatalf("idle status after ack = %+v", ps)
	}
	// Failed -> error
	c.session.LatestTerminalTurnStatus = wire.TurnStatusFailed
	if ps = st.compute(c); ps.State != ansi.ProgramStateError {
		t.Fatalf("error status = %+v", ps)
	}
}

func TestSlashCommandsAndOverlays(t *testing.T) {
	a, _ := setupApp(t, Options{})
	// Default launch opens fuzzy session picker.
	if _, ok := a.ov.(*pickerOverlay); !ok {
		t.Fatalf("expected pickerOverlay on default launch, got %T", a.ov)
	}
	pickerView := ansi.Strip(a.View().Content)
	if !strings.Contains(pickerView, "Fix the flaky checkout test") || !strings.Contains(pickerView, "+ New session") {
		t.Fatalf("picker missing sessions or + New session:\n%s", pickerView)
	}
	// Filter and attach to Codex session ("Upgrade the payments SDK").
	typeStr(a, "payments")
	_, cmd := a.Update(press("enter"))
	runCmd(a, cmd)
	if a.conv == nil || a.conv.id != "ses_sdk" {
		t.Fatalf("expected attach to ses_sdk, got %+v", a.conv)
	}

	// Open /model overlay and verify Codex fast_mode + custom model ID appear.
	typeStr(a, "/model")
	_, cmd = a.Update(press("enter"))
	runCmd(a, cmd)
	modelView := ansi.Strip(a.View().Content)
	if !strings.Contains(modelView, "Model & Reasoning") || !strings.Contains(modelView, "Codex fast mode") || !strings.Contains(modelView, "Custom model ID") {
		t.Fatalf("/model overlay missing expected options:\n%s", modelView)
	}
	_, _ = a.Update(press("esc"))

	// Open /new modal and verify harnesses + sizes.
	typeStr(a, "/new")
	_, cmd = a.Update(press("enter"))
	runCmd(a, cmd)
	newView := ansi.Strip(a.View().Content)
	if !strings.Contains(newView, "New session") || !strings.Contains(newView, "Harness") || !strings.Contains(newView, "Size") {
		t.Fatalf("/new overlay missing expected fields:\n%s", newView)
	}
	_, _ = a.Update(press("esc"))

	// Test /settings, /tools, /projects, /usage, /billing, /account, /files.
	for _, slash := range []string{"/settings", "/tools", "/projects", "/usage", "/billing", "/account", "/files"} {
		typeStr(a, slash)
		_, cmd = a.Update(press("enter"))
		runCmd(a, cmd)
		if a.ov == nil {
			t.Fatalf("%s did not open overlay", slash)
		}
		// Render at narrow Rex split width (34 cols) to verify zero panics & graceful truncation.
		a.w = 34
		_ = a.View()
		a.w = 100
		_, _ = a.Update(press("esc"))
	}
}

func TestAuthGateBannerAndQueuedPrompt(t *testing.T) {
	a, _ := setupApp(t, Options{SessionQuery: "checkout"})
	unauth := false
	a.conv.session.AgentAuthenticated = &unauth
	a.conv.session.CurrentTurnID = ""
	a.conv.pending = []api.PendingMessage{{RequestID: "r1", Text: "Run tests after login", Kind: "inbox"}}
	plain := ansi.Strip(a.View().Content)
	if !strings.Contains(plain, "Sign-in required") || !strings.Contains(plain, "/login") {
		t.Fatalf("expected Auth Gate banner:\n%s", plain)
	}
	if !strings.Contains(plain, "Queued · runs after /login") {
		t.Fatalf("expected queued prompt banner:\n%s", plain)
	}
	if !strings.Contains(plain, "sign-in required") {
		t.Fatalf("expected footer sign-in required state:\n%s", plain)
	}
}

func TestPickerGroupsSpaceReadToggleAndDefaultModelDisplay(t *testing.T) {
	a, _ := setupApp(t, Options{})
	// Inject a discovered model on agy ("gemini-4-neon") with DefaultModel="default".
	a.agents = append(a.agents[:3], wire.AgentSpec{
		ID: "agy", Label: "Antigravity", DefaultModel: "default",
		Models: []wire.AgentModelSpec{
			{ID: "default", Label: "CLI Default", SupportsReasoningEffort: true, Efforts: []wire.ReasoningEffort{"low", "high"}, DefaultEffort: "high"},
			{ID: "gemini-4-neon", Label: "Gemini 4 Neon", SupportsReasoningEffort: true, Efforts: []wire.ReasoningEffort{"low", "high"}, DefaultEffort: "high"},
		},
	})

	// 1. Picker shows explicit group headers so sort order is obvious.
	pickerView := ansi.Strip(a.View().Content)
	if !strings.Contains(pickerView, "NEEDS YOU") || !strings.Contains(pickerView, "RUNNING") || !strings.Contains(pickerView, "RESTING") {
		t.Fatalf("picker missing explicit group headers:\n%s", pickerView)
	}

	// 2. Pressing Space with empty filter toggles read/unread on the selected session.
	p := a.ov.(*pickerOverlay)
	rows := p.filtered(a)
	p.sel = len(rows) - 1 // pick a RESTING session with a finished turn
	target := rows[p.sel]
	if api.Unread(target, a.prefs.Reviewed[target.SessionID]) {
		t.Fatalf("expected %s to start read", target.SessionID)
	}
	_, _ = a.Update(press("space"))
	if !api.Unread(target, a.prefs.Reviewed[target.SessionID]) {
		t.Fatalf("expected Space to mark %s unread", target.SessionID)
	}
	// Mark it read again with Space.
	rows = p.filtered(a)
	for i, s := range rows {
		if s.SessionID == target.SessionID {
			p.sel = i
			break
		}
	}
	_, _ = a.Update(press("space"))
	if api.Unread(target, a.prefs.Reviewed[target.SessionID]) {
		t.Fatalf("expected second Space to mark %s read again", target.SessionID)
	}

	// 3. Default model displays actual resolved model name in parentheses.
	a.ov = nil
	a.toast = ""
	a.conv = newConversation(wire.Session{
		SessionID: "ses_agy", Name: "Agy test", Agent: "agy", RuntimeVersion: 2, Model: "default", ReasoningEffort: "high", MachineStatus: wire.MachineStatusHydrated,
	})
	footer := ansi.Strip(a.View().Content)
	if !strings.Contains(footer, "agy/default (Gemini 4 Neon)/high") {
		t.Fatalf("footer did not show resolved default model:\n%s", footer)
	}
	// Verify single empty line between transcript and composer divider.
	lines := strings.Split(footer, "\n")
	if len(lines) < 3 || lines[0] != "" || !strings.HasPrefix(lines[1], "────") {
		t.Fatalf("expected single empty line above composer divider, got lines=%q", lines)
	}
	// Verify /model shows "CLI Default (Gemini 4 Neon)".
	a.openModelOverlay()
	modelView := ansi.Strip(a.View().Content)
	if !strings.Contains(modelView, "CLI Default (Gemini 4 Neon)") {
		t.Fatalf("/model did not show resolved default model in parens:\n%s", modelView)
	}
}

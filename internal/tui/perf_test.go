package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/barad-io/barad/wire"
)

// long adds a session with a long history: many exchanges, each a run of
// work with long outputs and a reply with code, as real sessions grow.
func (f *fakeAPI) long(exchanges int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	at := fakeStart.Add(-48 * time.Hour)
	fs := &fakeSession{s: wire.Session{SessionID: "ses_long", Name: "A long session", ProviderResourceID: "prov_claude", Provider: "claude", Model: "claude-opus-5",
		MachineStatus: wire.MachineStatusHydrated, ContextWindowTokens: 200_000, ProjectID: "proj_acme", CreatedAt: at, UpdatedAt: at},
		turns: map[string]wire.Turn{}, proposals: map[int64]wire.SessionProposalView{}}
	output := strings.Repeat("ok  \tacme/pkg/something\t0.123s\n", 200)
	for i := range exchanges {
		fs.user(fmt.Sprintf("Step %d: look at the failing build and fix what you find. Keep the change small.", i), at, nil)
		t := fs.turn(fmt.Sprintf("turn_%d", i), at)
		for j := range 12 {
			at = at.Add(10 * time.Second)
			fs.think(t, "Check what the build says before changing anything, then look at the package it names.", at)
			fs.tool(t, "bash", map[string]any{"command": fmt.Sprintf("go test ./... -run Test%d", j)}, map[string]any{"exit_code": 0, "output": output}, at, 2*time.Second)
			fs.tool(t, "read", map[string]any{"file_path": "/workspace/web/checkout/cart.go"}, map[string]any{"output": output}, at, 100*time.Millisecond)
		}
		fs.reply(t, "Fixed. The build broke because **cart.go** read `total` without the lock.\n\n```go\nfunc (c *Cart) Total() int {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\treturn c.total\n}\n```\n\n- one\n- two\n- three", at)
		fs.finish(t, wire.TurnStatusCompleted, nil, at)
	}
	f.list = append(f.list, fs)
}

// TestTypingStaysFast holds the app to a frame budget: a key press in a long
// conversation, and its frame, take well under a frame at 60 Hz.
func TestTypingStaysFast(t *testing.T) {
	d := newDriver(t)
	d.f.long(150)
	d.run(d.a.list())
	d.openSession("long session")
	d.sees("A long session")
	d.screen()
	type sample struct{ update, view time.Duration }
	var worst sample
	var total time.Duration
	const n = 60
	for i := range n {
		began := time.Now()
		d.a.Update(tea.KeyPressMsg{Code: 'a' + rune(i%26), Text: string('a' + rune(i%26))})
		mid := time.Now()
		d.a.View()
		s := sample{mid.Sub(began), time.Since(mid)}
		total += s.update + s.view
		if s.update+s.view > worst.update+worst.view {
			worst = s
		}
		d.a.Update(tickMsg(time.Now())) // a frame between keys, as the program draws
		d.a.View()
	}
	mean := total / n
	t.Logf("key to frame: mean %v, worst %v (update %v, view %v)", mean, worst.update+worst.view, worst.update, worst.view)
	if mean > 4*time.Millisecond {
		t.Errorf("a key press takes %v to draw on average", mean)
	}

	// A sync changes the conversation: the exchanges are derived again, and
	// the frame after it redraws only what changed.
	began := time.Now()
	for range 10 {
		d.a.conv.changed()
		d.a.View()
	}
	synced := time.Since(began) / 10
	t.Logf("frame after a sync: %v", synced)
	if synced > 25*time.Millisecond {
		t.Errorf("a frame after a sync takes %v", synced)
	}

	// The other screens draw within the budget too.
	for _, keys := range [][]string{{"esc"}, {"?"}, {"esc", "n"}} {
		d.keys(keys...)
		began := time.Now()
		for range 10 {
			d.a.Update(tickMsg(time.Now()))
			d.a.View()
		}
		frame := time.Since(began) / 10
		t.Logf("after %v: %v a frame", keys, frame)
		if frame > 4*time.Millisecond {
			t.Errorf("after %v a frame takes %v", keys, frame)
		}
	}
}

package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestTypingStaysFast holds the app to a frame budget: a key press in a long
// conversation, and its frame, take under half a frame at 60 Hz (8 ms), even
// on a slow CI runner; a frame after a sync, under a frame. Drawing the whole
// transcript every frame took 56 ms here.
func TestTypingStaysFast(t *testing.T) {
	d := newDriver(t)
	d.f.Long(150)
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
	if mean > 8*time.Millisecond {
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
	if synced > 16*time.Millisecond {
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
		if frame > 8*time.Millisecond {
			t.Errorf("after %v a frame takes %v", keys, frame)
		}
	}
}

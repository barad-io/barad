package shell

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barad-io/barad/internal/fakeapi"
	"github.com/barad-io/barad/wire"
)

// syncBuffer is what the shell printed, read while it prints.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestAShellInASessionsComputer(t *testing.T) {
	f := fakeapi.New(0)
	t.Cleanup(f.Srv.Close)
	client := f.SignIn(t, t.TempDir())
	keys, typing, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	done := make(chan error, 1)
	var status int
	go func() {
		var err error
		status, err = Run(context.Background(), client, "ses_checkout", keys, &out)
		done <- err
	}()
	waitFor := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), want); {
			if time.Now().After(deadline) {
				t.Fatalf("the shell never showed %q:\n%s", want, out.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("you@workspace:~$ ")
	activity := func() []string {
		f.Mu.Lock()
		defer f.Mu.Unlock()
		return slices.Clone(f.Activity)
	}
	// The terminal answering a program is not someone at work; typing is.
	_, _ = typing.WriteString("\x1b[12;40R")
	time.Sleep(100 * time.Millisecond)
	if got := activity(); len(got) != 0 {
		t.Fatalf("a terminal's reply kept the session awake: %v", got)
	}
	_, _ = typing.WriteString("ls -la\r")
	waitFor("you said")
	for deadline := time.Now().Add(5 * time.Second); len(activity()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("typing did not keep the session awake")
		}
	}
	_, _ = typing.WriteString("exit\r")
	select {
	case err := <-done:
		if err != nil || status != 0 {
			t.Fatalf("status %d, %v", status, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shell did not end")
	}
	waitFor("logout")

	// An asleep session has no terminal until it wakes.
	if _, err := Run(context.Background(), client, "ses_search", keys, &out); err == nil || !strings.Contains(err.Error(), "hydrated") {
		t.Fatalf("an asleep session's shell: %v", err)
	}
	if err := Wake(context.Background(), client, wire.Session{SessionID: "ses_search", ArchivedAt: time.Now()}, &out); err == nil {
		t.Fatal("an archived session woke")
	}
}

func TestOnlyAPersonsInputIsActivity(t *testing.T) {
	for in, want := range map[string]bool{
		"l":                              true,
		"ls -la\r":                       true,
		"\x1b[A":                         true,  // up
		"\x1b[15~":                       true,  // F5
		"\x1bOP":                         true,  // F1
		"\x1bb":                          true,  // Alt+b
		"\x1b":                           true,  // Esc
		"\x1b[<0;10;5M":                  true,  // a click
		"\x1b[<64;10;5M":                 true,  // the wheel
		"\x1b[M !!":                      true,  // a click, the older encoding
		"\x1b[12;40R":                    false, // where the cursor is
		"\x1b[?62;22c":                   false, // what the terminal is
		"\x1b[0n":                        false, // its status
		"\x1b[8;24;80t":                  false, // its size
		"\x1b[?2004;2$y":                 false, // a mode
		"\x1b[I":                         false, // focus gained
		"\x1b[O":                         false, // focus lost
		"\x1b]11;rgb:1111/1111/1111\x07": false, // its background colour
		"\x1bP>|kitty(0.36)\x1b\\":       false, // its name
		"\x1b[<35;10;5M":                 false, // the pointer moving
		"\x1b[M@!!":                      false, // the pointer moving, the older encoding
		"\x1b[I\x1b[12;40R":              false,
		"\x1b[12;40Rx":                   true, // a reply, then a key
	} {
		if got := fromPerson([]byte(in)); got != want {
			t.Errorf("fromPerson(%q) = %v, want %v", in, got, want)
		}
	}
}

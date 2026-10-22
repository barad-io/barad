package shell

import (
	"bytes"
	"context"
	"os"
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
	_, _ = typing.WriteString("ls -la\r")
	waitFor("you said ls -la")
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

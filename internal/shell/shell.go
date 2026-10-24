// Package shell is a login shell in a session's computer, in this terminal:
// what the web app's Terminal tab is, without the browser. barad makes an
// ECDH P-256 key pair and asks the API for a terminal; the API starts the
// shell and returns Jinn's relay, a route and the computer's half of the key.
// Both ends derive the channel's keys (usejinn.com/go), so Barad and the
// relay carry only ciphertext.
package shell

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
	jinn "usejinn.com/go"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Wake wakes an asleep session and waits until its computer is ready, saying
// so on out. A session already awake returns at once.
func Wake(ctx context.Context, client *api.Client, s wire.Session, out io.Writer) error {
	if s.MachineStatus == wire.MachineStatusHydrated {
		return nil
	}
	if !s.ArchivedAt.IsZero() {
		return errors.New("an archived session has no computer")
	}
	fmt.Fprintf(out, "Waking %s…\n", s.SessionID)
	if s.MachineStatus != wire.MachineStatusHydrating {
		if err := client.Wake(ctx, s.SessionID); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return errors.New("the session's computer did not wake in 5 minutes")
		case <-time.After(2 * time.Second):
		}
		list, err := client.Sessions(ctx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(l wire.Session) bool { return l.SessionID == s.SessionID })
		if i < 0 {
			return errors.New("the session is gone")
		}
		switch list[i].MachineStatus {
		case wire.MachineStatusHydrated:
			return nil
		case wire.MachineStatusBroken:
			return errors.New("the session's computer is broken")
		}
	}
}

// Run connects this terminal (in, out) to a login shell in an awake
// session's computer, until the shell exits or the connection ends. It
// returns the shell's exit status.
func Run(ctx context.Context, client *api.Client, sessionID string, in *os.File, out io.Writer) (int, error) {
	fd := int(in.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return 0, err
	}
	ticket, err := client.OpenTerminal(ctx, sessionID, jinn.EncodeTerminalKey(private.PublicKey()), rows, cols)
	if err != nil {
		return 0, err
	}
	peer, err := jinn.DecodeTerminalKey(ticket.PublicKey)
	if err != nil {
		return 0, err
	}
	ch, err := jinn.NewTerminalChannel(private, peer, true)
	if err != nil {
		return 0, err
	}
	dial, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dial, ticket.RelayURL+"?route="+url.QueryEscape(ticket.Route)+"&side=caller", nil)
	cancelDial()
	if err != nil {
		return 0, fmt.Errorf("could not reach the terminal relay: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 10)

	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err != nil {
			return 0, err
		}
		defer term.Restore(fd, state)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One writer at a time: a frame's sequence is its place in the stream.
	var mu sync.Mutex
	send := func(kind byte, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		for first := true; first || len(data) > 0; first = false { // a long paste goes in pieces
			n := min(len(data), jinn.MaxTerminalData)
			if err := conn.Write(ctx, websocket.MessageBinary, ch.Seal(kind, data[:n])); err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	}

	// Keys go to the shell. The reader is cancelled when the shell ends, so
	// it leaves stdin to whoever reads it next (the app, after a shell).
	keys, err := cancelreader.NewReader(in)
	if err != nil {
		return 0, err
	}
	defer keys.Cancel()
	go func() {
		buf := make([]byte, 4096)
		var reported time.Time
		for {
			n, err := keys.Read(buf)
			if err != nil {
				return
			}
			if send(jinn.FrameInput, buf[:n]) != nil {
				return
			}
			if time.Since(reported) > time.Minute { // typing keeps the session awake
				reported = time.Now()
				go func() { _ = client.Activity(ctx, sessionID) }()
			}
		}
	}()

	// A window that changes size tells the shell.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			c, r, err := term.GetSize(fd)
			if err != nil || c == cols && r == rows {
				continue
			}
			cols, rows = c, r
			size := make([]byte, 4)
			binary.BigEndian.PutUint16(size, uint16(rows))
			binary.BigEndian.PutUint16(size[2:], uint16(cols))
			if send(jinn.FrameResize, size) != nil {
				return
			}
		}
	}()

	for {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			return 0, errors.New("the terminal disconnected")
		}
		kind, data, err := ch.Open(frame)
		if err != nil {
			return 0, err
		}
		switch kind {
		case jinn.FrameOutput:
			if _, err := out.Write(data); err != nil {
				return 0, err
			}
		case jinn.FrameExit:
			status := 0
			if len(data) >= 4 {
				status = int(int32(binary.BigEndian.Uint32(data)))
			}
			conn.Close(websocket.StatusNormalClosure, "")
			return status, nil
		default:
			return 0, fmt.Errorf("the terminal sent a frame of kind %d", kind)
		}
	}
}

// Command is a session's shell as the app runs it in its own terminal
// (tea.Exec): the app steps aside, the shell runs until it exits, and the
// app comes back.
type Command struct {
	Client  *api.Client
	Session wire.Session
	in      io.Reader
	out     io.Writer
}

func (c *Command) SetStdin(r io.Reader)  { c.in = r }
func (c *Command) SetStdout(w io.Writer) { c.out = w }
func (c *Command) SetStderr(io.Writer)   {}

func (c *Command) Run() error {
	in, ok := c.in.(*os.File)
	if !ok {
		in = os.Stdin
	}
	ctx := context.Background()
	name := c.Session.Name
	if name == "" {
		name = c.Session.SessionID
	}
	fmt.Fprintf(c.out, "\r\n%s: a shell in its computer. Type exit to come back to barad.\r\n\r\n", name)
	if err := Wake(ctx, c.Client, c.Session, c.out); err != nil {
		return err
	}
	_, err := Run(ctx, c.Client, c.Session.SessionID, in, c.out)
	return err
}

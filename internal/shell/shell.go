// Package shell connects this terminal to a session's remote shell over Barad's
// terminal relay WebSocket. Each binary WebSocket message is a 1-byte frame kind
// followed by payload bytes.
package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Binary terminal frame kinds exchanged over Barad's terminal WebSocket.
const (
	FrameInput  byte = 1 // keystrokes, client to server
	FrameOutput byte = 2 // terminal output, server to client
	FrameResize byte = 3 // rows and cols (two big-endian uint16s), client to server
	FrameExit   byte = 4 // exit status (one big-endian int32), server to client
)

// maxFrameData bounds one frame's payload: the relay reads messages up to 64 KiB.
const maxFrameData = 32 << 10

// EncodeFrame encodes one binary terminal frame ([kind][data]).
func EncodeFrame(kind byte, data []byte) []byte {
	out := make([]byte, 1+len(data))
	out[0] = kind
	copy(out[1:], data)
	return out
}

// DecodeFrame splits a binary terminal WebSocket message into kind and payload.
func DecodeFrame(frame []byte) (byte, []byte, error) {
	if len(frame) == 0 {
		return 0, nil, errors.New("empty terminal frame")
	}
	return frame[0], frame[1:], nil
}

// Wake wakes an asleep session and waits until its machine is ready, writing
// status lines to out. A session already awake returns immediately.
func Wake(ctx context.Context, client *api.Client, s wire.Session, out io.Writer) error {
	if s.MachineStatus == wire.MachineStatusHydrated {
		return nil
	}
	if !s.ArchivedAt.IsZero() || s.MachineStatus == wire.MachineStatusArchived {
		return errors.New("an archived session has no workspace machine")
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
			return errors.New("the session's machine did not wake in 5 minutes")
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
			return errors.New("the session's machine is broken")
		}
	}
}

// Run connects this terminal (in, out) to a login shell in an awake session's
// machine until the shell exits or the connection ends.
func Run(ctx context.Context, client *api.Client, sessionID string, in *os.File, out io.Writer) (int, error) {
	return RunInit(ctx, client, sessionID, in, out, "")
}

// RunInit connects this terminal (in, out) to a login shell and, if initCmd is
// non-empty, sends it once the shell attaches. Used for interactive `/login`.
func RunInit(ctx context.Context, client *api.Client, sessionID string, in *os.File, out io.Writer, initCmd string) (int, error) {
	fd := int(in.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 80, 24
	}
	ticket, err := client.OpenTerminal(ctx, sessionID, rows, cols)
	if err != nil {
		return 0, err
	}
	dial, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dial, ticket.URL, nil)
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

	// One writer at a time, so long pastes stay ordered.
	var mu sync.Mutex
	send := func(kind byte, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		for first := true; first || len(data) > 0; first = false {
			n := min(len(data), maxFrameData)
			if err := conn.Write(ctx, websocket.MessageBinary, EncodeFrame(kind, data[:n])); err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	}

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
			if send(FrameInput, buf[:n]) != nil {
				return
			}
			if fromPerson(buf[:n]) && time.Since(reported) > time.Minute {
				reported = time.Now()
				go func() { _ = client.Activity(ctx, sessionID) }()
			}
		}
	}()

	// Propagate terminal resize events.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			c, r, err := term.GetSize(fd)
			if err != nil || (c == cols && r == rows) {
				continue
			}
			cols, rows = c, r
			size := make([]byte, 4)
			binary.BigEndian.PutUint16(size, uint16(rows))
			binary.BigEndian.PutUint16(size[2:], uint16(cols))
			if send(FrameResize, size) != nil {
				return
			}
		}
	}()

	initSent := initCmd == ""
	for {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			return 0, errors.New("the terminal disconnected")
		}
		kind, data, err := DecodeFrame(frame)
		if err != nil {
			return 0, err
		}
		switch kind {
		case FrameOutput:
			if !initSent {
				initSent = true
				_ = send(FrameInput, []byte(initCmd+"\r"))
				continue
			}
			if _, err := out.Write(data); err != nil {
				return 0, err
			}
		case FrameExit:
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

// RunCommand executes cmdArgs non-interactively inside an awake session's
// machine over Barad's terminal relay, writing clean command output to out and
// returning the command's exit status. Works in scripts and pipes without a TTY.
func RunCommand(ctx context.Context, client *api.Client, sessionID string, cmdArgs []string, out io.Writer) (int, error) {
	if len(cmdArgs) == 0 {
		return 0, errors.New("missing command")
	}
	ticket, err := client.OpenTerminal(ctx, sessionID, 24, 240)
	if err != nil {
		return 0, err
	}
	dial, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dial, ticket.URL, nil)
	cancelDial()
	if err != nil {
		return 0, fmt.Errorf("could not reach the terminal relay: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 10)

	var tokBytes [6]byte
	_, _ = rand.Read(tokBytes[:])
	tok := hex.EncodeToString(tokBytes[:])
	beginMark := "__BARAD_BEGIN_" + tok + "__"
	endMark := "__BARAD_END_" + tok + "__:"

	quoted := make([]string, len(cmdArgs))
	for i, a := range cmdArgs {
		quoted[i] = shellQuote(a)
	}
	cmdline := strings.Join(quoted, " ")
	script := fmt.Sprintf("stty -echo 2>/dev/null; printf '%s\\n'; (%s); __ec=$?; printf '\\n%s%%d\\n' \"$__ec\"; exit \"$__ec\"\r",
		beginMark, cmdline, endMark)

	sent := false
	sendScript := func() error {
		if sent {
			return nil
		}
		sent = true
		return conn.Write(ctx, websocket.MessageBinary, EncodeFrame(FrameInput, []byte(script)))
	}

	var buf bytes.Buffer
	exitCode := 0
	for {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			break
		}
		kind, data, err := DecodeFrame(frame)
		if err != nil {
			return 0, err
		}
		switch kind {
		case FrameOutput:
			if !sent {
				if err := sendScript(); err != nil {
					return 0, err
				}
			}
			buf.Write(data)
		case FrameExit:
			if len(data) >= 4 {
				exitCode = int(int32(binary.BigEndian.Uint32(data)))
			}
			conn.Close(websocket.StatusNormalClosure, "")
			goto done
		}
	}

done:
	raw := strings.ReplaceAll(buf.String(), "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "")
	if idx := strings.LastIndex(raw, beginMark+"\n"); idx >= 0 {
		body := raw[idx+len(beginMark)+1:]
		if endIdx := strings.LastIndex(body, "\n"+endMark); endIdx >= 0 {
			tail := body[endIdx+1+len(endMark):]
			if nl := strings.IndexByte(tail, '\n'); nl >= 0 {
				tail = tail[:nl]
			}
			if parsed, err := strconv.Atoi(strings.TrimSpace(tail)); err == nil {
				exitCode = parsed
			}
			body = body[:endIdx]
		} else if endIdx := strings.LastIndex(body, endMark); endIdx >= 0 {
			body = body[:endIdx]
		}
		if body != "" {
			if !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			_, _ = io.WriteString(out, body)
		}
		return exitCode, nil
	}
	// Fallback (e.g. simple mock relay that doesn't execute sh): print raw output.
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		_, _ = io.WriteString(out, trimmed+"\n")
	}
	return exitCode, nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Command is a session's interactive shell executed via tea.Exec: the TUI steps
// aside, the shell runs in the real terminal until it exits, and the TUI resumes.
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
	fmt.Fprintf(c.out, "\r\n%s — interactive shell. Type exit to return to barad.\r\n\r\n", name)
	if err := Wake(ctx, c.Client, c.Session, c.out); err != nil {
		return err
	}
	_, err := Run(ctx, c.Client, c.Session.SessionID, in, c.out)
	return err
}

// LoginCommand runs a harness's native login CLI inside the session's machine
// via tea.Exec, then reports auth status so queued prompts run automatically.
type LoginCommand struct {
	Client       *api.Client
	Session      wire.Session
	AgentID      string
	LoginCommand string
	in           io.Reader
	out          io.Writer
}

func (c *LoginCommand) SetStdin(r io.Reader)  { c.in = r }
func (c *LoginCommand) SetStdout(w io.Writer) { c.out = w }
func (c *LoginCommand) SetStderr(io.Writer)   {}

func (c *LoginCommand) Run() error {
	in, ok := c.in.(*os.File)
	if !ok {
		in = os.Stdin
	}
	ctx := context.Background()
	agentID := c.AgentID
	if agentID == "" {
		agentID = c.Session.HarnessID()
	}
	loginCmd := strings.TrimSpace(c.LoginCommand)
	if loginCmd == "" {
		loginCmd = DefaultLoginCommand(agentID)
	}
	name := c.Session.Name
	if name == "" {
		name = c.Session.SessionID
	}
	fmt.Fprintf(c.out, "\r\nSigning in to %s for %s…\r\n\r\n", agentID, name)
	if err := Wake(ctx, c.Client, c.Session, c.out); err != nil {
		return err
	}
	initCmd := fmt.Sprintf(" %s; barad-tool auth %s --report; __ec=$?; exit $__ec", loginCmd, shellQuote(agentID))
	status, err := RunInit(ctx, c.Client, c.Session.SessionID, in, c.out, initCmd)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("login exited with status %d", status)
	}
	return nil
}

// DefaultLoginCommand returns the fallback native CLI login command for a v2 harness.
func DefaultLoginCommand(agentID string) string {
	switch strings.ToLower(strings.TrimSpace(agentID)) {
	case "claude":
		return "claude auth login"
	case "codex":
		return "codex login --device-auth"
	case "grok":
		return "grok login --device-code"
	case "agy":
		return "agy acp --login"
	default:
		return "claude auth login"
	}
}

// fromPerson reports whether terminal input holds something a person sent:
// a key, a paste, a click. Terminal replies (cursor position, attributes,
// focus, mouse motion) must not keep a session awake.
func fromPerson(in []byte) bool {
	for i := 0; i < len(in); i++ {
		if in[i] != 0x1b || i+1 >= len(in) {
			return true
		}
		switch in[i+1] {
		case ']', 'P', '_', '^': // OSC, DCS, APC, PM string reply
			j := i + 2
			for j < len(in) && in[j] != 0x07 && !(in[j] == 0x1b && j+1 < len(in) && in[j+1] == '\\') {
				j++
			}
			if j < len(in) && in[j] == 0x1b {
				j++
			}
			i = j
		case '[':
			j := i + 2
			for j < len(in) && (in[j] < 0x40 || in[j] > 0x7e) {
				j++
			}
			if j >= len(in) {
				return true
			}
			params, final := in[i+2:j], in[j]
			switch {
			case strings.ContainsRune("RcntyIO", rune(final)):
			case (final == 'M' || final == 'm') && len(params) > 0 && params[0] == '<':
				button, _, _ := strings.Cut(string(params[1:]), ";")
				if b, err := strconv.Atoi(button); err != nil || b&32 == 0 {
					return true
				}
			case final == 'M' && len(params) == 0 && j+3 < len(in):
				if (in[j+1]-32)&32 == 0 {
					return true
				}
				j += 3
			default:
				return true
			}
			i = j
		default:
			return true
		}
	}
	return false
}

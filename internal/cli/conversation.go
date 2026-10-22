package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/internal/tui"
	"github.com/barad-io/barad/wire"
)

// What you have read is kept on this computer, shared with the app: the
// newest finished turn of each session you have seen.

// reviewed is which turns you have read. The first time anything asks,
// everything so far counts as read, as in the app and the web app.
func (c *call) reviewed(all []wire.Session) map[string]string {
	p := prefs.Load()
	if p.Reviewed == nil {
		p, _ = prefs.Update(func(p *prefs.Prefs) {
			if p.Reviewed == nil {
				p.Read(all)
			}
		})
	}
	return p.Reviewed
}

var sessionsRead = command{
	usage: "sessions read [SESSION…] [--all] [--project P]",
	about: "mark replies read: the sessions named, or with --all every unread one (in a project with --project)",
	flags: func(fs *flag.FlagSet, c *call) {
		boolFlag(fs, c, "all", "every session with a reply you have not read")
		strFlag(fs, c, "project", "with --all, only this project's")
	},
	run: func(c *call) error {
		all, err := c.api.Sessions(c.ctx)
		if err != nil {
			return err
		}
		read := c.reviewed(all)
		var marked []wire.Session
		switch {
		case c.flag("all"):
			only := ""
			if q := c.str("project"); q != "" {
				p, err := c.project(q)
				if err != nil {
					return err
				}
				only = p.ProjectID
			}
			for _, s := range all {
				if s.ArchivedAt.IsZero() && api.Unread(s, read[s.SessionID]) && (only == "" || projectOf(s) == only) {
					marked = append(marked, s)
				}
			}
		case len(c.args) == 0:
			return errors.New("name the sessions to mark read, or --all")
		default:
			for _, q := range c.args {
				s, err := c.session(q)
				if err != nil {
					return err
				}
				marked = append(marked, s)
			}
		}
		if _, err := prefs.Update(func(p *prefs.Prefs) { p.Read(marked) }); err != nil {
			return err
		}
		return c.say(marked, "Marked %s read.", count(len(marked), "session"))
	},
}

var sessionsUnread = command{
	usage: "sessions unread SESSION",
	about: "mark a session's last reply unread, so it waits for you again",
	run: func(c *call) error {
		s, err := c.sessionArg(false)
		if err != nil {
			return err
		}
		if s.LatestTerminalTurnID == "" {
			return fmt.Errorf("%s has not replied yet", sessionName(s))
		}
		if _, err := prefs.Update(func(p *prefs.Prefs) { p.Unread(s.SessionID) }); err != nil {
			return err
		}
		return c.say(s, "Marked %s unread.", sessionName(s))
	},
}

var sessionsLog = command{
	usage: "sessions log SESSION [--all] [--last]",
	about: "the conversation as text: your messages and the agent's replies (the newest page; --all for everything, --last for the last reply alone)",
	flags: func(fs *flag.FlagSet, c *call) {
		boolFlag(fs, c, "all", "the whole conversation, loading every earlier page")
		boolFlag(fs, c, "last", "only the agent's last reply, as it wrote it")
	},
	run: func(c *call) error {
		s, err := c.sessionArg(false)
		if err != nil {
			return err
		}
		msgs, err := c.conversation(s.SessionID, c.flag("all"))
		if err != nil {
			return err
		}
		if c.flag("last") {
			msgs = lastReply(msgs)
			if len(msgs) == 0 {
				return fmt.Errorf("%s has not replied yet", sessionName(s))
			}
			if !c.asJSON {
				_, err := fmt.Fprintln(c.out, msgs[0].Text)
				return err
			}
		}
		if _, err := prefs.Update(func(p *prefs.Prefs) { p.Read([]wire.Session{s}) }); err != nil {
			return err
		}
		if c.asJSON {
			return c.json(msgs)
		}
		c.print(msgs)
		return nil
	},
}

// conversation reads a session's conversation: its newest page, or all of it.
func (c *call) conversation(id string, all bool) ([]tui.Message, error) {
	sync, err := c.api.Sync(c.ctx, id, true, 0, 0)
	if err != nil {
		return nil, err
	}
	for all && sync.TranscriptHasOlder {
		older, err := c.api.TranscriptBefore(c.ctx, id, sync.TranscriptBeforeCursor)
		if err != nil {
			return nil, err
		}
		sync.Items = append(older.Items, sync.Items...)
		sync.Turns = append(older.Turns, sync.Turns...)
		sync.Proposals = append(older.Proposals, sync.Proposals...)
		sync.TranscriptBeforeCursor, sync.TranscriptHasOlder = older.BeforeCursor, older.HasOlder
	}
	return tui.Plain(sync), nil
}

// lastReply is what the agent said after your last message: its replies,
// and how the turn ended if not well.
func lastReply(msgs []tui.Message) []tui.Message {
	i := len(msgs)
	for i > 0 && msgs[i-1].From != "you" {
		i--
	}
	var out []tui.Message
	for _, m := range msgs[i:] {
		if m.From != "compacted" {
			out = append(out, m)
		}
	}
	if len(out) > 1 && out[len(out)-1].From == "agent" {
		return out[len(out)-1:] // the reply that ended the turn
	}
	return out
}

// print writes messages as a conversation: who and when on a line, then
// the text, indented.
func (c *call) print(msgs []tui.Message) {
	for i, m := range msgs {
		if i > 0 {
			fmt.Fprintln(c.out)
		}
		head := map[string]string{"you": "You", "agent": "Agent", "failed": "Failed", "stopped": "Stopped", "proposal": "Proposal", "compacted": "Context compacted"}[m.From]
		if !m.At.IsZero() {
			head += "  ·  " + m.At.Local().Format("2 Jan 15:04")
		}
		if m.Steps > 0 {
			head += fmt.Sprintf("  ·  %s", count(m.Steps, "step"))
		}
		fmt.Fprintln(c.out, head)
		for _, line := range strings.Split(strings.TrimRight(m.Text, "\n"), "\n") {
			fmt.Fprintln(c.out, "  "+line)
		}
	}
}

var send = command{
	usage: "send SESSION [MESSAGE…] [--file PATH]… [--wait]",
	about: "send a session a message, with files (piped input goes too); --wait prints the reply when the turn ends",
	flags: func(fs *flag.FlagSet, c *call) {
		listFlag(fs, c, "file", "an image or text file to attach (repeat for more)")
		boolFlag(fs, c, "wait", "wait for the agent's reply and print it")
	},
	run: func(c *call) error {
		s, err := c.sessionArg(true)
		if err != nil {
			return err
		}
		text := strings.TrimSpace(strings.Join(c.args[1:], " "))
		files, err := c.attachments(c.list("file"))
		if err != nil {
			return err
		}
		if c.piped() { // piped in: the message, or a file with it
			data, err := io.ReadAll(c.in)
			if err != nil {
				return err
			}
			switch piped := strings.TrimSpace(string(data)); {
			case piped == "":
			case text == "" && len(piped) <= wire.MessageTextMaximumBytes:
				text = piped
			default:
				files = append(files, attachment{"stdin.txt", wire.PromptTextType, data})
			}
		}
		if text == "" && len(files) == 0 {
			return errors.New("missing the message")
		}
		before := s.LatestTerminalTurnID
		id := requestID()
		var media []string
		for _, f := range files {
			mid, err := c.api.UploadMedia(c.ctx, s.SessionID, id, "media_"+strings.TrimPrefix(requestID(), "msg_"), f.name, f.mediaType, f.data)
			if err != nil {
				return err
			}
			media = append(media, mid)
		}
		out, err := c.api.SendMessage(c.ctx, s.SessionID, id, text, media)
		if err != nil {
			return err
		}
		if !c.flag("wait") {
			return c.say(out, "Sent to %s.", sessionName(s))
		}
		return c.waitForReply(s, before)
	},
}

// waitForReply waits for a session to finish a turn after before, then
// prints its reply. A turn that failed is an error.
func (c *call) waitForReply(s wire.Session, before string) error {
	if _, ok := c.terminal(); ok && !c.asJSON {
		fmt.Fprintf(c.out, "Waiting for %s to reply… (Ctrl+C stops waiting; the agent carries on)\n\n", sessionName(s))
	}
	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(pollEvery):
		}
		all, err := c.api.Sessions(c.ctx)
		if err != nil {
			return err
		}
		for _, now := range all {
			if now.SessionID != s.SessionID || now.LatestTerminalTurnID == before || now.CurrentTurnID != "" || now.PendingTurn {
				continue
			}
			msgs, err := c.conversation(s.SessionID, false)
			if err != nil {
				return err
			}
			if _, err := prefs.Update(func(p *prefs.Prefs) { p.Read([]wire.Session{now}) }); err != nil {
				return err
			}
			reply := lastReply(msgs)
			if c.asJSON {
				err = c.json(reply)
			} else {
				c.print(reply)
			}
			if err == nil && now.LatestTerminalTurnStatus == wire.TurnStatusFailed {
				err = fmt.Errorf("%s's turn failed", sessionName(s))
			}
			return err
		}
	}
}

type attachment struct {
	name, mediaType string
	data            []byte
}

// attachments reads files to send: images as themselves, other text as
// text, within the API's limits.
func (c *call) attachments(paths []string) ([]attachment, error) {
	var out []attachment
	total := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(p)
		mediaType := http.DetectContentType(data)
		switch {
		case mediaType == "image/png" || mediaType == "image/jpeg" || mediaType == "image/webp" || mediaType == "image/gif":
			if len(data) > wire.PromptMediaMaximumBytes {
				return nil, fmt.Errorf("%s is larger than 20 MB", name)
			}
		case utf8.Valid(data):
			mediaType = wire.PromptTextType
			if len(data) > wire.PromptTextMaximumBytes {
				return nil, fmt.Errorf("%s is larger than 1 MB of text", name)
			}
		default:
			return nil, fmt.Errorf("%s is neither an image (PNG, JPEG, WebP, GIF) nor text; put it in the workspace with `barad files put`", name)
		}
		total += len(data)
		out = append(out, attachment{name, mediaType, data})
	}
	switch {
	case len(out) > wire.PromptMediaMaximumItems:
		return nil, fmt.Errorf("a message takes at most %d files", wire.PromptMediaMaximumItems)
	case total > wire.PromptMediaMaximumTotalBytes:
		return nil, errors.New("a message's files take at most 50 MB together")
	}
	return out, nil
}

func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

var sessionsProposal = command{
	usage: "sessions proposal SESSION [--approve | --dismiss]",
	about: "the session a session's agent proposes you start; --approve starts it, --dismiss declines it",
	flags: func(fs *flag.FlagSet, c *call) {
		boolFlag(fs, c, "approve", "start the proposed session")
		boolFlag(fs, c, "dismiss", "decline it")
	},
	run: func(c *call) error {
		s, err := c.sessionArg(false)
		if err != nil {
			return err
		}
		sync, err := c.api.Sync(c.ctx, s.SessionID, true, 0, 0)
		if err != nil {
			return err
		}
		var open *wire.SessionProposalView
		for i, p := range sync.Proposals {
			if p.Decision == nil && (open == nil || p.Sequence > open.Sequence) {
				open = &sync.Proposals[i]
			}
		}
		if open == nil {
			return fmt.Errorf("%s proposes nothing now", sessionName(s))
		}
		switch {
		case c.flag("approve") && c.flag("dismiss"):
			return errors.New("--approve or --dismiss, not both")
		case c.flag("approve"):
			d, err := c.api.ApproveProposal(c.ctx, s, *open)
			if err != nil {
				return err
			}
			return c.say(d, "Started %q (%s).", open.Proposal.Title, d.DestinationID)
		case c.flag("dismiss"):
			if err := c.api.DismissProposal(c.ctx, s.SessionID, open.Sequence); err != nil {
				return err
			}
			return c.say(open, "Dismissed %q.", open.Proposal.Title)
		}
		if c.asJSON {
			return c.json(open)
		}
		p := open.Proposal
		fmt.Fprintf(c.out, "%s\n\n  why        %s\n", p.Title, p.Reason)
		var repos, env []string
		for _, r := range p.Repositories {
			name := r.Repository
			if r.RequestedRef != nil && *r.RequestedRef != "" {
				name += "@" + *r.RequestedRef
			}
			repos = append(repos, name)
		}
		for _, v := range p.Environment {
			env = append(env, v.Name)
		}
		fmt.Fprintf(c.out, "  repos      %s\n  variables  %s\n\n", firstNonEmpty(strings.Join(repos, ", "), "none"), firstNonEmpty(strings.Join(env, ", "), "none"))
		for _, line := range strings.Split(p.Prompt, "\n") {
			fmt.Fprintln(c.out, "  "+line)
		}
		fmt.Fprintf(c.out, "\nbarad sessions proposal %s --approve starts it; --dismiss declines it.\n", s.SessionID)
		return nil
	},
}

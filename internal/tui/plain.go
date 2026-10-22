package tui

import (
	"fmt"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Message is one entry of a conversation as plain text, read the way the
// app reads it: what `barad sessions log` prints, and `barad send --wait`.
type Message struct {
	From  string    `json:"from"` // you, agent, failed, stopped, proposal, compacted
	Text  string    `json:"text"`
	At    time.Time `json:"at,omitzero"`
	Steps int       `json:"steps,omitempty"` // the agent's tool calls and thoughts before it
}

// Plain is a session's conversation, from a sync, as plain messages. Work
// in progress is left out: an agent's reply counts once it is whole.
func Plain(s *api.Sync) []Message {
	c := newConversation(s.Session)
	c.applySync(s)
	var out []Message
	for _, ex := range c.exchanges() {
		if u := ex.user; u != nil && u.status == "" {
			text := u.text
			if n := len(u.media); n > 0 {
				text += fmt.Sprintf("\n[%d attached]", n)
			}
			out = append(out, Message{From: "you", Text: text, At: u.at})
		}
		steps := 0
		for _, p := range ex.parts {
			switch p.kind {
			case partRun:
				steps += len(p.steps)
			case partText:
				if !p.live {
					out = append(out, Message{From: "agent", Text: p.text, Steps: steps})
					steps = 0
				}
			case partProposal:
				title, _ := p.steps[0].args["title"].(string)
				out = append(out, Message{From: "proposal", Text: "Proposed a session: " + title})
			case partCompaction:
				out = append(out, Message{From: "compacted", Text: p.text})
			case partFailure:
				msg := "The turn failed."
				if f := p.turn.Failure; f != nil && f.Message != "" {
					msg = f.Message
				}
				out = append(out, Message{From: "failed", Text: msg, At: p.turn.CompletedAt})
			case partEnd:
				if p.turn.Status == wire.TurnStatusCancelled {
					out = append(out, Message{From: "stopped", Text: "Stopped.", At: p.turn.CompletedAt})
				}
				// The reply before it ended then: give it the turn's end time.
				for i := len(out) - 1; i >= 0 && out[i].From != "you"; i-- {
					if out[i].From == "agent" && out[i].At.IsZero() {
						out[i].At = p.turn.CompletedAt
						break
					}
				}
			}
		}
	}
	return out
}

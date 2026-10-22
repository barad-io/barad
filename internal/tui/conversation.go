package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// A conversation is the open session as the API sends it: items in sequence,
// the turns they belong to, proposals, the messages the server is holding,
// and the text the agent is streaming. Everything shown is derived from it.
type conversation struct {
	id        string
	session   wire.Session
	items     []wire.TranscriptItem // by sequence
	turns     map[string]wire.Turn
	proposals map[int64]wire.SessionProposalView // by tool result sequence
	pending   []api.PendingMessage               // held by the server, not yet in the transcript
	sending   []api.PendingMessage               // sent from here, not yet admitted

	// Sync cursors.
	initialized       bool
	afterTranscript   int64
	afterEvent        int64
	before            int64
	hasOlder          bool
	loadingOlder      bool
	syncing, resync   bool
	liveTurn, liveSID string // the provider stream being shown
	liveText          string
}

func newConversation(s wire.Session) *conversation {
	return &conversation{id: s.SessionID, session: s, turns: map[string]wire.Turn{}, proposals: map[int64]wire.SessionProposalView{}}
}

// merge takes items in, keeping sequence order and dropping duplicates.
func (c *conversation) merge(items []wire.TranscriptItem) {
	seen := make(map[int64]bool, len(c.items))
	for _, it := range c.items {
		seen[it.Sequence] = true
	}
	for _, it := range items {
		if !seen[it.Sequence] {
			c.items = append(c.items, it)
			seen[it.Sequence] = true
		}
	}
	sort.Slice(c.items, func(a, b int) bool { return c.items[a].Sequence < c.items[b].Sequence })
}

// applySync takes one sync page. It reports whether another page follows.
func (c *conversation) applySync(d *api.Sync) bool {
	if c.liveTurn != "" && c.liveTurn != d.Session.CurrentTurnID {
		c.liveTurn, c.liveSID, c.liveText = "", "", "" // its turn ended
	}
	c.session = d.Session
	for _, t := range d.Turns {
		c.turns[t.TurnID] = t
	}
	for _, p := range d.Proposals {
		prev, ok := c.proposals[p.Sequence]
		if p.Decision == nil && ok {
			p.Decision = prev.Decision
		}
		c.proposals[p.Sequence] = p
	}
	c.applyEvents(d.Events)
	c.merge(d.Items)
	if !c.initialized {
		c.before, c.hasOlder = d.TranscriptBeforeCursor, d.TranscriptHasOlder
		if c.before == 0 && len(c.items) > 0 {
			c.before = c.items[0].Sequence
		}
	}
	c.initialized = true
	c.afterTranscript, c.afterEvent = d.TranscriptNextCursor, d.EventNextCursor
	c.pending = d.PendingMessages
	// What was sent from here and is now held or transcribed is no longer "sending".
	var still []api.PendingMessage
	for _, s := range c.sending {
		if !c.hasRequest(s.RequestID) {
			still = append(still, s)
		}
	}
	c.sending = still
	// The streamed text ends when its response is committed or its turn ends.
	committed := false
	for _, it := range c.items {
		if it.TurnID == c.liveTurn && it.StreamID == c.liveSID && c.liveSID != "" {
			committed = true
		}
	}
	if c.session.CurrentTurnID == "" || committed || c.liveTurn != c.session.CurrentTurnID {
		c.liveTurn, c.liveSID, c.liveText = "", "", ""
	}
	return d.TranscriptHasMore || d.EventHasMore
}

// hasRequest reports whether a message with this request id is held by the
// server or already in the transcript.
func (c *conversation) hasRequest(id string) bool {
	for _, p := range c.pending {
		if p.RequestID == id {
			return true
		}
	}
	for _, it := range c.items {
		if it.Kind == wire.ItemKindUser {
			var p wire.UserPromptPayload
			if json.Unmarshal(it.Payload, &p) == nil && p.RequestID == id {
				return true
			}
		}
	}
	return false
}

// applyEvents keeps the text the agent is streaming, as the web app does:
// deltas of the newest stream of the current turn, until it is committed.
func (c *conversation) applyEvents(events []wire.Event) {
	sort.Slice(events, func(a, b int) bool { return events[a].Sequence < events[b].Sequence })
	for _, e := range events {
		if e.Sequence <= c.afterEvent {
			continue
		}
		switch e.Kind {
		case wire.EventKindModelDelta:
			var p struct {
				Delta *string `json:"delta"`
			}
			if json.Unmarshal(e.Payload, &p) != nil || p.Delta == nil {
				continue
			}
			if c.liveTurn != e.TurnID || c.liveSID != e.StreamID {
				c.liveTurn, c.liveSID, c.liveText = e.TurnID, e.StreamID, ""
			}
			c.liveText += *p.Delta
		case wire.EventKindStatus:
			var p struct {
				Type     string                        `json:"type"`
				Decision *wire.SessionProposalDecision `json:"decision"`
			}
			if json.Unmarshal(e.Payload, &p) != nil {
				continue
			}
			if p.Type == "session_proposal_decided" && p.Decision != nil {
				if v, ok := c.proposals[p.Decision.Sequence]; ok {
					v.Decision = p.Decision
					c.proposals[p.Decision.Sequence] = v
				}
				continue
			}
			// Streams are serialized: a newer stream means the shown one was committed.
			if c.liveTurn == e.TurnID && c.liveSID != "" && c.liveSID != e.StreamID {
				c.liveTurn, c.liveSID, c.liveText = "", "", ""
				continue
			}
			switch p.Type {
			case "response.failed", "response.incomplete", "response.error", "error", "provider_error", "provider_retry":
				if c.liveTurn == e.TurnID && c.liveSID == e.StreamID {
					c.liveTurn, c.liveSID, c.liveText = "", "", ""
				}
			}
		}
	}
}

// ---------------------------------------------------------------- exchanges

// An exchange is one message of yours and everything the agent did about
// it: runs of work, text, a proposal, a failure.
type exchange struct {
	user  *userMessage // nil when the page starts mid-exchange
	parts []part
}

type userMessage struct {
	text   string
	media  []wire.PromptMediaRef
	at     time.Time
	status string // "" in the transcript; "queued", "steering" or "sending" while held
}

type partKind int

const (
	partRun partKind = iota
	partText
	partCompaction
	partProposal
	partFailure
	partCancelled
)

type part struct {
	kind    partKind
	turnID  string
	steps   []*step              // a run
	text    string               // text, or a compaction's note
	turn    *wire.Turn           // a failure
	call    *wire.TranscriptItem // a proposal's call
	result  *wire.TranscriptItem // a proposal's result
	live    bool                 // text the agent is still writing
	working bool                 // the run is the current turn's last
}

// step is one reasoning summary or tool call, with its result once it has one.
type step struct {
	kind   string // reasoning, bash, read, write, edit, web_search, screenshot, publish, fs_*, …
	call   *wire.TranscriptItem
	result *wire.TranscriptItem
	args   map[string]any
	think  string
}

// exchanges derives the conversation's exchanges from its items.
func (c *conversation) exchanges() []exchange {
	var out []exchange
	current := func() *exchange {
		if len(out) == 0 {
			out = append(out, exchange{})
		}
		return &out[len(out)-1]
	}
	calls := map[string]*step{}
	runFor := func(turnID string) *part {
		ex := current()
		if n := len(ex.parts); n > 0 && ex.parts[n-1].kind == partRun && ex.parts[n-1].turnID == turnID {
			return &ex.parts[n-1]
		}
		ex.parts = append(ex.parts, part{kind: partRun, turnID: turnID})
		return &ex.parts[len(ex.parts)-1]
	}
	lastOfTurn := map[string]int{} // exchange index holding each turn's last item
	for i := range c.items {
		it := &c.items[i]
		switch it.Kind {
		case wire.ItemKindUser:
			var p wire.UserPromptPayload
			_ = json.Unmarshal(it.Payload, &p)
			if p.Text == "" {
				_ = json.Unmarshal(it.Payload, &p.Text) // a bare string
			}
			out = append(out, exchange{user: &userMessage{text: p.Text, media: p.Media, at: it.CreatedAt}})
		case wire.ItemKindReasoning:
			if text := reasoningSummary(it.Payload); text != "" {
				r := runFor(it.TurnID)
				r.steps = append(r.steps, &step{kind: "reasoning", call: it, think: text})
			}
		case wire.ItemKindToolCall:
			var p struct {
				Kind         string          `json:"kind"`
				Arguments    json.RawMessage `json:"arguments"`
				RawArguments string          `json:"raw_arguments"` // arguments that were not JSON
			}
			_ = json.Unmarshal(it.Payload, &p)
			var args map[string]any
			_ = json.Unmarshal(p.Arguments, &args)
			if args == nil && p.RawArguments != "" {
				args = map[string]any{"arguments": p.RawArguments}
			}
			s := &step{kind: strings.ToLower(p.Kind), call: it, args: args}
			if s.kind == "" {
				s.kind = "tool"
			}
			if it.ToolCallID != "" {
				calls[it.ToolCallID] = s
			}
			if s.kind == string(wire.CommandKindNewSession) {
				ex := current()
				ex.parts = append(ex.parts, part{kind: partProposal, turnID: it.TurnID, call: it, steps: []*step{s}})
			} else {
				r := runFor(it.TurnID)
				r.steps = append(r.steps, s)
			}
		case wire.ItemKindToolResult:
			if s, ok := calls[it.ToolCallID]; ok && it.ToolCallID != "" {
				s.result = it
				break
			}
			r := runFor(it.TurnID)
			r.steps = append(r.steps, &step{kind: "tool", result: it})
		case wire.ItemKindAssistant:
			if text := payloadText(it.Payload); text != "" {
				ex := current()
				ex.parts = append(ex.parts, part{kind: partText, turnID: it.TurnID, text: text})
			}
		case wire.ItemKindProviderOutput:
			if text := providerMessageText(it.Payload); text != "" {
				ex := current()
				ex.parts = append(ex.parts, part{kind: partText, turnID: it.TurnID, text: text})
			}
		case wire.ItemKindContextCompaction:
			var p struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(it.Payload, &p)
			note := "Context compacted"
			if p.Status == "failed" {
				note = "Context compaction failed"
			}
			ex := current()
			ex.parts = append(ex.parts, part{kind: partCompaction, text: note})
		}
		if it.TurnID != "" && len(out) > 0 {
			lastOfTurn[it.TurnID] = len(out) - 1
		}
	}
	// A proposal block takes its call's result.
	for i := range out {
		for j := range out[i].parts {
			if b := &out[i].parts[j]; b.kind == partProposal {
				b.result = b.steps[0].result
			}
		}
	}

	// How each turn ended: a failure after its work, a cancellation noted.
	for id, t := range c.turns {
		idx, ok := lastOfTurn[id]
		if !ok {
			if id != c.session.LatestTerminalTurnID || len(out) == 0 {
				continue
			}
			idx = len(out) - 1
		}
		t := t
		switch t.Status {
		case wire.TurnStatusFailed:
			out[idx].parts = append(out[idx].parts, part{kind: partFailure, turnID: id, turn: &t})
		case wire.TurnStatusCancelled:
			out[idx].parts = append(out[idx].parts, part{kind: partCancelled, turnID: id, turn: &t})
		}
	}

	// The current turn: its last run is working; its streamed text follows.
	if cur := c.session.CurrentTurnID; cur != "" && len(out) > 0 {
		ex := &out[len(out)-1]
		n := len(ex.parts)
		if n > 0 && ex.parts[n-1].kind == partRun && ex.parts[n-1].turnID == cur {
			ex.parts[n-1].working = true
		} else if c.liveText == "" {
			ex.parts = append(ex.parts, part{kind: partRun, turnID: cur, working: true})
		}
		if c.liveText != "" && c.liveTurn == cur {
			ex.parts = append(ex.parts, part{kind: partText, turnID: cur, text: c.liveText, live: true})
		}
	}

	// Messages the server holds, then those still on their way.
	for _, p := range c.pending {
		status := "queued"
		if p.Kind == "steering" {
			status = "steering"
		}
		out = append(out, exchange{user: &userMessage{text: p.Text, media: p.Media, at: p.CreatedAt, status: status}})
	}
	for _, p := range c.sending {
		out = append(out, exchange{user: &userMessage{text: p.Text, at: p.CreatedAt, status: "sending"}})
	}
	return out
}

func payloadText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Text
}

// providerMessageText is the text of a provider's output item, with its
// sources listed after it (as the web app shows it).
func providerMessageText(raw json.RawMessage) string {
	var p struct {
		Type          string `json:"type"`
		ClaudeContent *struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Citations []struct {
				URL, Title string
			} `json:"citations"`
		} `json:"claude_content"`
		Content []struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Refusal     string `json:"refusal"`
			Annotations []struct {
				URL, Title string
			} `json:"annotations"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	type source struct{ title, url string }
	var text string
	var sources []source
	switch {
	case p.Type == "claude_content" && p.ClaudeContent != nil:
		if p.ClaudeContent.Type != "text" {
			return ""
		}
		text = p.ClaudeContent.Text
		for _, c := range p.ClaudeContent.Citations {
			sources = append(sources, source{c.Title, c.URL})
		}
	case p.Type == "message":
		for _, part := range p.Content {
			switch part.Type {
			case "output_text":
				text += part.Text
			case "refusal":
				text += part.Refusal
			}
			for _, a := range part.Annotations {
				sources = append(sources, source{a.Title, a.URL})
			}
		}
	default:
		return ""
	}
	seen := map[string]bool{}
	var lines []string
	for _, s := range sources {
		if s.url == "" || seen[s.url] {
			continue
		}
		seen[s.url] = true
		title := strings.TrimSpace(s.title)
		if title == "" {
			title = "Source"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", title, s.url))
	}
	if len(lines) > 0 {
		text += "\n\nSources:\n" + strings.Join(lines, "\n")
	}
	return text
}

// reasoningSummary is a reasoning item's readable summary.
func reasoningSummary(raw json.RawMessage) string {
	var p struct {
		Type          string `json:"type"`
		ClaudeContent *struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"claude_content"`
		Summary json.RawMessage `json:"summary"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	if p.Type == "claude_content" {
		if p.ClaudeContent != nil && p.ClaudeContent.Type == "thinking" {
			return p.ClaudeContent.Thinking
		}
		return ""
	}
	var s string
	if json.Unmarshal(p.Summary, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(p.Summary, &parts) != nil {
		return ""
	}
	var texts []string
	for _, part := range parts {
		var t string
		if json.Unmarshal(part, &t) == nil && t != "" {
			texts = append(texts, t)
			continue
		}
		var o struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(part, &o) == nil && o.Text != "" {
			texts = append(texts, o.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// ---------------------------------------------------------------- results

// result is a tool result, read the way the web app reads it.
type result struct {
	direct    string         // a result that is only a string
	value     map[string]any // payload.result as an object
	status    string         // payload.status
	errCode   string
	errMsg    string
	cancelled bool
}

func readResult(it *wire.TranscriptItem) result {
	if it == nil {
		return result{}
	}
	var r result
	if json.Unmarshal(it.Payload, &r.direct) == nil {
		return r
	}
	var p struct {
		Result json.RawMessage `json:"result"`
		Status string          `json:"status"`
		Error  *struct {
			Code, Message string
		} `json:"error"`
	}
	_ = json.Unmarshal(it.Payload, &p)
	r.status = p.Status
	if p.Error != nil {
		r.errCode, r.errMsg = p.Error.Code, p.Error.Message
	}
	if json.Unmarshal(p.Result, &r.value) != nil {
		var s string
		if json.Unmarshal(p.Result, &s) == nil {
			r.direct = s
		}
	}
	r.cancelled = r.errCode == "cancelled" || r.status == "cancelled"
	return r
}

// failure is why a tool failed, or "" when it did not.
func (r result) failure(kind string) string {
	if r.errMsg != "" {
		return r.errMsg
	}
	if kind == "web_search" && strings.HasPrefix(r.direct, "Web search failed:") {
		return r.direct
	}
	exit, hasExit := r.value["exit_code"].(float64)
	failed := r.status == "failed" || r.value["outcome"] == "failure" || (hasExit && exit != 0)
	if !failed {
		return ""
	}
	if d, _ := r.value["detail"].(string); d != "" {
		return d
	}
	if hasExit && exit != 0 {
		return fmt.Sprintf("Exited with status %d.", int(exit))
	}
	return "Tool execution failed."
}

// output is a result's text output.
func (r result) output() string {
	if r.direct != "" {
		return r.direct
	}
	s, _ := r.value["output"].(string)
	return s
}

// applyOlder takes a page from before the oldest item shown.
func (c *conversation) applyOlder(d *api.Older) {
	for _, t := range d.Turns {
		if _, ok := c.turns[t.TurnID]; !ok {
			c.turns[t.TurnID] = t
		}
	}
	for _, p := range d.Proposals {
		if _, ok := c.proposals[p.Sequence]; !ok {
			c.proposals[p.Sequence] = p
		}
	}
	c.merge(d.Items)
	c.before, c.hasOlder = d.BeforeCursor, d.HasOlder
	if c.before == 0 && len(c.items) > 0 {
		c.before = c.items[0].Sequence
	}
}

// openProposal is the newest proposal still waiting for you.
func (c *conversation) openProposal() (wire.SessionProposalView, bool) {
	var best wire.SessionProposalView
	found := false
	for _, p := range c.proposals {
		if p.Decision == nil && (!found || p.Sequence > best.Sequence) {
			best, found = p, true
		}
	}
	return best, found
}

// A picRef is an image in the conversation: one you attached, or a
// screenshot the agent took (keyed by its result's sequence).
type picRef struct {
	key, name string
	mediaID   string
	shot      int64
}

func mediaRef(m wire.PromptMediaRef) picRef {
	return picRef{key: "media/" + m.MediaID, name: m.Filename, mediaID: m.MediaID}
}

func shotRef(result *wire.TranscriptItem) picRef {
	return picRef{key: fmt.Sprintf("shot/%d", result.Sequence), name: "screenshot " + result.CreatedAt.Local().Format("Jan 2 15:04:05"), shot: result.Sequence}
}

// screenshot reports whether a step's result carries a screenshot.
func (s *step) screenshot() bool {
	if s.kind != "screenshot" || s.result == nil {
		return false
	}
	r := readResult(s.result)
	_, ok := r.value["screenshot"].(map[string]any)
	return ok && r.failure(s.kind) == "" && !r.cancelled
}

// pictures lists every image in the conversation, in order.
func (c *conversation) pictures() []picRef {
	var out []picRef
	for _, ex := range c.exchanges() {
		if ex.user != nil {
			for _, m := range ex.user.media {
				if isImage(m.MediaType) {
					out = append(out, mediaRef(m))
				}
			}
		}
		for _, p := range ex.parts {
			for _, s := range p.steps {
				if s.screenshot() {
					out = append(out, shotRef(s.result))
				}
			}
		}
	}
	return out
}

// activity is how much the conversation did in each of n equal spans up to
// now, over its loaded items; nil when there is too little to draw.
func (c *conversation) activity(now time.Time, n int) []float64 {
	if len(c.items) < 4 {
		return nil
	}
	first := c.items[0].CreatedAt
	span := now.Sub(first) / time.Duration(n)
	if span <= 0 {
		return nil
	}
	out := make([]float64, n)
	for _, it := range c.items {
		i := int(it.CreatedAt.Sub(first) / span)
		out[min(max(i, 0), n-1)]++
	}
	return out
}

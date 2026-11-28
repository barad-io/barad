package tui

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/prefs"
	"github.com/barad-io/barad/wire"
)

// conversation holds the currently attached session's state synced from the
// Barad API: transcript items (supporting Barad v2 in-place streaming updates),
// turns, proposals, server-queued inbox messages, and live status events.
type conversation struct {
	id        string
	session   wire.Session
	items     []wire.TranscriptItem
	read      map[int64]readItem
	turns     map[string]wire.Turn
	proposals map[int64]wire.SessionProposalView
	pending   []api.PendingMessage // uniform post-turn queue held by server
	sending   []api.PendingMessage // in-flight upload/send from this client

	initialized     bool
	afterTranscript int64
	afterEvent      int64
	before          int64
	hasOlder        bool
	loadingOlder    bool
	syncing, resync bool

	// Legacy v1 event-streamed text + v2 retry notice.
	liveTurn, liveSID string
	liveText          string
	retryNotice       string
	retryTurnID       string

	exs     []exchange
	derived bool
}

func (c *conversation) changed() { c.derived = false }

func newConversation(s wire.Session) *conversation {
	return &conversation{
		id:        s.SessionID,
		session:   s,
		read:      map[int64]readItem{},
		turns:     map[string]wire.Turn{},
		proposals: map[int64]wire.SessionProposalView{},
	}
}

// merge incorporates items by sequence, replacing existing items at the same
// sequence so Barad v2 in-place streaming text/reasoning updates render live.
func (c *conversation) merge(items []wire.TranscriptItem) {
	index := make(map[int64]int, len(c.items))
	for i, it := range c.items {
		index[it.Sequence] = i
	}
	for _, it := range items {
		if idx, exists := index[it.Sequence]; exists {
			c.items[idx] = it
			c.read[it.Sequence] = readPayload(it)
		} else {
			index[it.Sequence] = len(c.items)
			c.items = append(c.items, it)
			c.read[it.Sequence] = readPayload(it)
		}
	}
	sort.Slice(c.items, func(a, b int) bool { return c.items[a].Sequence < c.items[b].Sequence })
}

// applySync applies a /sync response. Returns true if another page follows.
func (c *conversation) applySync(d *api.Sync) bool {
	if c.liveTurn != "" && c.liveTurn != d.Session.CurrentTurnID {
		c.liveTurn, c.liveSID, c.liveText = "", "", ""
	}
	if c.retryTurnID != "" && c.retryTurnID != d.Session.CurrentTurnID {
		c.retryTurnID, c.retryNotice = "", ""
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
	var still []api.PendingMessage
	for _, s := range c.sending {
		if !c.hasRequest(s.RequestID) {
			still = append(still, s)
		}
	}
	c.sending = still
	committed := false
	for _, it := range c.items {
		if it.TurnID == c.liveTurn && it.StreamID == c.liveSID && c.liveSID != "" && !it.Streaming {
			committed = true
		}
	}
	if c.session.CurrentTurnID == "" || committed || c.liveTurn != c.session.CurrentTurnID {
		c.liveTurn, c.liveSID, c.liveText = "", "", ""
	}
	// Clear retry notice once new text/tools arrive after it or turn ends.
	if c.session.CurrentTurnID == "" {
		c.retryTurnID, c.retryNotice = "", ""
	}
	c.changed()
	return d.TranscriptHasMore || d.EventHasMore
}

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
				Type        string                        `json:"type"`
				Message     string                        `json:"message"`
				Code        string                        `json:"code"`
				HTTPStatus  int                           `json:"http_status"`
				Attempt     int                           `json:"attempt"`
				MaxAttempts int                           `json:"max_attempts"`
				RetryInMS   int64                         `json:"retry_in_ms"`
				Decision    *wire.SessionProposalDecision `json:"decision"`
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
			if p.Type == "provider_retry" {
				c.retryTurnID = e.TurnID
				c.retryNotice = formatRetry(p.Message, p.Code, p.HTTPStatus, p.Attempt, p.MaxAttempts, p.RetryInMS)
			}
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

func formatRetry(msg, code string, status, attempt, maxAttempts int, retryInMS int64) string {
	var parts []string
	if msg != "" {
		parts = append(parts, msg)
	} else if code != "" {
		parts = append(parts, strings.ReplaceAll(code, "_", " "))
	} else {
		parts = append(parts, "Transient provider error")
	}
	if status > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", status))
	}
	if attempt > 0 {
		if maxAttempts > 0 {
			parts = append(parts, fmt.Sprintf("retry %d/%d", attempt, maxAttempts))
		} else {
			parts = append(parts, fmt.Sprintf("retry #%d", attempt))
		}
	}
	if retryInMS > 0 {
		parts = append(parts, fmt.Sprintf("in %.1fs", float64(retryInMS)/1000))
	}
	return strings.Join(parts, " · ")
}

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
	c.changed()
	c.before, c.hasOlder = d.BeforeCursor, d.HasOlder
	if c.before == 0 && len(c.items) > 0 {
		c.before = c.items[0].Sequence
	}
}

// ---------------------------------------------------------------- exchanges

type exchange struct {
	user   *userMessage
	parts  []part
	pics   []picRef
	sum    uint64
	moving bool
}

type userMessage struct {
	text   string
	media  []wire.PromptMediaRef
	at     time.Time
	status string // "" in transcript; "queued" or "sending" while held
}

type partKind int

const (
	partRun partKind = iota
	partText
	partCompaction
	partProposal
	partFailure
	partEnd
)

type part struct {
	kind    partKind
	turnID  string
	steps   []*step
	text    string
	turn    *wire.Turn
	call    *wire.TranscriptItem
	result  *wire.TranscriptItem
	live    bool
	working bool
}

type step struct {
	kind      string
	call      *wire.TranscriptItem
	result    *wire.TranscriptItem
	args      map[string]any
	arrayArgs []any // for v2 tool calls whose arguments are a JSON array (e.g. file_change)
	think     string
	live      bool
}

func (c *conversation) exchanges() []exchange {
	if !c.derived {
		c.exs, c.derived = c.derive(), true
	}
	return c.exs
}

func (c *conversation) derive() []exchange {
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
	lastOfTurn := map[string]int{}
	for i := range c.items {
		it := &c.items[i]
		r := c.read[it.Sequence]
		switch it.Kind {
		case wire.ItemKindUser:
			out = append(out, exchange{user: &userMessage{text: r.text, media: r.media, at: it.CreatedAt}})
		case wire.ItemKindReasoning:
			if r.text != "" {
				run := runFor(it.TurnID)
				run.steps = append(run.steps, &step{kind: "reasoning", call: it, think: r.text, live: it.Streaming})
			}
		case wire.ItemKindToolCall:
			s := &step{kind: r.kind, call: it, args: r.args, arrayArgs: r.arrayArgs}
			if it.ToolCallID != "" {
				calls[it.ToolCallID] = s
			}
			if s.kind == string(wire.CommandKindNewSession) {
				ex := current()
				ex.parts = append(ex.parts, part{kind: partProposal, turnID: it.TurnID, call: it, steps: []*step{s}})
			} else {
				run := runFor(it.TurnID)
				run.steps = append(run.steps, s)
			}
		case wire.ItemKindToolResult:
			if s, ok := calls[it.ToolCallID]; ok && it.ToolCallID != "" {
				s.result = it
				break
			}
			run := runFor(it.TurnID)
			run.steps = append(run.steps, &step{kind: "tool", result: it})
		case wire.ItemKindAssistant, wire.ItemKindProviderOutput:
			if r.text != "" {
				ex := current()
				ex.parts = append(ex.parts, part{kind: partText, turnID: it.TurnID, text: r.text, live: it.Streaming})
			}
		case wire.ItemKindContextCompaction:
			ex := current()
			ex.parts = append(ex.parts, part{kind: partCompaction, text: r.text})
		}
		if it.TurnID != "" && len(out) > 0 {
			lastOfTurn[it.TurnID] = len(out) - 1
		}
	}
	for i := range out {
		for j := range out[i].parts {
			if b := &out[i].parts[j]; b.kind == partProposal && len(b.steps) > 0 {
				b.result = b.steps[0].result
			}
		}
	}

	ended := make([]string, 0, len(c.turns))
	for id := range c.turns {
		ended = append(ended, id)
	}
	sort.Slice(ended, func(i, j int) bool { return c.turns[ended[i]].CreatedAt.Before(c.turns[ended[j]].CreatedAt) })
	for _, id := range ended {
		t := c.turns[id]
		idx, ok := lastOfTurn[id]
		if !ok {
			if id != c.session.LatestTerminalTurnID || len(out) == 0 {
				continue
			}
			idx = len(out) - 1
		}
		switch t.Status {
		case wire.TurnStatusFailed:
			out[idx].parts = append(out[idx].parts, part{kind: partFailure, turnID: id, turn: &t}, part{kind: partEnd, turnID: id, turn: &t})
		case wire.TurnStatusCompleted, wire.TurnStatusCancelled:
			out[idx].parts = append(out[idx].parts, part{kind: partEnd, turnID: id, turn: &t})
		}
	}

	// Active turn state.
	if cur := c.session.CurrentTurnID; cur != "" && len(out) > 0 {
		ex := &out[len(out)-1]
		hasStreamingText := false
		for _, p := range ex.parts {
			if p.kind == partText && p.turnID == cur && p.live {
				hasStreamingText = true
			}
		}
		n := len(ex.parts)
		if n > 0 && ex.parts[n-1].kind == partRun && ex.parts[n-1].turnID == cur {
			ex.parts[n-1].working = true
		} else if c.liveText == "" && !hasStreamingText {
			ex.parts = append(ex.parts, part{kind: partRun, turnID: cur, working: true})
		}
		if c.liveText != "" && c.liveTurn == cur && !hasStreamingText {
			ex.parts = append(ex.parts, part{kind: partText, turnID: cur, text: c.liveText, live: true})
		}
		ex.moving = true
	}

	for i := range out {
		c.settle(&out[i])
	}
	return out
}

func (c *conversation) settle(ex *exchange) {
	h := fnv.New64a()
	put := func(vs ...any) { fmt.Fprint(h, vs...); h.Write([]byte{0}) }
	put(c.session.HarnessID(), c.session.Model)
	if u := ex.user; u != nil {
		put(u.at.UnixNano(), u.text, u.status, len(u.media))
		for _, m := range u.media {
			put(m.MediaID, m.Filename)
			if isImage(m.MediaType) && m.MediaID != "" {
				ex.pics = append(ex.pics, mediaRef(m))
			}
		}
		ex.moving = ex.moving || u.status != ""
	}
	for _, p := range ex.parts {
		put(p.kind, p.turnID, len(p.text), p.live, p.working)
		ex.moving = ex.moving || p.live || p.working
		if p.turn != nil {
			put(p.turn.Status, p.turn.CompletedAt.UnixNano())
		}
		for _, s := range p.steps {
			if s.call != nil {
				put(s.call.Sequence, s.live)
			}
			if s.result != nil {
				put(s.result.Sequence)
			}
			if s.live {
				ex.moving = true
			}
			if s.screenshot() {
				ex.pics = append(ex.pics, shotRef(s.result))
			}
		}
		if p.kind == partProposal {
			ex.moving = ex.moving || p.result == nil
			if p.result != nil {
				v, ok := c.proposals[p.result.Sequence]
				put(ok, v.Decision != nil)
				if v.Decision != nil {
					put(v.Decision.Status)
				}
			}
		}
	}
	// If the last exchange belongs to an active turn or has no terminal end yet while current turn is set, keep moving.
	if c.session.CurrentTurnID != "" {
		for _, p := range ex.parts {
			if p.turnID == c.session.CurrentTurnID {
				ex.moving = true
			}
		}
	}
	ex.sum = h.Sum64()
}

// allSteps returns every non-empty step in chronological order for /tools inspection.
func (c *conversation) allSteps() []*step {
	var out []*step
	for _, ex := range c.exchanges() {
		for _, p := range ex.parts {
			if p.kind == partRun {
				out = append(out, p.steps...)
			}
		}
	}
	return out
}

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

type picRef struct {
	key, name string
	mediaID   string
	shot      int64
}

func mediaRef(m wire.PromptMediaRef) picRef {
	return picRef{key: "media/" + m.MediaID, name: m.Filename, mediaID: m.MediaID}
}

func shotRef(res *wire.TranscriptItem) picRef {
	return picRef{key: fmt.Sprintf("shot/%d", res.Sequence), name: "screenshot " + res.CreatedAt.Local().Format("Jan 2 15:04:05"), shot: res.Sequence}
}

func (s *step) screenshot() bool {
	if s.kind != "screenshot" || s.result == nil {
		return false
	}
	r := readResult(s.result)
	_, ok := r.value["screenshot"].(map[string]any)
	return ok && r.failure(s.kind) == "" && !r.cancelled
}

func (c *conversation) pictures() []picRef {
	var out []picRef
	for _, ex := range c.exchanges() {
		out = append(out, ex.pics...)
	}
	return out
}

// ---------------------------------------------------------------- payload decoding

type readItem struct {
	text      string
	media     []wire.PromptMediaRef
	kind      string
	args      map[string]any
	arrayArgs []any
}

func readPayload(it wire.TranscriptItem) readItem {
	var r readItem
	switch it.Kind {
	case wire.ItemKindUser:
		var p wire.UserPromptPayload
		_ = json.Unmarshal(it.Payload, &p)
		if p.Text == "" {
			_ = json.Unmarshal(it.Payload, &p.Text)
		}
		r.text, r.media = p.Text, p.Media
	case wire.ItemKindReasoning:
		r.text = reasoningSummary(it.Payload)
	case wire.ItemKindToolCall:
		var p struct {
			Kind         string          `json:"kind"`
			Name         string          `json:"name"`
			Arguments    json.RawMessage `json:"arguments"`
			RawArguments string          `json:"raw_arguments"`
		}
		_ = json.Unmarshal(it.Payload, &p)
		if len(p.Arguments) > 0 {
			if json.Unmarshal(p.Arguments, &r.args) != nil {
				_ = json.Unmarshal(p.Arguments, &r.arrayArgs)
			}
		}
		if r.args == nil && r.arrayArgs == nil && p.RawArguments != "" {
			r.args = map[string]any{"arguments": p.RawArguments}
		}
		r.kind = strings.ToLower(strings.TrimSpace(p.Kind))
		if r.kind == "" {
			r.kind = strings.ToLower(strings.TrimSpace(p.Name))
		}
		if r.kind == "" {
			r.kind = "tool"
		}
	case wire.ItemKindAssistant:
		r.text = payloadText(it.Payload)
	case wire.ItemKindProviderOutput:
		r.text = providerMessageText(it.Payload)
	case wire.ItemKindContextCompaction:
		var p struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(it.Payload, &p)
		r.text = "Context compacted"
		if p.Status == "failed" {
			r.text = "Context compaction failed"
		}
	}
	return r
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

func reasoningSummary(raw json.RawMessage) string {
	var p struct {
		Type          string `json:"type"`
		Text          string `json:"text"`
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
	if json.Unmarshal(p.Summary, &s) == nil && s != "" {
		return s
	}
	if p.Text != "" {
		return p.Text
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

// ---------------------------------------------------------------- tool results

type result struct {
	direct    string
	value     map[string]any
	status    string
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
		return fmt.Sprintf("Exited with status %d", int(exit))
	}
	return "Tool execution failed"
}

func (r result) output() string {
	if r.direct != "" {
		return r.direct
	}
	s, _ := r.value["output"].(string)
	return s
}

// ---------------------------------------------------------------- conversation-first rendering

// renderExchange renders one exchange full-width with zero permanent chrome,
// ready to print cleanly into the terminal's primary buffer or render live.
func renderExchange(c *conversation, ex exchange, w int, work prefs.Work, frame int, now time.Time, cancelOn string) []string {
	w = max(w, 28)
	var lines []Line

	// User prompt: crisp left-aligned coding-agent prompt line (`❯ ...`).
	if u := ex.user; u != nil {
		promptText := strings.TrimSpace(u.text)
		if promptText != "" {
			wrapped := Wrap(linkify(promptText, SText.Bold(true)), max(12, w-2))
			for i, wl := range wrapped {
				prefix := S("  ", SText)
				if i == 0 {
					prefix = S("❯ ", SAccent.Bold(true))
				}
				lines = append(lines, append(Line{prefix}, wl...))
			}
		}
		if len(u.media) > 0 {
			var chips Line
			chips = append(chips, S("  ", SText))
			for i, m := range u.media {
				if i > 0 {
					chips = append(chips, S("  ", SText))
				}
				chips = append(chips, S(attachMark(m.MediaType)+m.Filename, SMuted))
			}
			lines = append(lines, chips)
		}
		if len(ex.parts) > 0 {
			lines = append(lines, Line{})
		}
	}

	lastRun := map[string]int{}
	for i, p := range ex.parts {
		if p.kind == partRun {
			lastRun[p.turnID] = i
		}
	}

	for i, p := range ex.parts {
		switch p.kind {
		case partRun:
			lines = append(lines, renderRun(c, p, lastRun[p.turnID] == i, w, work, frame, now)...)
		case partText:
			md := Markdown(p.text, w)
			if p.live && len(md) > 0 {
				md[len(md)-1] = append(md[len(md)-1], S("▍", SAccent))
			}
			if len(lines) > 0 && len(lines[len(lines)-1]) > 0 {
				lines = append(lines, Line{})
			}
			lines = append(lines, md...)
		case partCompaction:
			lines = append(lines, Line{}, Line{S("── ", Fg(BorderDefault)), S(strings.ToLower(p.text), SFaint), S(" ──", Fg(BorderDefault))})
		case partProposal:
			lines = append(lines, Line{})
			lines = append(lines, renderProposal(c, p, w, frame)...)
		case partFailure:
			msg := "The turn failed."
			if p.turn != nil && p.turn.Failure != nil {
				msg = p.turn.Failure.Message
				if p.turn.Failure.Code != "" {
					msg += " (" + p.turn.Failure.Code + ")"
				}
			}
			lines = append(lines, Line{}, Line{S("✕ ", SDanger.Bold(true)), S("Error", SDanger.Bold(true))})
			lines = append(lines, ruled(Wrap([]Seg{S(msg, SSec)}, max(12, w-2)), Danger)...)
		case partEnd:
			if p.turn != nil {
				lines = append(lines, Line{}, renderTurnEnd(*p.turn, w, now))
			}
		}
	}

	// Retry notice + active working spinner.
	if c.retryNotice != "" && c.retryTurnID == c.session.CurrentTurnID && ex.moving {
		lines = append(lines, Line{S("↻ ", SWarn.Bold(true)), S(c.retryNotice, SWarn)})
	}
	for _, p := range ex.parts {
		if p.working {
			if len(lines) > 0 && len(lines[len(lines)-1]) > 0 {
				lines = append(lines, Line{})
			}
			lines = append(lines, renderWorkingLine(c, p, frame, now, cancelOn))
		}
	}

	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Paint(nil, 0)
	}
	return out
}

func renderRun(c *conversation, p part, last bool, w int, work prefs.Work, frame int, now time.Time) []Line {
	t, hasTurn := c.turns[p.turnID]
	working := p.working
	var start, end time.Time
	calls := 0
	for _, s := range p.steps {
		if s.call != nil {
			if start.IsZero() {
				start = s.call.CreatedAt
			}
			end = s.call.CreatedAt
		}
		if s.result != nil {
			end = s.result.CreatedAt
		}
		if s.kind != "reasoning" {
			calls++
		}
	}
	if hasTurn && (start.IsZero() || working) {
		start = t.CreatedAt
	}
	took := time.Duration(0)
	if !start.IsZero() {
		if working {
			took = now.Sub(start)
		} else {
			took = end.Sub(start)
		}
		took = max(0, took).Truncate(time.Second)
	}

	var out []Line
	// In compact mode on finished turns, show each tool call as a single crisp
	// line if <= 4 calls, or fold into a clean summary line if longer (still
	// inspectable in full with /tools or Ctrl+T).
	if !working && work == prefs.WorkFolded && len(p.steps) > 4 {
		label := fmt.Sprintf("▸ %d steps · %s (Ctrl+T or /tools to inspect)", calls, dur(took))
		if calls == 0 {
			label = fmt.Sprintf("▸ thought · %s", dur(took))
		}
		if last && hasTurn && t.Status == wire.TurnStatusFailed {
			label = fmt.Sprintf("▸ stopped after %s · %d steps", dur(took), calls)
		}
		return []Line{{S(label, SFaint)}}
	}

	steps := p.steps
	if working && work == prefs.WorkFolded && len(steps) > 6 {
		out = append(out, Line{S(fmt.Sprintf("  … %d earlier steps", len(steps)-6), SFaint)})
		steps = steps[len(steps)-6:]
	}
	for i, s := range steps {
		running := working && i == len(steps)-1 && s.result == nil && s.kind != "reasoning"
		out = append(out, renderStep(s, running, w, work, frame)...)
	}
	return out
}

func renderStep(s *step, running bool, w int, work prefs.Work, frame int) []Line {
	if s.kind == "reasoning" {
		txt := strings.TrimSpace(s.think)
		if txt == "" {
			return nil
		}
		if work == prefs.WorkFolded {
			first, _, more := strings.Cut(txt, "\n")
			if more {
				first += " …"
			}
			return []Line{{S("  💭 ", SFaint), S(ansi.Truncate(first, max(10, w-5), "…"), SFaint.Italic(true))}}
		}
		wrapped := Wrap([]Seg{S(txt, SFaint.Italic(true))}, max(12, w-4))
		if work == prefs.WorkShown && len(wrapped) > 4 {
			wrapped = append(wrapped[:4], Line{S("…", SFaint)})
		}
		out := make([]Line, 0, len(wrapped))
		for i, wl := range wrapped {
			lead := S("    ", SFaint)
			if i == 0 {
				lead = S("  💭 ", SFaint)
			}
			out = append(out, append(Line{lead}, wl...))
		}
		return out
	}

	r := readResult(s.result)
	failure := ""
	if s.result != nil {
		failure = r.failure(s.kind)
	}
	dot := S("● ", SFaint)
	if running {
		dot = S(Spinner[frame%len(Spinner)]+" ", SAgent.Bold(true))
	} else if failure != "" {
		dot = S("✕ ", SDanger)
	} else if s.result != nil && !r.cancelled {
		dot = S("● ", SOk)
	}

	kindName := s.kind
	target := strings.TrimSpace(toolSummary(s))
	if nl := strings.IndexByte(target, '\n'); nl >= 0 {
		target = target[:nl] + " …"
	}
	// ACP harnesses (grok/agy) sometimes put the full command/path inside the
	// tool name (e.g. "execute `sleep 12`"). Normalize so we never duplicate it.
	if len(kindName) > 18 {
		if verb, rest, ok := strings.Cut(kindName, " "); ok && len(verb) <= 16 {
			rest = strings.Trim(strings.TrimSpace(rest), "`")
			kindName = verb
			if target == "" {
				target = rest
			}
		} else {
			kindName = ansi.Truncate(kindName, 18, "…")
		}
	} else if target != "" && strings.Contains(strings.ToLower(kindName), strings.ToLower(target)) {
		target = ""
	}

	var meta Line
	if s.call != nil && s.result != nil {
		d := s.result.CreatedAt.Sub(s.call.CreatedAt).Truncate(100 * time.Millisecond)
		if d > 0 {
			meta = append(meta, S(dur(d)+" ", SFaint))
		}
	}
	switch {
	case running:
		meta = append(meta, S("running", SAgent))
	case r.cancelled:
		meta = append(meta, S("stopped", SFaint))
	case failure != "":
		meta = append(meta, S("failed", SDanger))
	case s.kind == "edit":
		add, del := editCounts(s.args)
		if add > 0 || del > 0 {
			meta = append(meta, S(fmt.Sprintf("+%d", add), SOk), S(" ", SFaint), S(fmt.Sprintf("−%d", del), SDanger))
		} else {
			meta = append(meta, S("✓", SOk))
		}
	case s.screenshot():
		meta = append(meta, S("screenshot (/open)", SAgent))
	case s.result != nil:
		meta = append(meta, S("✓", SOk))
	}

	leftPrefix := Line{dot, S(kindName, SSec.Bold(true))}
	if target != "" {
		avail := max(8, w-leftPrefix.Width()-meta.Width()-3)
		leftPrefix = append(leftPrefix, S(" "+ansi.Truncate(target, avail, "…"), SMuted))
	}
	out := []Line{spread(leftPrefix, meta, w)}

	if (work == prefs.WorkFolded && !running) || s.result == nil || r.cancelled {
		return out
	}

	bodyW := max(12, w-4)
	clip := func(o string) string { return ansi.Truncate(strings.ReplaceAll(o, "\t", "  "), bodyW, "…") }
	under := func(ls ...Line) {
		for _, l := range ls {
			out = append(out, append(Line{S("  ▏ ", Fg(BorderSubtle))}, l...))
		}
	}

	text := strings.TrimRight(r.output(), "\n")
	if failure != "" && text == "" {
		under(Wrap([]Seg{S(failure, SDanger)}, bodyW)...)
		return out
	}

	switch {
	case s.kind == "edit":
		diffLines := renderEditDiff(s.args, bodyW, work == prefs.WorkFull)
		under(diffLines...)
		return out
	case s.kind == "file_change":
		if text != "" {
			under(renderPatchLines(text, bodyW, work == prefs.WorkFull)...)
		}
		return out
	case s.kind == "write" && work == prefs.WorkFull:
		if content, _ := s.args["content"].(string); content != "" {
			under(codeLines(content, filepath.Ext(target), bodyW)...)
		}
		return out
	case (s.kind == "read" || s.kind == "fs_read") && text != "" && failure == "":
		if work == prefs.WorkFull {
			under(codeLines(text, filepath.Ext(target), bodyW)...)
		} else {
			under(Line{S(fmt.Sprintf("%d lines", strings.Count(text, "\n")+1), SFaint)})
		}
		return out
	}

	if text == "" {
		return out
	}
	output := strings.Split(text, "\n")
	if work == prefs.WorkFull {
		const limit = 200
		for i, o := range output {
			if i == limit {
				under(Line{S(fmt.Sprintf("… %d more lines", len(output)-limit), SFaint)})
				break
			}
			st := SFaint
			if strings.Contains(o, "FAIL") || strings.Contains(o, "panic:") || strings.Contains(o, "Error") {
				st = SDanger
			}
			under(Line{S(clip(o), st)})
		}
		return out
	}
	last := telling(output)
	if last != "" {
		l := Line{S(ansi.Truncate(strings.ReplaceAll(last, "\t", "  "), max(8, bodyW-12), "…"), SFaint)}
		if more := len(output) - 1; more > 0 {
			l = append(l, S(fmt.Sprintf("  +%d lines", more), SFaint))
		}
		under(l)
	}
	return out
}

func renderEditDiff(args map[string]any, w int, full bool) []Line {
	var out []Line
	maxLines := 12
	if full {
		maxLines = 200
	}
	addLine := func(prefix, text string, st lipgloss.Style) bool {
		if len(out) >= maxLines {
			return false
		}
		out = append(out, Line{S(ansi.Truncate(prefix+" "+strings.ReplaceAll(text, "\t", "  "), w, "…"), st)})
		return true
	}
	emitPair := func(oldText, newText string) {
		if oldText != "" {
			for _, l := range strings.Split(strings.TrimSuffix(oldText, "\n"), "\n") {
				if !addLine("-", l, SDanger) {
					return
				}
			}
		}
		if newText != "" {
			for _, l := range strings.Split(strings.TrimSuffix(newText, "\n"), "\n") {
				if !addLine("+", l, SOk) {
					return
				}
			}
		}
	}
	if edits, ok := args["edits"].([]any); ok {
		for _, e := range edits {
			m, _ := e.(map[string]any)
			oldT, _ := m["old_text"].(string)
			newT, _ := m["new_text"].(string)
			emitPair(oldT, newT)
		}
	} else {
		oldT, _ := args["old_string"].(string)
		if oldT == "" {
			oldT, _ = args["old_text"].(string)
		}
		newT, _ := args["new_string"].(string)
		if newT == "" {
			newT, _ = args["new_text"].(string)
		}
		emitPair(oldT, newT)
	}
	return out
}

func renderPatchLines(text string, w int, full bool) []Line {
	lines := strings.Split(text, "\n")
	limit := 12
	if full {
		limit = 200
	}
	var out []Line
	for i, l := range lines {
		if i == limit {
			out = append(out, Line{S(fmt.Sprintf("… %d more lines", len(lines)-limit), SFaint)})
			break
		}
		st := SFaint
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			st = SOk
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			st = SDanger
		case strings.HasPrefix(l, "@@"):
			st = SAgent
		}
		out = append(out, Line{S(ansi.Truncate(strings.ReplaceAll(l, "\t", "  "), w, "…"), st)})
	}
	return out
}

func renderWorkingLine(c *conversation, p part, frame int, now time.Time, cancelOn string) Line {
	t, hasTurn := c.turns[p.turnID]
	word := "Working"
	switch {
	case cancelOn == p.turnID || (hasTurn && t.CancelRequested):
		word = "Stopping"
	case c.session.MachineStatus == wire.MachineStatusHydrating:
		word = "Waking workspace"
	case hasTurn && t.Status == wire.TurnStatusCompacting:
		word = "Compacting context"
	}
	l := Line{S(Spinner[frame%len(Spinner)]+" ", SAccent.Bold(true)), S(word, SText.Bold(true))}
	if hasTurn && !t.CreatedAt.IsZero() {
		l = append(l, S(" · "+dur(max(0, now.Sub(t.CreatedAt)).Truncate(time.Second)), SMuted))
	}
	l = append(l, S(" · Esc to stop", SFaint))
	return l
}

func renderTurnEnd(t wire.Turn, w int, now time.Time) Line {
	var l Line
	switch t.Status {
	case wire.TurnStatusFailed:
		l = Line{S("✕ ", SDanger), S("failed", SFaint)}
	case wire.TurnStatusCancelled:
		l = Line{S("■ ", SFaint), S("stopped", SFaint)}
	default:
		l = Line{S("✓ ", SFaint), S("done", SFaint)}
	}
	if !t.CompletedAt.IsZero() {
		l = append(l, S(" · "+clock(t.CompletedAt, now), SFaint))
		if !t.CreatedAt.IsZero() && t.CompletedAt.After(t.CreatedAt) {
			l = append(l, S(" · "+dur(t.CompletedAt.Sub(t.CreatedAt).Truncate(time.Second)), SFaint))
		}
	}
	rem := max(0, w-l.Width()-1)
	if rem > 0 {
		l = append(l, S(" "+strings.Repeat("─", rem), Fg(BorderSubtle)))
	}
	return l
}

func renderProposal(c *conversation, p part, w int, frame int) []Line {
	var view wire.SessionProposalView
	known := false
	if p.result != nil {
		view, known = c.proposals[p.result.Sequence]
	}
	title, reason := view.Proposal.Title, view.Proposal.Reason
	if !known && len(p.steps) > 0 {
		title, _ = p.steps[0].args["title"].(string)
		reason, _ = p.steps[0].args["reason"].(string)
	}
	head := Line{S("◆ ", SAccent.Bold(true)), S("Proposed new session", SAccent.Bold(true))}
	edge := Accent
	if known && view.Decision != nil {
		head, edge = Line{S("◆ ", SFaint), S("Proposed new session", SMuted)}, BorderDefault
	}
	body := []Line{{S(title, SText.Bold(true))}}
	if reason != "" {
		body = append(body, Wrap([]Seg{S(reason, SSec)}, max(12, w-2))...)
	}
	switch {
	case !known && p.result != nil:
		body = append(body, Line{S(readResult(p.result).failure("new_session"), SMuted)})
	case !known:
		body = append(body, Line{S(Spinner[frame%len(Spinner)]+" proposing…", SAgent)})
	case view.Decision == nil:
		body = append(body, Line{S("Open /settings to approve or dismiss this proposal.", SMuted)})
	case view.Decision.Status == "approved":
		body = append(body, Line{S("✓ Approved", SOk)})
	default:
		body = append(body, Line{S("Dismissed", SMuted)})
	}
	return append([]Line{head}, ruled(body, edge)...)
}

func toolSummary(s *step) string {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := s.args[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	switch s.kind {
	case "web_search":
		return str("query", "q")
	case "bash", "shell", "exec":
		return str("command", "cmd")
	case "read", "write", "edit":
		return str("file_path", "path")
	case "fs_list", "fs_read", "publish":
		return str("path")
	case "file_change":
		if len(s.arrayArgs) > 0 {
			var files []string
			for _, item := range s.arrayArgs {
				if m, ok := item.(map[string]any); ok {
					p, _ := m["path"].(string)
					k, _ := m["kind"].(string)
					if p != "" {
						if k != "" {
							files = append(files, p+" ("+k+")")
						} else {
							files = append(files, p)
						}
					}
				}
			}
			if len(files) > 0 {
				return strings.Join(files, ", ")
			}
		}
		return str("path", "file_path")
	}
	if v := str("command", "file_path", "path", "query", "pattern", "url"); v != "" {
		return v
	}
	var parts []string
	for k, v := range s.args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func codeLines(content, ext string, w int) []Line {
	lines := strings.Split(content, "\n")
	more := ""
	if len(lines) > 150 {
		more = fmt.Sprintf("… %d more lines", len(lines)-150)
		lines = lines[:150]
	}
	out := CodeBlock(strings.Join(lines, "\n"), strings.TrimPrefix(ext, "."), w)
	if more != "" {
		out = append(out, Line{S(more, SFaint)})
	}
	return out
}

func telling(output []string) string {
	for _, o := range output {
		if strings.Contains(o, "FAIL") || strings.Contains(o, "panic:") {
			return o
		}
	}
	for i := len(output) - 1; i >= 0; i-- {
		if strings.TrimSpace(output[i]) != "" {
			return output[i]
		}
	}
	return ""
}

func editCounts(args map[string]any) (add, del int) {
	countPair := func(oldText, newText string) {
		have := map[string]int{}
		for _, l := range strings.Split(oldText, "\n") {
			if l != "" {
				have[l]++
			}
		}
		for _, l := range strings.Split(newText, "\n") {
			if l == "" {
				continue
			}
			if have[l] > 0 {
				have[l]--
				continue
			}
			add++
		}
		for _, n := range have {
			del += n
		}
	}
	if edits, ok := args["edits"].([]any); ok {
		for _, e := range edits {
			m, _ := e.(map[string]any)
			oldT, _ := m["old_text"].(string)
			newT, _ := m["new_text"].(string)
			countPair(oldT, newT)
		}
		return add, del
	}
	oldT, _ := args["old_string"].(string)
	if oldT == "" {
		oldT, _ = args["old_text"].(string)
	}
	newT, _ := args["new_string"].(string)
	if newT == "" {
		newT, _ = args["new_text"].(string)
	}
	countPair(oldT, newT)
	return add, del
}

func dur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second && d%time.Second != 0:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

func clock(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	switch {
	case t.YearDay() == now.YearDay() && t.Year() == now.Year():
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

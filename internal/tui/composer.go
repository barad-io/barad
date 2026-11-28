package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/wire"
)

// slashCmd defines one interactive slash command available from the composer.
type slashCmd struct {
	name    string
	aliases []string
	hint    string
	run     func(a *app, arg string) tea.Cmd
}

var slashCommands = []slashCmd{
	{name: "/sessions", aliases: []string{"/s"}, hint: "Fuzzy session picker (Ctrl+P)", run: func(a *app, arg string) tea.Cmd { return a.openPicker(arg) }},
	{name: "/new", aliases: []string{"/n"}, hint: "Create a new session", run: func(a *app, arg string) tea.Cmd { return a.openNewSession(arg) }},
	{name: "/model", aliases: []string{"/m"}, hint: "Switch model, reasoning effort, or fast mode", run: func(a *app, _ string) tea.Cmd { return a.openModelOverlay() }},
	{name: "/login", hint: "Sign in to the session's coding agent CLI", run: func(a *app, _ string) tea.Cmd { return a.execLogin() }},
	{name: "/shell", aliases: []string{"/sh"}, hint: "Open interactive shell in workspace VM", run: func(a *app, _ string) tea.Cmd { return a.execShell() }},
	{name: "/settings", hint: "Rename, project, repos, env vars, wake, share, archive", run: func(a *app, _ string) tea.Cmd { return a.openSettingsOverlay() }},
	{name: "/files", aliases: []string{"/f"}, hint: "Browse /workspace files and preview", run: func(a *app, arg string) tea.Cmd { return a.openFilesOverlay(arg) }},
	{name: "/tools", hint: "Inspect full tool diffs/outputs & load older history", run: func(a *app, _ string) tea.Cmd { return a.openToolsOverlay() }},
	{name: "/projects", hint: "Manage projects and default harness settings", run: func(a *app, _ string) tea.Cmd { return a.openProjectsOverlay() }},
	{name: "/usage", hint: "Token and workspace compute usage (7/30/90d)", run: func(a *app, _ string) tea.Cmd { return a.openUsageOverlay() }},
	{name: "/billing", hint: "Credits balance, history, terms, buy credits", run: func(a *app, _ string) tea.Cmd { return a.openBillingOverlay() }},
	{name: "/account", hint: "Account, GitHub, git identity, theme & keybindings", run: func(a *app, _ string) tea.Cmd { return a.openAccountOverlay() }},
	{name: "/continue", hint: "Continue a legacy read-only session into v2", run: func(a *app, _ string) tea.Cmd { return a.continueCurrentSession() }},
	{name: "/open", hint: "Open latest screenshot or attached image URL", run: func(a *app, _ string) tea.Cmd { return a.openLatestMedia() }},
	{name: "/stop", hint: "Stop the active turn gracefully", run: func(a *app, _ string) tea.Cmd { return a.cancelTurn() }},
	{name: "/quit", aliases: []string{"/exit", "/q"}, hint: "Exit barad", run: func(a *app, _ string) tea.Cmd { return a.quit() }},
}

// slashMatches returns commands matching a composer prefix like "/mo" or "/".
func slashMatches(input string) []slashCmd {
	if !strings.HasPrefix(input, "/") || strings.ContainsAny(input, " \n\t") {
		return nil
	}
	q := strings.ToLower(input)
	var out []slashCmd
	for _, cmd := range slashCommands {
		if strings.HasPrefix(cmd.name, q) || Fuzzy(strings.TrimPrefix(q, "/"), strings.TrimPrefix(cmd.name, "/")) {
			out = append(out, cmd)
			continue
		}
		for _, al := range cmd.aliases {
			if strings.HasPrefix(al, q) {
				out = append(out, cmd)
				break
			}
		}
	}
	return out
}

// runSlash executes a submitted "/cmd [arg]" line if it matches a known command.
func (a *app) runSlash(raw string) (tea.Cmd, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") {
		return nil, false
	}
	name, arg, _ := strings.Cut(trimmed, " ")
	name = strings.ToLower(name)
	for _, cmd := range slashCommands {
		if cmd.name == name {
			a.comp.Reset()
			a.compSel = 0
			return cmd.run(a, strings.TrimSpace(arg)), true
		}
		for _, al := range cmd.aliases {
			if al == name {
				a.comp.Reset()
				a.compSel = 0
				return cmd.run(a, strings.TrimSpace(arg)), true
			}
		}
	}
	// If single unambiguous prefix match and no arg, run it.
	if arg == "" {
		if m := slashMatches(name); len(m) == 1 {
			a.comp.Reset()
			a.compSel = 0
			return m[0].run(a, ""), true
		}
	}
	a.flash("Unknown command "+name+" — type / to see commands", true)
	return nil, true
}

// atToken finds an active "@prefix" token right before the cursor in the composer.
func atToken(e *Editor) (start int, prefix string, ok bool) {
	if e.Pos == 0 || e.Pos > len(e.Text) {
		return 0, "", false
	}
	i := e.Pos - 1
	for i >= 0 && e.Text[i] != ' ' && e.Text[i] != '\n' && e.Text[i] != '\t' {
		if e.Text[i] == '@' {
			if i == 0 || e.Text[i-1] == ' ' || e.Text[i-1] == '\n' || e.Text[i-1] == '\t' {
				return i, string(e.Text[i+1 : e.Pos]), true
			}
			return 0, "", false
		}
		i--
	}
	return 0, "", false
}

// atMatches returns cached workspace paths matching "@prefix".
func (a *app) atMatches(prefix string) []string {
	if a.conv == nil {
		return nil
	}
	files := a.wsFiles[a.conv.id]
	if len(files) == 0 {
		return nil
	}
	var out []string
	for _, f := range files {
		if prefix == "" || Fuzzy(prefix, f) {
			out = append(out, f)
			if len(out) >= 8 {
				break
			}
		}
	}
	return out
}

// completionItems returns the current inline completion popup rows (either /cmd or @file).
type completionItem struct {
	label string
	hint  string
	apply func(a *app) tea.Cmd
}

func (a *app) completions() []completionItem {
	val := a.comp.Value()
	if matches := slashMatches(val); len(matches) > 0 {
		out := make([]completionItem, 0, min(len(matches), 8))
		for _, cmd := range matches {
			c := cmd
			out = append(out, completionItem{
				label: c.name,
				hint:  c.hint,
				apply: func(a *app) tea.Cmd {
					a.comp.Reset()
					a.compSel = 0
					return c.run(a, "")
				},
			})
			if len(out) >= 8 {
				break
			}
		}
		return out
	}
	if start, prefix, ok := atToken(&a.comp); ok {
		files := a.atMatches(prefix)
		out := make([]completionItem, 0, len(files))
		for _, f := range files {
			path := f
			out = append(out, completionItem{
				label: "@" + path,
				hint:  "workspace",
				apply: func(a *app) tea.Cmd {
					// Replace "@prefix" with "@path "
					before := append([]rune{}, a.comp.Text[:start]...)
					ins := []rune("@" + path + " ")
					after := a.comp.Text[a.comp.Pos:]
					a.comp.Text = append(before, append(ins, after...)...)
					a.comp.Pos = start + len(ins)
					a.compSel = 0
					return nil
				},
			})
		}
		return out
	}
	return nil
}

// renderQueuedBanner renders uniform post-turn queued prompts held by the server
// or currently uploading.
func (a *app) renderQueuedBanner(w int) []Line {
	if a.conv == nil {
		return nil
	}
	var rows []Line
	for _, p := range a.conv.pending {
		first, _, more := strings.Cut(strings.TrimSpace(p.Text), "\n")
		if more {
			first += " …"
		}
		if first == "" && len(p.Media) > 0 {
			first = fmt.Sprintf("[%d attachment(s)]", len(p.Media))
		}
		badge := "Queued · runs after current turn: "
		if a.conv.session.AuthRequired() {
			badge = "Queued · runs after /login: "
		} else if a.conv.session.CurrentTurnID == "" {
			badge = "Queued · starting: "
		}
		avail := max(10, w-ansi.StringWidth(badge)-4)
		rows = append(rows, Line{S("↳ ", SAgent.Bold(true)), S(badge, SAgent), S(ansi.Truncate(first, avail, "…"), SSec)})
	}
	for _, p := range a.conv.sending {
		first, _, _ := strings.Cut(strings.TrimSpace(p.Text), "\n")
		avail := max(10, w-14)
		rows = append(rows, Line{S(Spinner[a.frame%len(Spinner)]+" ", SAccent), S("Sending: ", SMuted), S(ansi.Truncate(first, avail, "…"), SSec)})
	}
	return rows
}

// renderComposer draws the pinned bottom composer with completion popup,
// attachments, multiline editor, and thin top divider.
func (a *app) renderComposer(w int) []Line {
	w = max(w, 28)
	var out []Line

	// Inline completion popup (/commands or @workspace-files) sits cleanly above the divider.
	if comps := a.completions(); len(comps) > 0 && a.ov == nil {
		sel := min(max(0, a.compSel), len(comps)-1)
		for i, c := range comps {
			prefix := "  "
			labelSt, hintSt := SSec, SFaint
			if i == sel {
				prefix = "▸ "
				labelSt = SAccent.Bold(true)
				hintSt = SMuted
			}
			left := Line{S(prefix, labelSt), S(PadRight(c.label, min(18, max(10, w/3))), labelSt)}
			rem := max(0, w-left.Width()-1)
			if rem > 4 && c.hint != "" {
				left = append(left, S(" "+ansi.Truncate(c.hint, rem, "…"), hintSt))
			}
			out = append(out, left)
		}
	}

	out = append(out, Line{S(strings.Repeat("─", w), Fg(BorderSubtle))})

	// Attachment chips (if any).
	if len(a.attach) > 0 {
		var chips Line
		chips = append(chips, S("  ", SText))
		for i, at := range a.attach {
			if i > 0 {
				chips = append(chips, S("  ", SText))
			}
			chips = append(chips, S("["+attachMark(at.mediaType)+at.name+"]", SAgent))
		}
		chips = append(chips, S("  (Backspace removes)", SFaint))
		out = append(out, chips)
	}

	placeholder := "Message agent… (/ for commands, @ for files, Ctrl+P sessions)"
	if w < 62 {
		placeholder = "Message… (/ cmds, Ctrl+P sessions)"
	}
	if a.conv == nil {
		placeholder = "Select a session with /sessions or /new"
	} else if !a.conv.session.ArchivedAt.IsZero() || a.conv.session.MachineStatus == wire.MachineStatusArchived {
		placeholder = "Archived (read-only) · /continue to fork · /sessions to switch"
	} else if !a.conv.session.IsV2() {
		placeholder = "Legacy read-only session · run /continue to continue in v2"
	} else if a.conv.session.CurrentTurnID != "" {
		placeholder = "Queue follow-up message (runs after current turn)… Esc to stop"
		if w < 62 {
			placeholder = "Queue next prompt… (Esc stops)"
		}
	}

	focused := a.ov == nil
	edLines := a.comp.Lines(max(12, w-2), 6, SText, placeholder, focused)
	for i, el := range edLines {
		prefix := S("  ", SText)
		if i == 0 {
			prefix = S("❯ ", SAccent.Bold(true))
		}
		out = append(out, append(Line{prefix}, el...))
	}
	return out
}

// renderFooter draws the single thin bottom status line:
//
//	session name · harness / model / effort (+ fast) · ctx % · state
//
// Adapts gracefully down to narrow Rex/tmux splits (~32 cols).
func (a *app) renderFooter(w int) Line {
	w = max(w, 24)
	if a.toast != "" && a.now.Sub(a.toastAt) < 4*time.Second {
		st := SOk
		if a.toastBad {
			st = SDanger
		}
		return Line{S(ansi.Truncate(a.toast, w, "…"), st)}
	}
	if a.conv == nil {
		left := Line{S("barad", SAccent.Bold(true)), S(" · ", SFaint), S("/sessions · /new · /quit", SFaint)}
		if !a.online {
			return spread(left, Line{S("connecting…", SWarn)}, w)
		}
		return left
	}

	s := a.conv.session
	stateWord, stateSt := footerState(s, a.conv)
	right := Line{S(stateWord, stateSt)}
	if !a.online {
		right = append(Line{S("offline · ", SWarn)}, right...)
	}

	// Context percentage (Claude, Codex, Antigravity report context_window_tokens).
	ctxStr := ""
	if s.ContextWindowTokens > 0 && s.LatestProviderUsage.ContextTokens > 0 {
		pct := min(100, (s.LatestProviderUsage.ContextTokens*100)/s.ContextWindowTokens)
		ctxStr = fmt.Sprintf("%d%%", pct)
	}

	// Build harness / model / effort (+ fast) string.
	harness := harnessLabel(s.HarnessID())
	modelStr := s.Model
	if modelStr == "" || modelStr == "default" {
		modelStr = "default"
	}
	spec := harness + "/" + modelStr
	if s.ReasoningEffort != "" {
		spec += "/" + string(s.ReasoningEffort)
	}
	if s.FastMode {
		spec += " +fast"
	}

	name := sessionTitle(s)
	availLeft := max(8, w-right.Width()-2)

	// Narrow mode (< 56 cols): show compact "name · harness · ctx%" or "harness/model · ctx%".
	if w < 56 {
		compact := ansi.Truncate(name, max(8, availLeft/2), "…") + " · " + harness
		if ctxStr != "" {
			compact += " · " + ctxStr
		}
		return spread(Line{S(ansi.Truncate(compact, availLeft, "…"), SMuted)}, right, w)
	}

	var left Line
	// Allocate space cleanly between session name, spec, and ctx %.
	suffix := " · " + spec
	if ctxStr != "" {
		suffix += " · ctx " + ctxStr
	}
	maxName := availLeft - ansi.StringWidth(suffix)
	if maxName < 10 {
		// Truncate spec if very long so name still gets >= 10 cols.
		maxName = min(len(name), max(10, availLeft/3))
		left = append(left, S(ansi.Truncate(name, maxName, "…"), SSec.Bold(true)))
		rem := max(0, availLeft-left.Width())
		left = append(left, S(ansi.Truncate(suffix, rem, "…"), SFaint))
	} else {
		left = append(left, S(ansi.Truncate(name, maxName, "…"), SSec.Bold(true)))
		left = append(left, S(" · ", SFaint), S(spec, SMuted))
		if ctxStr != "" {
			ctxSt := SFaint
			if s.ContextWindowTokens > 0 && s.LatestProviderUsage.ContextTokens*100/s.ContextWindowTokens >= 80 {
				ctxSt = SWarn
			}
			left = append(left, S(" · ctx "+ctxStr, ctxSt))
		}
	}
	return spread(left, right, w)
}

func footerState(s wire.Session, c *conversation) (string, lipgloss.Style) {
	switch {
	case !s.ArchivedAt.IsZero() || s.MachineStatus == wire.MachineStatusArchived:
		return "archived", SFaint
	case s.MachineStatus == wire.MachineStatusBroken:
		return "broken", SDanger.Bold(true)
	case s.CurrentTurnID != "":
		return "working", SAgent.Bold(true)
	case s.AuthRequired():
		return "sign-in required", SWarn.Bold(true)
	case s.MachineStatus == wire.MachineStatusHydrating:
		return "waking", SAgent
	case s.PendingTurn || (c != nil && len(c.pending) > 0):
		return "queued", SAgent
	case s.MachineStatus == wire.MachineStatusHydrated:
		return "awake", SOk
	default:
		return "sleeping", SFaint
	}
}

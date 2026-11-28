package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/barad-io/barad/wire"
)

// statusTracker emits Rex Program Status Protocol (OSC 7501) sequences strictly
// for the currently attached session in this terminal pane. Each pane reports
// its own attached session independently so side-by-side Rex tabs/splits show
// exact per-pane status without colliding.
type statusTracker struct {
	lastSeq    string
	doneTurnID string // turn whose completion triggered state=done; cleared on next keypress
}

// compute derives the OSC 7501 ProgramStatus for the currently attached session.
func (st *statusTracker) compute(c *conversation) ansi.ProgramStatus {
	if c == nil {
		return ansi.ProgramStatus{
			State: ansi.ProgramStateIdle,
			App:   "barad",
			Title: "barad",
		}
	}
	s := c.session
	title := sessionTitle(s)
	if len(title) > 64 {
		title = title[:64]
	}

	switch {
	case s.MachineStatus == wire.MachineStatusBroken:
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateError,
			App:     "barad",
			Title:   title,
			Message: "Machine error",
		}
	case s.CurrentTurnID != "":
		msg := "Working"
		if t, ok := c.turns[s.CurrentTurnID]; ok && t.Status == wire.TurnStatusCompacting {
			msg = "Compacting context"
		} else if s.MachineStatus == wire.MachineStatusHydrating {
			msg = "Waking workspace"
		}
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateWorking,
			App:     "barad",
			Title:   title,
			Message: msg,
		}
	case s.AuthRequired():
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateBlocked,
			Kind:    ansi.ProgramStatusKindAuth,
			App:     "barad",
			Title:   title,
			Message: "Sign-in required (" + harnessLabel(s.HarnessID()) + ")",
		}
	case s.MachineStatus == wire.MachineStatusHydrating:
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateWorking,
			App:     "barad",
			Title:   title,
			Message: "Waking workspace",
		}
	case s.PendingTurn || len(c.pending) > 0 || len(c.sending) > 0:
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateWorking,
			App:     "barad",
			Title:   title,
			Message: "Starting turn",
		}
	case s.LatestTerminalTurnID != "" && s.LatestTerminalTurnStatus == wire.TurnStatusFailed:
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateError,
			App:     "barad",
			Title:   title,
			Message: "Turn failed",
		}
	}

	if _, hasProposal := c.openProposal(); hasProposal {
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateBlocked,
			Kind:    ansi.ProgramStatusKindPermission,
			App:     "barad",
			Title:   title,
			Message: "Session proposal waiting",
		}
	}

	if st.doneTurnID != "" && s.LatestTerminalTurnID == st.doneTurnID && s.LatestTerminalTurnStatus == wire.TurnStatusCompleted {
		return ansi.ProgramStatus{
			State:   ansi.ProgramStateDone,
			App:     "barad",
			Title:   title,
			Message: "Turn completed",
		}
	}

	return ansi.ProgramStatus{
		State: ansi.ProgramStateIdle,
		App:   "barad",
		Title: title,
	}
}

// markTurnCompleted marks turnID as newly completed so OSC 7501 reports state=done
// until the user interacts with this pane.
func (st *statusTracker) markTurnCompleted(turnID string) {
	if turnID != "" {
		st.doneTurnID = turnID
	}
}

// ackUserInput clears the transient "done" state back to "idle".
func (st *statusTracker) ackUserInput() {
	st.doneTurnID = ""
}

// emit returns a tea.Raw command if the OSC 7501 sequence changed since the last emission.
func (st *statusTracker) emit(c *conversation) tea.Cmd {
	seq := ansi.SetProgramStatus(st.compute(c))
	if seq == "" || seq == st.lastSeq {
		return nil
	}
	st.lastSeq = seq
	return tea.Raw(seq)
}

// clear emits OSC 7501 clear on exit or detach.
func (st *statusTracker) clear() tea.Cmd {
	st.lastSeq = ""
	st.doneTurnID = ""
	return tea.Raw(ansi.ClearProgramStatus)
}

func sessionTitle(s wire.Session) string {
	if n := strings.TrimSpace(s.Name); n != "" {
		return n
	}
	if s.SessionID != "" {
		return s.SessionID
	}
	return "Untitled session"
}

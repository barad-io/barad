package api

import "github.com/barad-io/barad/wire"

// Group is where a session files on a list: what needs you comes first.
type Group int

const (
	NeedsYou Group = iota // a reply you have not read, a failure
	Running
	Resting
)

// Standing is a session's state as the web app's sidebar derives it: one
// word, and the group it files under.
type Standing struct {
	Word  string // failed, working, waking, ready, asleep, idle or archived
	Group Group
}

// StandingOf derives a session's standing. read is the newest finished turn
// you have seen; held is whether a message of yours waits for it.
func StandingOf(s wire.Session, read string, held bool) Standing {
	held = held || s.PendingTurn
	asleep := s.MachineStatus == wire.MachineStatusDehydrated || s.MachineStatus == wire.MachineStatusDehydrating
	switch {
	case !s.ArchivedAt.IsZero():
		return Standing{"archived", Resting}
	case s.MachineStatus == wire.MachineStatusBroken:
		return Standing{"failed", NeedsYou}
	case s.CurrentTurnID != "":
		return Standing{"working", Running}
	case s.MachineStatus == wire.MachineStatusHydrating:
		return Standing{"waking", Running}
	case s.MachineStatus == wire.MachineStatusDehydrated && s.PendingTurn:
		return Standing{"asleep", Resting}
	case held && asleep:
		return Standing{"waking", Running}
	case held:
		return Standing{"working", Running}
	case s.LatestTerminalTurnID != "" && s.LatestTerminalTurnStatus == wire.TurnStatusFailed:
		return Standing{"failed", NeedsYou}
	case Unread(s, read):
		return Standing{"ready", NeedsYou}
	case asleep:
		return Standing{"asleep", Resting}
	}
	return Standing{"idle", Resting}
}

// Unread reports whether a session finished a turn you have not seen.
func Unread(s wire.Session, read string) bool {
	return s.LatestTerminalTurnID != "" && s.LatestTerminalTurnID != read
}

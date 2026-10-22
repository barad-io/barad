// Package prefs is what barad remembers on this computer between runs: how
// the app looks and behaves, and which replies you have read. The app and
// the commands share one file; every change reads it fresh and writes it
// back whole, so a change made by one is not lost to the other.
package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Work is how a finished run shows its tool calls. Like the web app, a run
// that finishes folds into one row; a working run always shows its steps.
type Work int

const (
	WorkFolded Work = iota // the run's row alone
	WorkShown              // one row per step, each result cut to its telling line
	WorkFull               // every result whole
)

func (w Work) String() string { return [...]string{"folded", "shown", "full"}[w] }

type Prefs struct {
	Theme   string `json:"theme"`   // night or paper
	Project string `json:"project"` // the project the home screen shows
	Work    Work   `json:"work"`
	// Reviewed is, for each session, the newest finished turn you have
	// seen. Nil until the first list arrives, which counts as read: the
	// web app assumes the same.
	Reviewed map[string]string `json:"reviewed"`
	// Newline makes Enter start a new line in the composer; Alt+Enter sends.
	Newline bool `json:"enter_newline"`
	// Silent stops the desktop notification when a session finishes out of sight.
	Silent bool `json:"silent"`
	// The newest release, as last looked up (at most daily): the home
	// screen mentions it while it is newer than the one running.
	Latest          string    `json:"latest"`
	LatestCheckedAt time.Time `json:"latest_checked_at"`
}

func path() string {
	dir, err := api.ConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "prefs.json")
}

// Load reads the preferences; a missing or unreadable file is the defaults.
func Load() Prefs {
	var p Prefs
	if file := path(); file != "" {
		if data, err := os.ReadFile(file); err == nil {
			_ = json.Unmarshal(data, &p)
		}
	}
	if p.Work < WorkFolded || p.Work > WorkFull {
		p.Work = WorkFolded
	}
	if p.Theme != "paper" {
		p.Theme = "night"
	}
	return p
}

// Update applies change to the preferences as they are on disk now, saves
// them, and returns them.
func Update(change func(p *Prefs)) (Prefs, error) {
	p := Load()
	change(&p)
	file := path()
	if file == "" {
		return p, os.ErrNotExist
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return p, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return p, err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return p, err
	}
	return p, os.Rename(tmp, file)
}

// Read records that you have seen these sessions' newest finished turns.
func (p *Prefs) Read(list []wire.Session) {
	if p.Reviewed == nil {
		p.Reviewed = map[string]string{}
	}
	for _, s := range list {
		if s.LatestTerminalTurnID != "" {
			p.Reviewed[s.SessionID] = s.LatestTerminalTurnID
		}
	}
}

// Unread marks a session's newest turn as not seen.
func (p *Prefs) Unread(sessionID string) {
	if p.Reviewed == nil {
		p.Reviewed = map[string]string{}
	}
	p.Reviewed[sessionID] = ""
}

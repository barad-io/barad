package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/barad-io/barad/internal/prefs"
)

// This computer's preferences, which the app shares, and the account.

// setting is a preference `barad config` names, with its values.
type setting struct {
	key, about string
	values     []string // the first is the default
	get        func(p prefs.Prefs) string
	set        func(p *prefs.Prefs, v string)
}

var settings = []setting{
	{"theme", "the app's colours", []string{"night", "paper"},
		func(p prefs.Prefs) string { return p.Theme }, func(p *prefs.Prefs, v string) { p.Theme = v }},
	{"enter", "what Enter does in the composer (the other is Alt+Enter)", []string{"send", "newline"},
		func(p prefs.Prefs) string { return onOff(p.Newline, "newline", "send") },
		func(p *prefs.Prefs, v string) { p.Newline = v == "newline" }},
	{"notify", "a desktop notification when a session finishes while the app is open", []string{"on", "off"},
		func(p prefs.Prefs) string { return onOff(p.Silent, "off", "on") },
		func(p *prefs.Prefs, v string) { p.Silent = v == "off" }},
	{"steps", "how the app shows a finished turn's work", []string{"folded", "shown", "full"},
		func(p prefs.Prefs) string { return p.Work.String() },
		func(p *prefs.Prefs, v string) {
			p.Work = prefs.Work(slices.Index([]string{"folded", "shown", "full"}, v))
		}},
}

func onOff(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

var configCommand = command{
	usage: "config [KEY [VALUE]]",
	about: "this computer's preferences for the app: theme, enter, notify, steps",
	run: func(c *call) error {
		p := prefs.Load()
		if len(c.args) == 0 {
			out := map[string]string{}
			var rows [][]string
			for _, s := range settings {
				out[s.key] = s.get(p)
				rows = append(rows, []string{s.key, s.get(p), strings.Join(s.values, " | "), s.about})
			}
			return c.table(out, []string{"KEY", "VALUE", "CHOICES", "WHAT IT IS"}, rows)
		}
		i := slices.IndexFunc(settings, func(s setting) bool { return s.key == c.args[0] })
		if i < 0 {
			var keys []string
			for _, s := range settings {
				keys = append(keys, s.key)
			}
			return fmt.Errorf("no preference %q; there are %s", c.args[0], strings.Join(keys, ", "))
		}
		s := settings[i]
		if len(c.args) == 1 {
			return c.say(map[string]string{s.key: s.get(p)}, "%s", s.get(p))
		}
		v := c.args[1]
		if !slices.Contains(s.values, v) {
			return fmt.Errorf("%s is %s", s.key, strings.Join(s.values, " or "))
		}
		if _, err := prefs.Update(func(p *prefs.Prefs) { s.set(p, v) }); err != nil {
			return err
		}
		return c.say(map[string]string{s.key: v}, "%s: %s", s.key, v)
	},
}

var accountShow = command{
	usage: "account [show]",
	about: "who you are signed in as",
	run: func(c *call) error {
		email := c.api.Email()
		return c.say(map[string]string{"email": email}, "Signed in as %s.", firstNonEmpty(email, "an account without an email"))
	},
}

var accountPassword = command{
	usage: "account password",
	about: "change your Barad password (it asks for the current one, then the new one twice)",
	run: func(c *call) error {
		if _, ok := c.terminal(); !ok {
			return errors.New("changing the password needs a terminal to type it in")
		}
		previous, err := c.readValue("Current password", true)
		if err != nil {
			return err
		}
		proposed, err := c.readValue("New password", true)
		if err != nil {
			return err
		}
		again, err := c.readValue("New password again", true)
		if err != nil {
			return err
		}
		if proposed != again {
			return errors.New("the new passwords differ; nothing changed")
		}
		if err := c.api.ChangePassword(c.ctx, previous, proposed); err != nil {
			return err
		}
		return c.say(map[string]bool{"changed": true}, "Changed your password.")
	},
}

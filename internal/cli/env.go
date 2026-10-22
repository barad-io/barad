package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"golang.org/x/term"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Variables belong to a session, or to a project, whose sessions all
// receive them live (a session's own variable of the same name wins).

func ownerFlags(fs *flag.FlagSet, c *call) {
	strFlag(fs, c, "session", "the session whose variables these are")
	strFlag(fs, c, "project", "the project whose variables these are; its sessions all receive them")
}

// owner is whose variables the command changes, and what to call it.
func (c *call) owner() (api.EnvironmentOwner, string, error) {
	switch s, p := c.str("session"), c.str("project"); {
	case s != "" && p != "":
		return api.EnvironmentOwner{}, "", errors.New("name either --session or --project")
	case s != "":
		session, err := c.session(s)
		return api.EnvironmentOwner{Kind: "sessions", ID: session.SessionID}, sessionName(session), err
	case p != "":
		project, err := c.project(p)
		return api.EnvironmentOwner{Kind: "projects", ID: project.ProjectID}, project.Name, err
	}
	return api.EnvironmentOwner{}, "", errors.New("name the --session or the --project")
}

var envList = command{
	usage: "env [list] --session S | --project P",
	about: "the variables a session or a project has; secret values never show",
	flags: ownerFlags,
	run: func(c *call) error {
		o, _, err := c.owner()
		if err != nil {
			return err
		}
		env, err := c.api.Environment(c.ctx, o)
		if err != nil {
			return err
		}
		var rows [][]string
		for _, v := range env.Variables {
			value := v.Value
			if v.Secret {
				value = "(secret)"
			}
			rows = append(rows, []string{v.Name, value})
		}
		return c.table(env.Variables, []string{"NAME", "VALUE"}, rows)
	},
}

var envSet = command{
	usage: "env set NAME[=VALUE] --session S | --project P [--secret]",
	about: "set a variable; without =VALUE it is read from the terminal (hidden for a secret) or stdin",
	flags: func(fs *flag.FlagSet, c *call) {
		ownerFlags(fs, c)
		boolFlag(fs, c, "secret", "never show the value again, anywhere")
	},
	run: func(c *call) error {
		spec, err := c.arg(0, "NAME[=VALUE]")
		if err != nil {
			return err
		}
		o, whose, err := c.owner()
		if err != nil {
			return err
		}
		name, value, given := strings.Cut(spec, "=")
		if !given {
			if value, err = c.readValue(name, c.flag("secret")); err != nil {
				return err
			}
		}
		env, err := c.api.Environment(c.ctx, o)
		if err != nil {
			return err
		}
		out, err := c.api.ChangeEnvironment(c.ctx, o, wire.EnvironmentMutation{ExpectedRevision: env.Revision,
			SetVariables: []wire.EnvironmentVariable{{Name: name, Value: value, Secret: c.flag("secret")}}})
		if err != nil {
			return err
		}
		return c.say(out, "Set %s for %s.", name, whose)
	},
}

var envUnset = command{
	usage: "env unset NAME… --session S | --project P",
	about: "remove variables",
	flags: ownerFlags,
	run: func(c *call) error {
		if len(c.args) == 0 {
			return errors.New("missing the variable's name")
		}
		o, whose, err := c.owner()
		if err != nil {
			return err
		}
		env, err := c.api.Environment(c.ctx, o)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, v := range env.Variables {
			have[v.Name] = true
		}
		for _, n := range c.args {
			if !have[n] {
				return fmt.Errorf("%s has no variable %s", whose, n)
			}
		}
		out, err := c.api.ChangeEnvironment(c.ctx, o, wire.EnvironmentMutation{ExpectedRevision: env.Revision, RemoveVariables: c.args})
		if err != nil {
			return err
		}
		return c.say(out, "Removed %s from %s.", strings.Join(c.args, ", "), whose)
	},
}

// readValue reads a variable's value: typed at the terminal (unseen for a
// secret), or the whole of stdin, less its final newline.
func (c *call) readValue(name string, secret bool) (string, error) {
	fd, ok := c.terminal()
	if !ok {
		data, err := io.ReadAll(c.in)
		return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), err
	}
	fmt.Fprintf(c.out, "%s: ", name)
	if secret {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(c.out)
		return string(b), err
	}
	line, err := bufio.NewReader(c.in).ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

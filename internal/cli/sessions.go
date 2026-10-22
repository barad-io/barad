package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

var sessionsList = command{
	usage: "sessions [list] [--project P]",
	about: "your sessions, newest first",
	flags: func(fs *flag.FlagSet, c *call) { strFlag(fs, c, "project", "only this project's") },
	run: func(c *call) error {
		all, err := c.api.Sessions(c.ctx)
		if err != nil {
			return err
		}
		projects, err := c.api.Projects(c.ctx)
		if err != nil {
			return err
		}
		names := map[string]string{wire.DefaultProjectID: wire.DefaultProjectName}
		for _, p := range projects {
			names[p.ProjectID] = p.Name
		}
		only := ""
		if q := c.str("project"); q != "" {
			p, err := c.project(q)
			if err != nil {
				return err
			}
			only = p.ProjectID
		}
		var list []wire.Session
		for _, s := range all {
			if s.ArchivedAt.IsZero() && s.MachineStatus != wire.MachineStatusDeleting && (only == "" || projectOf(s) == only) {
				list = append(list, s)
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return activity(list[i]).After(activity(list[j])) })
		var rows [][]string
		for _, s := range list {
			rows = append(rows, []string{sessionName(s), state(s), names[projectOf(s)], s.Model, ago(activity(s)), s.SessionID})
		}
		return c.table(list, []string{"NAME", "STATE", "PROJECT", "MODEL", "ACTIVE", "ID"}, rows)
	},
}

var sessionsShow = command{
	usage: "sessions show SESSION",
	about: "a session's settings, repositories and variables",
	run: func(c *call) error {
		s, err := c.sessionArg()
		if err != nil {
			return err
		}
		repos, err := c.api.SessionRepositories(c.ctx, s.SessionID)
		if err != nil {
			return err
		}
		env, err := c.api.Environment(c.ctx, api.EnvironmentOwner{Kind: "sessions", ID: s.SessionID})
		if err != nil {
			return err
		}
		if c.asJSON {
			return c.json(map[string]any{"session": s, "repositories": repos, "environment": env.Variables})
		}
		project, err := c.project(projectOf(s))
		if err != nil {
			return err
		}
		var names []string
		for _, r := range repos {
			names = append(names, r.CanonicalPath)
		}
		repo := strings.Join(names, ", ")
		if repo == "" {
			repo = "none"
		}
		effort := string(s.ReasoningEffort)
		if effort == "" {
			effort = "—"
		}
		fmt.Fprintf(c.out, "%s  (%s)\n\n", sessionName(s), s.SessionID)
		for _, kv := range [][2]string{
			{"state", state(s)}, {"model", s.Model}, {"effort", effort}, {"project", project.Name},
			{"repositories", repo}, {"environment", describeEnv(env.Variables)},
			{"created", s.CreatedAt.Local().Format("2 Jan 2006 15:04")}, {"active", ago(activity(s))},
		} {
			fmt.Fprintf(c.out, "  %-13s %s\n", kv[0], kv[1])
		}
		return nil
	},
}

var sessionsNew = command{
	usage: "sessions new [TASK] [--project P] [--provider P [--model M] [--effort E]] [--repo OWNER/NAME[@BRANCH]]… [--no-repos] [--env NAME=VALUE]… [--secret-env NAME=VALUE]…",
	about: "start a session (from its project's agent and repositories unless you name others), and send it TASK",
	flags: func(fs *flag.FlagSet, c *call) {
		strFlag(fs, c, "project", "the project it joins (default: Default)")
		agentFlags(fs, c)
		listFlag(fs, c, "repo", "a repository it clones, at a branch if you name one (repeat for more)")
		boolFlag(fs, c, "no-repos", "a blank workspace, whatever the project clones")
		listFlag(fs, c, "env", "a variable it receives (repeat for more)")
		listFlag(fs, c, "secret-env", "a secret variable it receives")
	},
	run: func(c *call) error {
		project, err := c.project(firstNonEmpty(c.str("project"), wire.DefaultProjectID))
		if err != nil {
			return err
		}
		in := api.NewSession{ProjectID: project.ProjectID}
		agent := project.Agent
		switch {
		case c.str("provider") != "":
			if agent, err = c.agent(c.str("provider"), c.str("model"), c.str("effort")); err != nil {
				return err
			}
		case agent != nil && (c.str("model") != "" || c.str("effort") != ""):
			if agent, err = c.agent(agent.ProviderResourceID, firstNonEmpty(c.str("model"), agent.Model), c.str("effort")); err != nil {
				return err
			}
		case agent == nil:
			return fmt.Errorf("%s has no agent: name the --provider", project.Name)
		}
		in.ProviderResourceID, in.Model, in.ReasoningEffort = agent.ProviderResourceID, agent.Model, agent.ReasoningEffort
		switch {
		case c.flag("no-repos"):
		case len(c.list("repo")) > 0:
			all, err := c.api.Repositories(c.ctx)
			if err != nil {
				return err
			}
			for _, spec := range c.list("repo") {
				name, branch, _ := strings.Cut(spec, "@")
				r, err := c.repository(name, all)
				if err != nil {
					return err
				}
				in.Repositories = append(in.Repositories, api.RepositoryRequest{SourceID: r.SourceID, Repository: r.FullName, RequestedRef: branch})
			}
		default:
			for _, r := range project.Repositories {
				in.Repositories = append(in.Repositories, api.RepositoryRequest{SourceID: r.SourceID, Repository: r.Repository})
			}
		}
		for _, kind := range []string{"env", "secret-env"} {
			for _, spec := range c.list(kind) {
				name, value, ok := strings.Cut(spec, "=")
				if !ok {
					return fmt.Errorf("--%s takes NAME=VALUE, not %q", kind, spec)
				}
				in.Environment = append(in.Environment, wire.EnvironmentVariable{Name: name, Value: value, Secret: kind == "secret-env"})
			}
		}
		s, err := c.api.CreateSession(c.ctx, in)
		if err != nil {
			return err
		}
		if task := strings.Join(c.args, " "); strings.TrimSpace(task) != "" {
			if _, err := c.api.SendMessage(c.ctx, s.SessionID, requestID(), task, nil); err != nil {
				return fmt.Errorf("started %s, but could not send it the task: %w", s.SessionID, err)
			}
		}
		return c.say(s, "Started %s (%s).", sessionName(*s), s.SessionID)
	},
}

var sessionsEdit = command{
	usage: "sessions edit SESSION [--name N] [--model M] [--effort E] [--fast on|off] [--project P]",
	about: "rename a session, change its model, effort or fast mode, or move it to another project",
	flags: func(fs *flag.FlagSet, c *call) {
		strFlag(fs, c, "name", "its new name")
		strFlag(fs, c, "model", "the model its next turn uses")
		strFlag(fs, c, "effort", "the reasoning effort")
		strFlag(fs, c, "fast", "fast mode: on or off (faster replies at more usage or API cost)")
		strFlag(fs, c, "project", "the project it moves to")
	},
	run: func(c *call) error {
		s, err := c.sessionArg()
		if err != nil {
			return err
		}
		st := api.SettingsOf(s)
		changed := false
		if n := c.str("name"); n != "" {
			st.Name, changed = n, true
		}
		if c.str("model") != "" || c.str("effort") != "" {
			agent, err := c.agent(s.ProviderResourceID, firstNonEmpty(c.str("model"), s.Model), c.str("effort"))
			if err != nil {
				return err
			}
			st.Model, st.ReasoningEffort, changed = agent.Model, agent.ReasoningEffort, true
		}
		switch c.str("fast") {
		case "":
		case "on", "off":
			st.FastMode, changed = c.str("fast") == "on", true
		default:
			return errors.New("--fast is on or off")
		}
		if changed {
			out, err := c.api.UpdateSettings(c.ctx, s.SessionID, st)
			if err != nil {
				return err
			}
			s = *out
		}
		if q := c.str("project"); q != "" {
			p, err := c.project(q)
			if err != nil {
				return err
			}
			out, err := c.api.MoveSession(c.ctx, s.SessionID, p.ProjectID)
			if err != nil {
				return err
			}
			s, changed = *out, true
		}
		if !changed {
			return errors.New("nothing to change: name --name, --model, --effort, --fast or --project")
		}
		return c.say(s, "Saved %s.", sessionName(s))
	},
}

var sessionsRepos = command{
	usage: "sessions repos SESSION [--add OWNER/NAME]… [--remove OWNER/NAME]…",
	about: "the repositories a session's Git credentials reach, and giving or taking away access (its files stay)",
	flags: func(fs *flag.FlagSet, c *call) {
		listFlag(fs, c, "add", "give access to a repository")
		listFlag(fs, c, "remove", "take access away")
	},
	run: func(c *call) error {
		s, err := c.sessionArg()
		if err != nil {
			return err
		}
		if len(c.list("add")) > 0 {
			all, err := c.api.Repositories(c.ctx)
			if err != nil {
				return err
			}
			for _, name := range c.list("add") {
				r, err := c.repository(name, all)
				if err != nil {
					return err
				}
				if err := c.api.AddRepository(c.ctx, s.SessionID, r.SourceID, r.FullName); err != nil {
					return err
				}
			}
		}
		if len(c.list("remove")) > 0 {
			have, err := c.api.SessionRepositories(c.ctx, s.SessionID)
			if err != nil {
				return err
			}
			for _, name := range c.list("remove") {
				found := false
				for _, r := range have {
					if strings.EqualFold(r.CanonicalPath, name) {
						if err := c.api.RemoveRepository(c.ctx, s.SessionID, r); err != nil {
							return err
						}
						found = true
					}
				}
				if !found {
					return fmt.Errorf("%s does not reach %s", sessionName(s), name)
				}
			}
		}
		repos, err := c.api.SessionRepositories(c.ctx, s.SessionID)
		if err != nil {
			return err
		}
		var rows [][]string
		for _, r := range repos {
			rows = append(rows, []string{r.CanonicalPath, r.Host})
		}
		return c.table(repos, []string{"REPOSITORY", "HOST"}, rows)
	},
}

// sessionAction is a command that does one thing to a session.
func sessionAction(usage, about, confirm string, do func(c *call, s wire.Session) (string, error)) command {
	return command{usage: usage, about: about, run: func(c *call) error {
		s, err := c.sessionArg()
		if err != nil {
			return err
		}
		if confirm != "" {
			if err := c.confirm(fmt.Sprintf(confirm, sessionName(s))); err != nil {
				return err
			}
		}
		said, err := do(c, s)
		if err != nil {
			return err
		}
		return c.say(s, "%s", said)
	}}
}

var sessionsArchive = sessionAction("sessions archive SESSION [--yes]", "erase a session's workspace and published pages; the conversation stays",
	"Archive %s? Its workspace and published pages are erased; the conversation stays.",
	func(c *call, s wire.Session) (string, error) {
		_, err := c.api.Archive(c.ctx, s.SessionID)
		return "Archived " + sessionName(s) + ".", err
	})

var sessionsDelete = sessionAction("sessions delete SESSION [--yes]", "remove a session, its transcript and its workspace for good",
	"Delete %s? The session, its transcript and its workspace are removed for good.",
	func(c *call, s wire.Session) (string, error) {
		return "Deleting " + sessionName(s) + ".", c.api.DeleteSession(c.ctx, s.SessionID)
	})

var sessionsWake = sessionAction("sessions wake SESSION", "start a session's computer so it is ready for work", "",
	func(c *call, s wire.Session) (string, error) {
		return "Waking " + sessionName(s) + ".", c.api.Wake(c.ctx, s.SessionID)
	})

var sessionsStop = sessionAction("sessions stop SESSION", "stop the agent after its current step", "",
	func(c *call, s wire.Session) (string, error) {
		if s.CurrentTurnID == "" {
			return "", fmt.Errorf("%s is not working", sessionName(s))
		}
		return "Stopping " + sessionName(s) + " after this step.", c.api.Cancel(c.ctx, s.SessionID)
	})

var sessionsShare = sessionAction("sessions share SESSION", "a read-only link to the conversation, for 24 hours", "",
	func(c *call, s wire.Session) (string, error) {
		sh, err := c.api.Share(c.ctx, s.SessionID, "share"+strings.TrimPrefix(requestID(), "msg"))
		if err != nil {
			return "", err
		}
		return sh.URL, nil
	})

var send = command{
	usage: "send SESSION MESSAGE…",
	about: "send a session a message (the agent reads it at its next step if it is working)",
	run: func(c *call) error {
		s, err := c.sessionArg()
		if err != nil {
			return err
		}
		text := strings.TrimSpace(strings.Join(c.args[1:], " "))
		if text == "" {
			return errors.New("missing the message")
		}
		out, err := c.api.SendMessage(c.ctx, s.SessionID, requestID(), text, nil)
		if err != nil {
			return err
		}
		return c.say(out, "Sent to %s.", sessionName(s))
	},
}

func (c *call) sessionArg() (wire.Session, error) {
	q, err := c.arg(0, "the session")
	if err != nil {
		return wire.Session{}, err
	}
	return c.session(q)
}

func projectOf(s wire.Session) string {
	if s.ProjectID == "" {
		return wire.DefaultProjectID
	}
	return s.ProjectID
}

// state is a session's state in a word.
func state(s wire.Session) string {
	switch {
	case s.MachineStatus == wire.MachineStatusBroken:
		return "broken"
	case s.CurrentTurnID != "":
		return "working"
	case s.MachineStatus == wire.MachineStatusHydrating:
		return "waking"
	case s.LatestTerminalTurnStatus == wire.TurnStatusFailed:
		return "failed"
	case s.MachineStatus == wire.MachineStatusDehydrated:
		return "asleep"
	}
	return "idle"
}

func activity(s wire.Session) time.Time {
	t := s.CreatedAt
	for _, u := range []time.Time{s.LatestTerminalTurnAt, s.LatestUserPromptAt} {
		if u.After(t) {
			t = u
		}
	}
	return t
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func requestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "msg_" + hex.EncodeToString(b)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

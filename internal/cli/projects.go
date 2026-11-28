package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

var projectsList = command{
	usage: "projects [list]",
	about: "your projects, with the default agent and repositories their sessions start with",
	run: func(c *call) error {
		projects, err := c.api.Projects(c.ctx)
		if err != nil {
			return err
		}
		var rows [][]string
		var open []wire.Project
		for _, p := range projects {
			if !p.ClosingAt.IsZero() {
				continue
			}
			open = append(open, p)
			rows = append(rows, []string{p.Name, describeAgent(p.Agent), describeRepos(p.Repositories), p.ProjectID})
		}
		return c.table(open, []string{"NAME", "AGENT", "REPOSITORIES", "ID"}, rows)
	},
}

var projectsShow = command{
	usage: "projects show PROJECT",
	about: "a project's settings and the names of its variables",
	run: func(c *call) error {
		q, err := c.arg(0, "the project")
		if err != nil {
			return err
		}
		p, err := c.project(q)
		if err != nil {
			return err
		}
		env, err := c.api.Environment(c.ctx, api.EnvironmentOwner{Kind: "projects", ID: p.ProjectID})
		if err != nil {
			return err
		}
		if c.asJSON {
			return c.json(map[string]any{"project": p, "environment": env.Variables})
		}
		fmt.Fprintf(c.out, "%s  (%s)\n\n", p.Name, p.ProjectID)
		fmt.Fprintf(c.out, "  agent         %s\n", describeAgent(p.Agent))
		fmt.Fprintf(c.out, "  repositories  %s\n", describeRepos(p.Repositories))
		fmt.Fprintf(c.out, "  environment   %s\n", describeEnv(env.Variables))
		return nil
	},
}

// agentFlags are how a project's or a session's v2 agent is named.
func agentFlags(fs *flag.FlagSet, c *call) {
	strFlag(fs, c, "agent", "the harness: claude, codex, grok, or agy")
	strFlag(fs, c, "model", "the model (default: harness default, or custom model ID)")
	strFlag(fs, c, "effort", "the reasoning effort (low, medium, high, max…)")
	strFlag(fs, c, "size", "the machine size: s, m, l, or xl")
}

var projectsNew = command{
	usage: "projects new NAME [--agent claude|codex|grok|agy [--model M] [--effort E] [--size S]] [--repo OWNER/NAME]… [--colour C]",
	about: "make a project; its sessions start with this default harness and clone these repositories",
	flags: func(fs *flag.FlagSet, c *call) {
		agentFlags(fs, c)
		listFlag(fs, c, "repo", "a repository its sessions clone (repeat for more)")
		strFlag(fs, c, "colour", "its colour: "+strings.Join(wire.ProjectColours, ", "))
	},
	run: func(c *call) error {
		name, err := c.arg(0, "the project's name")
		if err != nil {
			return err
		}
		in := api.ProjectSettings{Name: name, Colour: c.str("colour"), Repositories: []wire.ProjectRepository{}}
		if c.str("agent") != "" {
			if in.Agent, err = c.resolveAgent(c.str("agent"), c.str("model"), c.str("effort"), c.str("size")); err != nil {
				return err
			}
		} else if c.str("model") != "" || c.str("effort") != "" || c.str("size") != "" {
			return errors.New("--model, --effort and --size need --agent")
		}
		if in.Repositories, err = c.projectRepos(nil, c.list("repo"), nil); err != nil {
			return err
		}
		p, err := c.api.CreateProject(c.ctx, in)
		if err != nil {
			return err
		}
		return c.say(p, "Made %s (%s).", p.Name, p.ProjectID)
	},
}

var projectsEdit = command{
	usage: "projects edit PROJECT [--name N] [--colour C] [--agent A [--model M] [--effort E] [--size S] | --no-agent] [--add-repo R]… [--remove-repo R]…",
	about: "change a project's name, default agent, or repositories",
	flags: func(fs *flag.FlagSet, c *call) {
		strFlag(fs, c, "name", "its new name")
		agentFlags(fs, c)
		boolFlag(fs, c, "no-agent", "new sessions choose their agent")
		listFlag(fs, c, "add-repo", "a repository its sessions also clone")
		listFlag(fs, c, "remove-repo", "a repository its sessions no longer clone")
		strFlag(fs, c, "colour", "its colour: "+strings.Join(wire.ProjectColours, ", "))
	},
	run: func(c *call) error {
		q, err := c.arg(0, "the project")
		if err != nil {
			return err
		}
		p, err := c.project(q)
		if err != nil {
			return err
		}
		in := api.SettingsOfProject(p)
		if n := c.str("name"); n != "" {
			in.Name = n
		}
		if col := c.str("colour"); col != "" {
			in.Colour = col
		}
		switch {
		case c.flag("no-agent"):
			in.Agent = nil
		case c.str("agent") != "":
			if in.Agent, err = c.resolveAgent(c.str("agent"), c.str("model"), c.str("effort"), c.str("size")); err != nil {
				return err
			}
		case c.str("model") != "" || c.str("effort") != "" || c.str("size") != "":
			if in.Agent == nil {
				return errors.New("the project has no agent yet: name its --agent")
			}
			agentID := firstNonEmpty(in.Agent.Agent, "claude")
			model := firstNonEmpty(c.str("model"), in.Agent.Model)
			size := firstNonEmpty(c.str("size"), in.Agent.Size)
			if in.Agent, err = c.resolveAgent(agentID, model, c.str("effort"), size); err != nil {
				return err
			}
		}
		if in.Repositories, err = c.projectRepos(in.Repositories, c.list("add-repo"), c.list("remove-repo")); err != nil {
			return err
		}
		if p.ProjectID == wire.DefaultProjectID && in.Name != p.Name {
			return errors.New("the Default project keeps its name")
		}
		out, err := c.api.UpdateProject(c.ctx, p.ProjectID, in)
		if err != nil {
			return err
		}
		return c.say(out, "Saved %s.", out.Name)
	},
}

var projectsDelete = command{
	usage: "projects delete PROJECT [--yes]",
	about: "close a project: its sessions move to Default, nothing else is deleted",
	run: func(c *call) error {
		q, err := c.arg(0, "the project")
		if err != nil {
			return err
		}
		p, err := c.project(q)
		if err != nil {
			return err
		}
		if p.ProjectID == wire.DefaultProjectID {
			return errors.New("the Default project cannot be deleted")
		}
		if err := c.confirm(fmt.Sprintf("Delete %s? Its sessions move to Default; no session, transcript or workspace is deleted.", p.Name)); err != nil {
			return err
		}
		if err := c.api.DeleteProject(c.ctx, p.ProjectID); err != nil {
			return err
		}
		return c.say(p, "Deleted %s; its sessions are in Default.", p.Name)
	},
}

func (c *call) projectRepos(have []wire.ProjectRepository, add, remove []string) ([]wire.ProjectRepository, error) {
	out := append([]wire.ProjectRepository{}, have...)
	if len(add) > 0 {
		all, err := c.api.Repositories(c.ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range add {
			r, err := c.repository(name, all)
			if err != nil {
				return nil, err
			}
			dup := false
			for _, h := range out {
				dup = dup || strings.EqualFold(h.Repository, r.FullName)
			}
			if !dup {
				out = append(out, wire.ProjectRepository{SourceID: r.SourceID, Repository: r.FullName})
			}
		}
	}
	for _, name := range remove {
		found := false
		for i := range out {
			if strings.EqualFold(out[i].Repository, name) {
				out, found = append(out[:i], out[i+1:]...), true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("the project does not clone %q", name)
		}
	}
	return out, nil
}

func describeAgent(a *wire.ProjectAgent) string {
	if a == nil {
		return "chosen per session"
	}
	head := a.Agent
	if head == "" {
		head = a.ProviderResourceID
	}
	var parts []string
	if head != "" {
		parts = append(parts, head)
	}
	if a.Model != "" {
		parts = append(parts, a.Model)
	}
	if a.ReasoningEffort != "" {
		parts = append(parts, string(a.ReasoningEffort))
	}
	if a.Size != "" {
		parts = append(parts, "size "+a.Size)
	}
	if len(parts) == 0 {
		return "chosen per session"
	}
	return strings.Join(parts, " / ")
}

func describeRepos(rs []wire.ProjectRepository) string {
	if len(rs) == 0 {
		return "none"
	}
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.Repository
	}
	return strings.Join(names, ", ")
}

func describeEnv(vs []wire.EnvironmentVariable) string {
	if len(vs) == 0 {
		return "none"
	}
	names := make([]string, len(vs))
	for i, v := range vs {
		if v.Secret {
			names[i] = v.Name + " (secret)"
		} else {
			names[i] = v.Name
		}
	}
	return strings.Join(names, ", ")
}

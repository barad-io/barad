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
	about: "your projects, with the agent and repositories their sessions start with",
	run: func(c *call) error {
		projects, err := c.api.Projects(c.ctx)
		if err != nil {
			return err
		}
		names, err := c.providerNames()
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
			rows = append(rows, []string{p.Name, describeAgent(p.Agent, names), describeRepos(p.Repositories), p.ProjectID})
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
		names, err := c.providerNames()
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "%s  (%s)\n\n", p.Name, p.ProjectID)
		fmt.Fprintf(c.out, "  agent         %s\n", describeAgent(p.Agent, names))
		fmt.Fprintf(c.out, "  repositories  %s\n", describeRepos(p.Repositories))
		fmt.Fprintf(c.out, "  environment   %s\n", describeEnv(env.Variables))
		return nil
	},
}

// agentFlags are how a project's or a session's agent is named.
func agentFlags(fs *flag.FlagSet, c *call) {
	strFlag(fs, c, "provider", "the provider connection: its name, ID or type (claude, openai…)")
	strFlag(fs, c, "model", "the model (default: the connection's)")
	strFlag(fs, c, "effort", "the reasoning effort (default: the model's)")
}

var projectsNew = command{
	usage: "projects new NAME [--provider P [--model M] [--effort E]] [--repo OWNER/NAME]…",
	about: "make a project; its sessions start with this agent and clone these repositories",
	flags: func(fs *flag.FlagSet, c *call) {
		agentFlags(fs, c)
		listFlag(fs, c, "repo", "a repository its sessions clone (repeat for more)")
		strFlag(fs, c, "colour", "its colour: coral, amber, lime, teal, sky, indigo, violet or rose")
	},
	run: func(c *call) error {
		name, err := c.arg(0, "the project's name")
		if err != nil {
			return err
		}
		in := api.ProjectSettings{Name: name, Colour: c.str("colour"), Repositories: []wire.ProjectRepository{}}
		if c.str("provider") != "" {
			if in.Agent, err = c.agent(c.str("provider"), c.str("model"), c.str("effort")); err != nil {
				return err
			}
		} else if c.str("model") != "" || c.str("effort") != "" {
			return errors.New("--model and --effort need --provider")
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
	usage: "projects edit PROJECT [--name N] [--provider P [--model M] [--effort E] | --no-agent] [--add-repo R]… [--remove-repo R]…",
	about: "change a project's name, agent or repositories",
	flags: func(fs *flag.FlagSet, c *call) {
		strFlag(fs, c, "name", "its new name")
		agentFlags(fs, c)
		boolFlag(fs, c, "no-agent", "new sessions choose their agent")
		listFlag(fs, c, "add-repo", "a repository its sessions also clone")
		listFlag(fs, c, "remove-repo", "a repository its sessions no longer clone")
		strFlag(fs, c, "colour", "its colour")
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
		case c.str("provider") != "":
			if in.Agent, err = c.agent(c.str("provider"), c.str("model"), c.str("effort")); err != nil {
				return err
			}
		case c.str("model") != "" || c.str("effort") != "":
			if in.Agent == nil {
				return errors.New("the project has no agent yet: name its --provider")
			}
			model := c.str("model")
			if model == "" {
				model = in.Agent.Model
			}
			if in.Agent, err = c.agent(in.Agent.ProviderResourceID, model, c.str("effort")); err != nil {
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

// projectRepos is have with add added and remove removed, each named
// owner/name and checked against what your Git sources reach.
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

// providerNames names each provider connection, by ID.
func (c *call) providerNames() (map[string]string, error) {
	all, err := c.api.Providers(c.ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range all {
		out[p.ResourceID] = p.Name
	}
	return out, nil
}

func describeAgent(a *wire.ProjectAgent, names map[string]string) string {
	if a == nil {
		return "chosen per session"
	}
	s := names[a.ProviderResourceID]
	if s == "" {
		s = a.ProviderResourceID
	}
	s += " · " + a.Model
	if a.ReasoningEffort != "" {
		s += " · " + string(a.ReasoningEffort)
	}
	return s
}

func describeRepos(rs []wire.ProjectRepository) string {
	if len(rs) == 0 {
		return "none"
	}
	var names []string
	for _, r := range rs {
		names = append(names, r.Repository)
	}
	return strings.Join(names, ", ")
}

func describeEnv(vs []wire.EnvironmentVariable) string {
	if len(vs) == 0 {
		return "none"
	}
	var names []string
	for _, v := range vs {
		n := v.Name
		if v.Secret {
			n += " (secret)"
		}
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

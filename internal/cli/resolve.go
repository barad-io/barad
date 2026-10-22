package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Things are named on the command line by ID, or by name: exactly (any
// case), else by a part of the name only one of them has.

func pick[T any](kind, query string, all []T, id func(T) string, name func(T) string) (T, error) {
	var zero T
	q := strings.ToLower(strings.TrimSpace(query))
	for _, t := range all {
		if id(t) == query {
			return t, nil
		}
	}
	var exact, partial []T
	for _, t := range all {
		n := strings.ToLower(name(t))
		switch {
		case n == q:
			exact = append(exact, t)
		case strings.Contains(n, q):
			partial = append(partial, t)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		return zero, noMatch{kind, query}
	case 1:
		return matches[0], nil
	}
	var names []string
	for _, t := range matches {
		names = append(names, fmt.Sprintf("%s (%s)", name(t), id(t)))
	}
	return zero, fmt.Errorf("%q could be any of: %s; use the ID", query, strings.Join(names, ", "))
}

// noMatch is pick finding nothing by the name.
type noMatch struct{ kind, query string }

func (e noMatch) Error() string { return fmt.Sprintf("no %s matches %q", e.kind, e.query) }

func (c *call) project(query string) (wire.Project, error) {
	all, err := c.api.Projects(c.ctx)
	if err != nil {
		return wire.Project{}, err
	}
	var open []wire.Project
	for _, p := range all {
		if p.ClosingAt.IsZero() {
			open = append(open, p)
		}
	}
	if !hasDefault(open) {
		open = append([]wire.Project{{ProjectID: wire.DefaultProjectID, Name: wire.DefaultProjectName}}, open...)
	}
	return pick("project", query, open, func(p wire.Project) string { return p.ProjectID }, func(p wire.Project) string { return p.Name })
}

func hasDefault(ps []wire.Project) bool {
	for _, p := range ps {
		if p.ProjectID == wire.DefaultProjectID {
			return true
		}
	}
	return false
}

// session is a session by ID or name: a live one, or failing that, an
// archived one.
func (c *call) session(query string) (wire.Session, error) {
	all, err := c.api.Sessions(c.ctx)
	if err != nil {
		return wire.Session{}, err
	}
	var live, archived []wire.Session
	for _, s := range all {
		switch {
		case s.MachineStatus == wire.MachineStatusDeleting:
		case s.ArchivedAt.IsZero():
			live = append(live, s)
		default:
			archived = append(archived, s)
		}
	}
	s, err := pick("session", query, live, func(s wire.Session) string { return s.SessionID }, sessionName)
	if errors.As(err, &noMatch{}) {
		return pick("session", query, archived, func(s wire.Session) string { return s.SessionID }, sessionName)
	}
	return s, err
}

// live is a session that is not archived: one that can still work.
func (c *call) live(query string) (wire.Session, error) {
	s, err := c.session(query)
	if err == nil && !s.ArchivedAt.IsZero() {
		return s, fmt.Errorf("%s is archived: its workspace is gone, and it takes no messages", sessionName(s))
	}
	return s, err
}

// provider is a connection, by ID, name or provider type ("claude").
func (c *call) provider(query string) (wire.ProviderView, error) {
	all, err := c.api.Providers(c.ctx)
	if err != nil {
		return wire.ProviderView{}, err
	}
	var byType []wire.ProviderView
	for _, p := range all {
		if strings.EqualFold(p.ProviderType, query) {
			byType = append(byType, p)
		}
	}
	if len(byType) == 1 {
		return byType[0], nil
	}
	return pick("provider connection", query, all, func(p wire.ProviderView) string { return p.ResourceID }, func(p wire.ProviderView) string { return p.Name })
}

// repository is one your Git sources reach, by owner/name.
func (c *call) repository(name string, all []api.Repository) (api.Repository, error) {
	for _, r := range all {
		if strings.EqualFold(r.FullName, name) {
			return r, nil
		}
	}
	return api.Repository{}, fmt.Errorf("none of your Git sources reaches %q", name)
}

func sessionName(s wire.Session) string {
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	return "Untitled session"
}

// agent is the agent a provider connection, model and effort describe: what
// a project starts its sessions with. The model defaults to the
// connection's, and the effort to the model's.
func (c *call) agent(providerQuery, model, effort string) (*wire.ProjectAgent, error) {
	p, err := c.provider(providerQuery)
	if err != nil {
		return nil, err
	}
	models, err := c.api.Models(c.ctx, p.ResourceID)
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = models.DefaultModel
	}
	for _, m := range models.Models {
		if m.Model != model {
			continue
		}
		e := wire.ReasoningEffort(effort)
		switch {
		case !m.SupportsReasoningEffort && effort != "":
			return nil, fmt.Errorf("%s takes no reasoning effort", model)
		case !m.SupportsReasoningEffort:
			e = ""
		case effort == "":
			e = m.DefaultReasoningEffort
		default:
			ok := false
			for _, x := range m.ReasoningEfforts {
				ok = ok || x == e
			}
			if !ok {
				return nil, fmt.Errorf("%s takes reasoning effort %s, not %q", model, joinEfforts(m.ReasoningEfforts), effort)
			}
		}
		return &wire.ProjectAgent{ProviderResourceID: p.ResourceID, Model: model, ReasoningEffort: e}, nil
	}
	var names []string
	for _, m := range models.Models {
		names = append(names, m.Model)
	}
	return nil, fmt.Errorf("%s has no model %q; it has %s", p.Name, model, strings.Join(names, ", "))
}

func joinEfforts(es []wire.ReasoningEffort) string {
	var s []string
	for _, e := range es {
		s = append(s, string(e))
	}
	return strings.Join(s, ", ")
}

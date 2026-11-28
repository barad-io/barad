package cli

import (
	"errors"
	"fmt"
	"slices"
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

// session resolves a session by ID or name (live first, then archived).
func (c *call) session(query string) (wire.Session, error) {
	all, err := c.api.Sessions(c.ctx)
	if err != nil {
		return wire.Session{}, err
	}
	var live, archived []wire.Session
	for _, s := range all {
		switch {
		case s.MachineStatus == wire.MachineStatusDeleting:
		case s.ArchivedAt.IsZero() && s.MachineStatus != wire.MachineStatusArchived:
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

// live resolves an active (non-archived) session.
func (c *call) live(query string) (wire.Session, error) {
	s, err := c.session(query)
	if err == nil && (!s.ArchivedAt.IsZero() || s.MachineStatus == wire.MachineStatusArchived) {
		return s, fmt.Errorf("%s is archived: its workspace is gone, and it takes no messages", sessionName(s))
	}
	return s, err
}

// repository matches a GitHub repository reachable through the account's Git sources.
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

// agentSpec finds a v2 harness driver in GET /agents by id or label.
func (c *call) agentSpec(query string) (wire.AgentSpec, *wire.AgentsResponse, error) {
	cat, err := c.api.Agents(c.ctx)
	if err != nil {
		return wire.AgentSpec{}, nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		q = "claude"
	}
	// Accept common aliases.
	switch q {
	case "openai", "chatgpt":
		q = "codex"
	case "xai":
		q = "grok"
	case "antigravity":
		q = "agy"
	}
	for _, a := range cat.Agents {
		if strings.EqualFold(a.ID, q) || strings.EqualFold(a.Label, q) {
			return a, cat, nil
		}
	}
	var ids []string
	for _, a := range cat.Agents {
		ids = append(ids, a.ID)
	}
	return wire.AgentSpec{}, cat, fmt.Errorf("unknown agent %q; choose one of %s", query, strings.Join(ids, ", "))
}

// resolveAgent validates a v2 harness, model (including custom model IDs),
// reasoning effort, and machine size against GET /agents.
func (c *call) resolveAgent(agentQuery, model, effort, size string) (*wire.ProjectAgent, error) {
	spec, cat, err := c.agentSpec(agentQuery)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) == "" {
		model = spec.DefaultModel
		if model == "" {
			model = "default"
		}
	}
	// Find matching model spec, or inherit default model's effort capabilities for custom model IDs.
	var ms wire.AgentModelSpec
	matched := false
	for _, m := range spec.Models {
		if m.ID == model {
			ms, matched = m, true
			break
		}
	}
	if !matched {
		ms = wire.AgentModelSpec{
			ID:                      model,
			Label:                   model,
			SupportsReasoningEffort: true,
			Efforts:                 []wire.ReasoningEffort{"low", "medium", "high"},
			DefaultEffort:           "high",
		}
		for _, m := range spec.Models {
			if m.ID == spec.DefaultModel || m.ID == "default" {
				ms.SupportsReasoningEffort = m.SupportsReasoningEffort
				ms.Efforts = m.Efforts
				ms.DefaultEffort = m.DefaultEffort
				break
			}
		}
	}
	e := wire.ReasoningEffort(strings.ToLower(strings.TrimSpace(effort)))
	switch {
	case !ms.SupportsReasoningEffort && e != "":
		return nil, fmt.Errorf("%s does not support reasoning effort", model)
	case !ms.SupportsReasoningEffort:
		e = ""
	case e == "":
		e = ms.DefaultEffort
	case len(ms.Efforts) > 0 && !slices.Contains(ms.Efforts, e):
		return nil, fmt.Errorf("%s takes reasoning effort %s, not %q", model, joinEfforts(ms.Efforts), effort)
	}
	sz := strings.ToLower(strings.TrimSpace(size))
	if sz != "" && len(cat.Sizes) > 0 {
		ok := false
		var valid []string
		for _, s := range cat.Sizes {
			valid = append(valid, s.ID)
			if s.ID == sz {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("size must be one of %s, not %q", strings.Join(valid, ", "), size)
		}
	}
	return &wire.ProjectAgent{
		Agent:           spec.ID,
		Model:           model,
		ReasoningEffort: e,
		Size:            sz,
	}, nil
}

func joinEfforts(es []wire.ReasoningEffort) string {
	var s []string
	for _, e := range es {
		s = append(s, string(e))
	}
	return strings.Join(s, ", ")
}

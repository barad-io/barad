package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/tui"
	"github.com/barad-io/barad/wire"
)

// Provider connections: the model accounts sessions run on. A subscription
// signs in at its provider's page; an API connection takes a key.

func providerTypes() string {
	var s []string
	for _, p := range api.ProviderTypes {
		s = append(s, p.Type)
	}
	return strings.Join(s, ", ")
}

var providersList = command{
	usage: "providers [list]",
	about: "your provider connections",
	run: func(c *call) error {
		all, err := c.api.Providers(c.ctx)
		if err != nil {
			return err
		}
		var rows [][]string
		for _, p := range all {
			state := "ready"
			if !p.Ready {
				state = "signed out"
			}
			rows = append(rows, []string{p.Name, api.ProviderTypeName(p.ProviderType), state, p.ResourceID})
		}
		return c.table(all, []string{"NAME", "TYPE", "STATE", "ID"}, rows)
	},
}

var providersShow = command{
	usage: "providers show PROVIDER",
	about: "a connection, and the sessions that use it",
	run: func(c *call) error {
		p, err := c.providerArg()
		if err != nil {
			return err
		}
		users, err := c.api.ConnectionUsers(c.ctx, "providers", p.ResourceID)
		if err != nil {
			return err
		}
		if c.asJSON {
			return c.json(map[string]any{"provider": p, "sessions": users})
		}
		state := "ready"
		if !p.Ready {
			state = "signed out: barad providers login " + p.Name
		}
		fmt.Fprintf(c.out, "%s  (%s)\n\n  type     %s\n  state    %s\n  used by  %s\n", p.Name, p.ResourceID, api.ProviderTypeName(p.ProviderType), state, count(len(users), "session"))
		return c.listUsers(users)
	},
}

// listUsers names the sessions that use a connection.
func (c *call) listUsers(users []wire.ResourceDependency) error {
	if len(users) == 0 {
		return nil
	}
	all, err := c.api.Sessions(c.ctx)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, s := range all {
		names[s.SessionID] = sessionName(s)
	}
	fmt.Fprintln(c.out)
	for _, u := range users {
		fmt.Fprintf(c.out, "  %s  (%s)\n", firstNonEmpty(names[u.SessionID], "a deleted session"), u.SessionID)
	}
	return nil
}

var providersAdd = command{
	usage: "providers add TYPE [--name NAME]",
	about: "connect a provider: TYPE is chatgpt, openai_api, grok, claude or xai_api. A subscription signs in in your browser; an API type asks for its key (or reads it from stdin)",
	flags: func(fs *flag.FlagSet, c *call) { strFlag(fs, c, "name", "what to call it (default: the type's name)") },
	run: func(c *call) error {
		t, err := c.arg(0, "the provider type ("+providerTypes()+")")
		if err != nil {
			return err
		}
		if api.ProviderTypeName(t) == t {
			return fmt.Errorf("no provider type %q; it is one of %s", t, providerTypes())
		}
		name := firstNonEmpty(c.str("name"), api.ProviderTypeName(t))
		if api.UsesAPIKey(t) {
			key, err := c.readValue(api.ProviderTypeName(t)+" API key", true)
			if err != nil {
				return err
			}
			if strings.TrimSpace(key) == "" {
				return errors.New("no API key given")
			}
			p, err := c.api.AddAPIKeyProvider(c.ctx, t, name, strings.TrimSpace(key))
			if err != nil {
				return err
			}
			return c.say(p, "Connected %s.", p.Name)
		}
		return c.signIn(t, name, "")
	},
}

var providersLogin = command{
	usage: "providers login PROVIDER",
	about: "sign a connection in again (a subscription), or replace its API key",
	run: func(c *call) error {
		p, err := c.providerArg()
		if err != nil {
			return err
		}
		if api.UsesAPIKey(p.ProviderType) {
			key, err := c.readValue("New API key for "+p.Name, true)
			if err != nil {
				return err
			}
			if strings.TrimSpace(key) == "" {
				return errors.New("no API key given")
			}
			if err := c.api.PutAPIKey(c.ctx, p.ResourceID, strings.TrimSpace(key)); err != nil {
				return err
			}
			return c.say(p, "Replaced %s's API key.", p.Name)
		}
		return c.signIn(p.ProviderType, p.Name, p.ResourceID)
	},
}

// signIn runs a subscription's sign-in: the provider's page opens in your
// browser; ChatGPT and Grok take the code shown here, and Claude's page
// shows a code to paste back here. It waits until the connection is ready.
func (c *call) signIn(providerType, name, resourceID string) error {
	flow, err := c.api.AuthorizeProvider(c.ctx, providerType, name, resourceID)
	if err != nil {
		return err
	}
	claude := providerType == "claude"
	if !claude {
		page := firstNonEmpty(flow.VerificationURLComplete, flow.VerificationURL)
		fmt.Fprintf(c.out, "Sign in to %s in your browser and enter this code:\n\n    %s\n\n", api.ProviderTypeName(providerType), flow.UserCode)
		c.browse(page)
	}
	deadline := time.Now().Add(15 * time.Minute)
	if !flow.ExpiresAt.IsZero() {
		deadline = flow.ExpiresAt
	}
	opened := false
	for time.Now().Before(deadline) {
		select {
		case <-c.ctx.Done():
			if claude {
				_ = c.api.CancelProvider(c.ctx, flow)
			}
			return c.ctx.Err()
		case <-time.After(pollEvery):
		}
		poll, err := c.api.PollProvider(c.ctx, flow, providerType)
		switch {
		case err != nil:
			return err
		case poll.Connected:
			return c.say(map[string]string{"resource_id": flow.ResourceID, "name": name}, "Connected %s.", name)
		case poll.Failed:
			return errors.New("the sign-in failed or expired; start again")
		case claude && poll.VerificationURL != "" && !opened:
			opened = true
			fmt.Fprintf(c.out, "Sign in to Claude in your browser. It shows a code when you are done.\n\n")
			c.browse(poll.VerificationURL)
			code, err := c.readLine("Paste the code here: ")
			if err != nil {
				return err
			}
			if err := c.api.SubmitProviderCode(c.ctx, flow, strings.TrimSpace(code)); err != nil {
				return err
			}
			fmt.Fprintln(c.out, "Finishing the sign-in…")
		}
	}
	return errors.New("the sign-in expired; start again")
}

// browse opens a page in the browser, and says where it is in case none opened.
func (c *call) browse(page string) {
	if tui.OpenURL(page) == nil {
		fmt.Fprintf(c.out, "If no browser opened, go to %s\n", page)
	} else {
		fmt.Fprintf(c.out, "Open %s\n", page)
	}
	fmt.Fprintln(c.out, "Waiting for you…")
}

// readLine asks a question and reads the answer.
func (c *call) readLine(question string) (string, error) {
	fmt.Fprint(c.out, question)
	line, err := bufio.NewReader(c.in).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

var providersRename = command{
	usage: "providers rename PROVIDER NAME",
	about: "rename a connection",
	run: func(c *call) error {
		p, err := c.providerArg()
		if err != nil {
			return err
		}
		name, err := c.arg(1, "the new name")
		if err != nil {
			return err
		}
		if err := c.api.RenameConnection(c.ctx, "providers", p.ResourceID, name); err != nil {
			return err
		}
		return c.say(p, "Renamed %s to %s.", p.Name, name)
	},
}

var providersLogout = command{
	usage: "providers logout PROVIDER [--yes]",
	about: "remove a connection's sign-in or key; its sessions stop until it signs in again",
	run: func(c *call) error {
		p, err := c.providerArg()
		if err != nil {
			return err
		}
		if err := c.confirm(fmt.Sprintf("Sign %s out? Sessions using it stop until it signs in again.", p.Name)); err != nil {
			return err
		}
		if err := c.api.RemoveCredential(c.ctx, p.ResourceID); err != nil {
			return err
		}
		return c.say(p, "Signed %s out.", p.Name)
	},
}

var providersRemove = command{
	usage: "providers remove PROVIDER [--yes]",
	about: "remove a connection for good",
	run: func(c *call) error {
		p, err := c.providerArg()
		if err != nil {
			return err
		}
		users, err := c.api.ConnectionUsers(c.ctx, "providers", p.ResourceID)
		if err != nil {
			return err
		}
		question := fmt.Sprintf("Remove %s?", p.Name)
		if len(users) > 0 {
			question = fmt.Sprintf("Remove %s? %s using it stop working.", p.Name, count(len(users), "session"))
		}
		if err := c.confirm(question); err != nil {
			return err
		}
		if err := c.api.RemoveConnection(c.ctx, "providers", p.ResourceID); err != nil {
			return err
		}
		return c.say(p, "Removed %s.", p.Name)
	},
}

func (c *call) providerArg() (wire.ProviderView, error) {
	q, err := c.arg(0, "the provider connection")
	if err != nil {
		return wire.ProviderView{}, err
	}
	return c.provider(q)
}

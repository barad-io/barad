package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/wire"
)

// Source control: the GitHub account Barad signs in as, the accounts and
// organizations it is installed on (Git sources), and who sessions' commits
// are by.

var gitStatus = command{
	usage: "git [status]",
	about: "the GitHub account you signed in as, your Git sources, and who commits are by",
	run: func(c *call) error {
		gh, err := c.api.GitHub(c.ctx)
		if err != nil {
			return err
		}
		sources, err := c.api.GitSources(c.ctx)
		if err != nil {
			return err
		}
		id, err := c.api.GitSettings(c.ctx)
		if err != nil {
			return err
		}
		if c.asJSON {
			return c.json(map[string]any{"github": gh, "sources": sources, "identity": id})
		}
		switch {
		case gh.SignedIn:
			fmt.Fprintf(c.out, "GitHub     signed in as %s\n", gh.Login)
		case gh.Login != "":
			fmt.Fprintf(c.out, "GitHub     %s must sign in again: barad git login\n", gh.Login)
		default:
			fmt.Fprintln(c.out, "GitHub     signed out: barad git login")
		}
		fmt.Fprintf(c.out, "Commits    %s\n\nSOURCES\n", describeIdentity(*id))
		for _, s := range sources {
			fmt.Fprintf(c.out, "  %-24s %s\n", s.Name, s.ResourceID)
		}
		if len(sources) == 0 {
			fmt.Fprintln(c.out, "  none yet: barad git connect")
		}
		for _, in := range gh.Installations {
			if in.SourceID == "" {
				fmt.Fprintf(c.out, "  %-24s not connected: barad git connect %s\n", in.AccountLogin, in.AccountLogin)
			}
		}
		return nil
	},
}

func describeIdentity(s wire.GitSettings) string {
	author, committer := api.Identity(s)
	return "authored by " + author + ", committed by " + committer
}

var gitLogin = command{
	usage: "git login",
	about: "sign in to GitHub in your browser, so Barad can reach your repositories",
	run: func(c *call) error {
		link, err := c.api.GitHubSignIn(c.ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.out, "Sign in to GitHub in your browser.")
		c.browse(link.URL)
		return c.waitGitHub(link.ExpiresAt, func(gh *wire.GitHubAccount) bool { return gh.SignedIn }, func(gh *wire.GitHubAccount) string {
			return "Signed in to GitHub as " + gh.Login + "."
		})
	},
}

// waitGitHub waits until GitHub, in the browser, has done what was asked.
func (c *call) waitGitHub(until time.Time, done func(*wire.GitHubAccount) bool, say func(*wire.GitHubAccount) string) error {
	if until.IsZero() {
		until = time.Now().Add(10 * time.Minute)
	}
	for time.Now().Before(until) {
		gh, err := c.api.GitHub(c.ctx)
		if err != nil {
			return err
		}
		if done(gh) {
			return c.say(gh, "%s", say(gh))
		}
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(pollEvery):
		}
	}
	return errors.New("GitHub did not finish in time; start again")
}

var gitLogout = command{
	usage: "git logout [--yes]",
	about: "forget the GitHub sign-in; sources stay, but their repositories are out of reach until you sign in again",
	run: func(c *call) error {
		if err := c.confirm("Sign out of GitHub? Sessions cannot reach your repositories until you sign in again."); err != nil {
			return err
		}
		if err := c.api.GitHubSignOut(c.ctx); err != nil {
			return err
		}
		return c.say(map[string]bool{"signed_in": false}, "Signed out of GitHub.")
	},
}

var gitConnect = command{
	usage: "git connect [ACCOUNT]",
	about: "connect an account or organization Barad is installed on; with none named, or one it is not installed on, install it there in your browser",
	run: func(c *call) error {
		gh, err := c.api.GitHub(c.ctx)
		if err != nil {
			return err
		}
		if !gh.SignedIn {
			return errors.New("sign in to GitHub first: barad git login")
		}
		if len(c.args) > 0 {
			for _, in := range gh.Installations {
				if !strings.EqualFold(in.AccountLogin, c.args[0]) {
					continue
				}
				if in.SourceID != "" {
					return fmt.Errorf("%s is connected already", in.AccountLogin)
				}
				src, err := c.api.ConnectInstallation(c.ctx, in.InstallationID)
				if err != nil {
					return err
				}
				return c.say(src, "Connected %s.", src.Name)
			}
		}
		before := map[int64]bool{}
		for _, in := range gh.Installations {
			before[in.InstallationID] = true
		}
		link, err := c.api.GitHubInstall(c.ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.out, "Install Barad on an account or organization in your browser. (Only an organization's owner can; members can ask for it.)")
		c.browse(link.URL)
		return c.waitGitHub(link.ExpiresAt, func(gh *wire.GitHubAccount) bool {
			for _, in := range gh.Installations {
				if !before[in.InstallationID] && in.SourceID != "" {
					return true
				}
			}
			return false
		}, func(gh *wire.GitHubAccount) string {
			for _, in := range gh.Installations {
				if !before[in.InstallationID] {
					return "Connected " + in.AccountLogin + "."
				}
			}
			return "Connected."
		})
	},
}

var gitRepos = command{
	usage: "git repos [SOURCE]",
	about: "the repositories your Git sources reach (or one source's)",
	run: func(c *call) error {
		sources, err := c.api.GitSources(c.ctx)
		if err != nil {
			return err
		}
		if len(c.args) > 0 {
			s, err := c.source(c.args[0], sources)
			if err != nil {
				return err
			}
			sources = []wire.AccountResource{s}
		}
		var all []wire.GitHubRepository
		var rows [][]string
		for _, s := range sources {
			repos, err := c.api.SourceRepositories(c.ctx, s.ResourceID)
			if err != nil {
				return err
			}
			for _, r := range repos {
				all = append(all, r)
				rows = append(rows, []string{r.FullName, r.DefaultBranch, s.Name})
			}
		}
		return c.table(all, []string{"REPOSITORY", "DEFAULT BRANCH", "SOURCE"}, rows)
	},
}

var gitRename = command{
	usage: "git rename SOURCE NAME",
	about: "rename a Git source",
	run: func(c *call) error {
		s, err := c.sourceArg()
		if err != nil {
			return err
		}
		name, err := c.arg(1, "the new name")
		if err != nil {
			return err
		}
		if err := c.api.RenameConnection(c.ctx, "git-sources", s.ResourceID, name); err != nil {
			return err
		}
		return c.say(s, "Renamed %s to %s.", s.Name, name)
	},
}

var gitRemove = command{
	usage: "git remove SOURCE [--yes]",
	about: "disconnect a Git source; sessions lose access to its repositories",
	run: func(c *call) error {
		s, err := c.sourceArg()
		if err != nil {
			return err
		}
		users, err := c.api.ConnectionUsers(c.ctx, "git-sources", s.ResourceID)
		if err != nil {
			return err
		}
		question := fmt.Sprintf("Disconnect %s?", s.Name)
		if len(users) > 0 {
			question = fmt.Sprintf("Disconnect %s? %s lose access to its repositories.", s.Name, count(len(users), "session"))
		}
		if err := c.confirm(question); err != nil {
			return err
		}
		if err := c.api.RemoveConnection(c.ctx, "git-sources", s.ResourceID); err != nil {
			return err
		}
		return c.say(s, "Disconnected %s.", s.Name)
	},
}

var gitIdentity = command{
	usage: "git identity [--name N] [--email E] [--separate on|off] [--barad-author on|off]",
	about: "who sessions' commits are by: your name and email, and whether Barad commits as itself",
	flags: func(fs *flag.FlagSet, c *call) {
		strFlag(fs, c, "name", "the name commits carry")
		strFlag(fs, c, "email", "the email commits carry")
		strFlag(fs, c, "separate", "on: the committer differs from the author (you and Barad)")
		strFlag(fs, c, "barad-author", "on: Barad is the author")
	},
	run: func(c *call) error {
		id, err := c.api.GitSettings(c.ctx)
		if err != nil {
			return err
		}
		next, changed := *id, false
		if v := c.str("name"); v != "" {
			next.UserName, changed = v, true
		}
		if v := c.str("email"); v != "" {
			next.HumanEmail, changed = v, true
		}
		for _, f := range []struct {
			flag string
			to   *bool
		}{{"separate", &next.SeparateIdentities}, {"barad-author", &next.BaradIsAuthor}} {
			switch c.str(f.flag) {
			case "":
			case "on", "off":
				*f.to, changed = c.str(f.flag) == "on", true
			default:
				return fmt.Errorf("--%s is on or off", f.flag)
			}
		}
		if changed {
			if id, err = c.api.PutGitSettings(c.ctx, next); err != nil {
				return err
			}
		}
		return c.say(id, "Commits are %s.", describeIdentity(*id))
	},
}

func (c *call) source(query string, all []wire.AccountResource) (wire.AccountResource, error) {
	return pick("Git source", query, all, func(s wire.AccountResource) string { return s.ResourceID }, func(s wire.AccountResource) string { return s.Name })
}

func (c *call) sourceArg() (wire.AccountResource, error) {
	q, err := c.arg(0, "the Git source")
	if err != nil {
		return wire.AccountResource{}, err
	}
	all, err := c.api.GitSources(c.ctx)
	if err != nil {
		return wire.AccountResource{}, err
	}
	return c.source(q, all)
}

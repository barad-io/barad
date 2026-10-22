// Package cli is barad's commands that work without the app: everything the
// app and the web app do, as `barad NOUN VERB [ARGS] [FLAGS]`, for scripts
// and for a quick change. Lists print a table, or JSON with --json.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/barad-io/barad/internal/api"
)

// pollEvery is how often a command that waits asks again: for a reply, a
// sign-in, a payment. Tests shorten it.
var pollEvery = 2 * time.Second

// A command runs one verb of a noun. Its flags are declared on fs; run gets
// the arguments that are not flags.
type command struct {
	usage string // "projects new NAME [--provider P] …"
	about string
	flags func(fs *flag.FlagSet, c *call)
	run   func(c *call) error
}

// call is one command's run: its client, arguments, flags and output.
type call struct {
	ctx    context.Context
	api    *api.Client
	args   []string
	asJSON bool
	yes    bool
	out    io.Writer
	in     io.Reader
	values map[string]any // flag values, by name
}

// verbs is every command, in the order help lists them. A noun's first verb
// is what the noun alone does (`barad projects` lists them).
var verbs = []struct {
	noun, verb string
	cmd        *command
}{
	{"sessions", "list", &sessionsList}, {"sessions", "show", &sessionsShow}, {"sessions", "log", &sessionsLog},
	{"sessions", "read", &sessionsRead}, {"sessions", "unread", &sessionsUnread}, {"sessions", "proposal", &sessionsProposal},
	{"sessions", "new", &sessionsNew},
	{"sessions", "edit", &sessionsEdit}, {"sessions", "repos", &sessionsRepos}, {"sessions", "wake", &sessionsWake},
	{"sessions", "stop", &sessionsStop}, {"sessions", "share", &sessionsShare}, {"sessions", "archive", &sessionsArchive},
	{"sessions", "delete", &sessionsDelete},
	{"send", "", &send},
	{"shell", "", &shellCommand},
	{"files", "list", &filesList}, {"files", "cat", &filesCat}, {"files", "get", &filesGet}, {"files", "put", &filesPut},
	{"projects", "list", &projectsList}, {"projects", "show", &projectsShow}, {"projects", "new", &projectsNew},
	{"projects", "edit", &projectsEdit}, {"projects", "delete", &projectsDelete},
	{"env", "list", &envList}, {"env", "set", &envSet}, {"env", "unset", &envUnset},
	{"providers", "list", &providersList}, {"providers", "show", &providersShow}, {"providers", "add", &providersAdd},
	{"providers", "login", &providersLogin}, {"providers", "rename", &providersRename}, {"providers", "logout", &providersLogout},
	{"providers", "remove", &providersRemove},
	{"git", "status", &gitStatus}, {"git", "login", &gitLogin}, {"git", "logout", &gitLogout}, {"git", "connect", &gitConnect},
	{"git", "repos", &gitRepos}, {"git", "rename", &gitRename}, {"git", "remove", &gitRemove}, {"git", "identity", &gitIdentity},
	{"usage", "summary", &usageSummary}, {"usage", "sessions", &usageSessions}, {"usage", "providers", &usageProviders},
	{"usage", "models", &usageModels}, {"usage", "daily", &usageDaily},
	{"billing", "status", &billingStatus}, {"billing", "history", &billingHistory}, {"billing", "buy", &billingBuy},
	{"billing", "terms", &billingTerms},
	{"account", "show", &accountShow}, {"account", "password", &accountPassword},
	{"config", "", &configCommand},
}

// lookup is noun's verb, or with verb "", the noun's first.
func lookup(noun, verb string) *command {
	for _, v := range verbs {
		if v.noun == noun && (v.verb == verb || verb == "") {
			return v.cmd
		}
	}
	return nil
}

// Known reports whether name is a command line noun.
func Known(name string) bool { return lookup(name, "") != nil }

// Run runs `barad NOUN [VERB] ARGS…`, reading from in (a value to set, an
// answer to a question) and printing to out.
func Run(ctx context.Context, client *api.Client, args []string, in io.Reader, out io.Writer) error {
	noun, rest := args[0], args[1:]
	if len(rest) > 0 && (rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help") {
		return Help(out, noun)
	}
	cmd := lookup(noun, "")
	if len(rest) > 0 {
		if named := lookup(noun, rest[0]); named != nil && rest[0] != "" {
			cmd, rest = named, rest[1:]
		}
	}
	c := &call{ctx: ctx, api: client, out: out, in: in, values: map[string]any{}}
	fs := flag.NewFlagSet("barad "+noun, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&c.asJSON, "json", false, "print JSON")
	fs.BoolVar(&c.yes, "yes", false, "do not ask before a change that cannot be undone")
	if cmd.flags != nil {
		cmd.flags(fs, c)
	}
	positional, err := parse(fs, rest)
	if err != nil {
		return fmt.Errorf("%v\nusage: barad %s", err, cmd.usage)
	}
	c.args = positional
	return cmd.run(c)
}

// parse parses flags wherever they are among the arguments, as people type
// them: `barad projects new Web --repo acme/web`.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// Help prints the commands of one noun, or of them all, each under its noun.
func Help(out io.Writer, noun string) error {
	heading := ""
	for _, v := range verbs {
		if noun != "" && v.noun != noun {
			continue
		}
		if v.noun != heading && v.verb != "" {
			heading = v.noun
			fmt.Fprintf(out, "\n%s\n", strings.ToUpper(heading))
		}
		fmt.Fprintf(out, "  barad %s\n      %s\n", v.cmd.usage, v.cmd.about)
	}
	fmt.Fprintln(out, "\nNamed things take an ID or a name (any case, or a part only one has). Every command takes\n--json for machine-readable output, and --yes to skip a confirmation.")
	return nil
}

// table prints rows under a header, or the value as JSON with --json.
func (c *call) table(value any, header []string, rows [][]string) error {
	if c.asJSON {
		return c.json(value)
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.out, "Nothing yet.")
		return nil
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return w.Flush()
}

func (c *call) json(value any) error {
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

// say prints what a change did, unless the output is JSON (then the changed
// thing is printed).
func (c *call) say(value any, format string, args ...any) error {
	if c.asJSON {
		return c.json(value)
	}
	_, err := fmt.Fprintf(c.out, format+"\n", args...)
	return err
}

// arg is the i-th positional argument, named for the error when it is missing.
func (c *call) arg(i int, name string) (string, error) {
	if i >= len(c.args) || strings.TrimSpace(c.args[i]) == "" {
		return "", fmt.Errorf("missing %s", name)
	}
	return c.args[i], nil
}

// confirm asks before a change that cannot be undone. --yes answers for you;
// without a terminal to ask on, it is required.
func (c *call) confirm(question string) error {
	if c.yes {
		return nil
	}
	if _, ok := c.terminal(); !ok {
		return errors.New(question + " Run again with --yes to confirm.")
	}
	fmt.Fprint(c.out, question+" [y/N] ")
	line, _ := bufio.NewReader(c.in).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return errors.New("nothing changed")
	}
	return nil
}

// stringsFlag is a repeatable string flag (--repo a --repo b).
type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

func (c *call) str(name string) string    { return *c.values[name].(*string) }
func (c *call) list(name string) []string { return *c.values[name].(*stringsFlag) }
func (c *call) flag(name string) bool     { return *c.values[name].(*bool) }

func strFlag(fs *flag.FlagSet, c *call, name, usage string) {
	c.values[name] = fs.String(name, "", usage)
}

func listFlag(fs *flag.FlagSet, c *call, name, usage string) {
	v := &stringsFlag{}
	fs.Var(v, name, usage)
	c.values[name] = v
}

func boolFlag(fs *flag.FlagSet, c *call, name, usage string) {
	c.values[name] = fs.Bool(name, false, usage)
}

// piped reports whether input comes from a pipe or a file, not a terminal
// (or nothing at all).
func (c *call) piped() bool {
	f, ok := c.in.(*os.File)
	if !ok {
		return c.in != nil
	}
	info, err := f.Stat()
	return err == nil && (info.Mode()&os.ModeNamedPipe != 0 || info.Mode().IsRegular())
}

// terminal is the input's terminal, if it is one.
func (c *call) terminal() (int, bool) {
	f, ok := c.in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0, false
	}
	return int(f.Fd()), true
}

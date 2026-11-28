// barad is the Barad coding-agent CLI and TUI: persistent cloud sessions,
// native harnesses (claude, codex, grok, agy), and scriptable subcommands.
//
//	barad                 open fuzzy session picker (or resume with -r)
//	barad <session>       attach straight into a session's TUI
//	barad login           sign in: approve this computer in the web app
//	barad logout          sign out on this computer
//	barad version         the release this is
//	barad update          replace barad with its newest release
//	barad help            all scriptable commands
//
// Credentials live in ~/.config/barad (or $XDG_CONFIG_HOME/barad).
// BARAD_API_URL points at another stack; BARAD_THEME=auto|night|paper overrides
// theme detection.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/cli"
	"github.com/barad-io/barad/internal/tui"
	"github.com/barad-io/barad/internal/update"
)

// version is the release, set when built (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		code, say := cli.ExitCode(err)
		if say {
			fmt.Fprintln(os.Stderr, "barad:", err)
		}
		os.Exit(code)
	}
}

func run(args []string) error {
	client, err := api.New()
	if err != nil {
		return err
	}
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "":
		return runTUI(client, tui.Options{})
	case "-r", "--resume":
		return runTUI(client, tui.Options{Resume: true})
	case "login":
		return login(client)
	case "logout":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := client.SignOut(ctx); err != nil {
			return err
		}
		fmt.Println("Signed out.")
		return nil
	case "version", "--version", "-v":
		fmt.Println("barad", version)
		return nil
	case "update":
		return selfUpdate()
	case "help", "-h", "--help":
		fmt.Print("usage: barad [SESSION | -r | COMMAND]\n\n" +
			"  barad               open the fuzzy session picker\n" +
			"  barad -r, --resume  resume the last session\n" +
			"  barad SESSION       attach straight into a session's TUI\n" +
			"  barad login         sign in: approve this computer in your browser\n" +
			"  barad logout        sign out on this computer\n" +
			"  barad update        replace barad with its newest release\n" +
			"  barad version       the release this is\n")
		return cli.Help(os.Stdout, "")
	}
	if cli.Known(command) {
		if !client.SignedIn() {
			return errors.New("you are signed out; run barad login")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return cli.Run(ctx, client, args, os.Stdin, os.Stdout)
	}
	// If it does not look like a flag, treat `barad <session>` as attaching directly to that session.
	if !strings.HasPrefix(command, "-") {
		query := strings.Join(args, " ")
		return runTUI(client, tui.Options{SessionQuery: query})
	}
	return fmt.Errorf("unknown command %q; barad help lists them", command)
}

func runTUI(client *api.Client, opts tui.Options) error {
	if !client.SignedIn() {
		if err := login(client); err != nil {
			return err
		}
	}
	opts.Version = version
	return tui.Run(client, opts)
}

func selfUpdate() error {
	if !update.Released(version) {
		return errors.New("this is a development build; install a release with: curl -fsSL https://github.com/barad-io/barad/releases/latest/download/install.sh | sh")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	latest, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(latest, version) {
		fmt.Printf("barad %s is the newest release.\n", version)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	fmt.Printf("Updating barad %s to %s…\n", version, latest)
	if err := update.Install(ctx, latest, exe); err != nil {
		return err
	}
	fmt.Printf("barad is now %s.\n", latest)
	return nil
}

func login(client *api.Client) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	host, _ := os.Hostname()
	device := fmt.Sprintf("barad on %s (%s/%s)", host, runtime.GOOS, runtime.GOARCH)
	started, err := client.StartSignIn(ctx, device)
	if err != nil {
		return fmt.Errorf("could not start signing in: %w", err)
	}
	fmt.Printf("Sign in to Barad in your browser and approve this code:\n\n    %s\n\n", started.UserCode)
	if tui.OpenURL(started.VerificationURL) == nil {
		fmt.Printf("If no browser opened, go to %s\n", started.VerificationURL)
	} else {
		fmt.Printf("Open %s\n", started.VerificationURL)
	}
	fmt.Println("Waiting for you to approve it…")
	if err := client.FinishSignIn(ctx, started); err != nil {
		return err
	}
	if err := client.InitializeAccount(ctx); err != nil {
		return err
	}
	if email := client.Email(); email != "" {
		fmt.Printf("Signed in as %s.\n", email)
	} else {
		fmt.Println("Signed in.")
	}
	return nil
}

// barad is Barad in the terminal: your sessions, their conversations, and
// everything the web app does with them.
//
//	barad            the app (signs you in first if needed)
//	barad login      sign in: approve this computer in the web app
//	barad logout     sign out on this computer
//	barad version    the release this is
//	barad update     replace barad with its newest release
//
// Credentials live in the user config directory (~/.config/barad on Linux),
// readable only by you. BARAD_API_URL points it at another stack;
// BARAD_THEME=night|paper and BARAD_GRAPHICS=kitty|blocks override what it
// detects.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/tui"
	"github.com/barad-io/barad/internal/update"
)

// version is the release, set when it is built (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "barad:", err)
		os.Exit(1)
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
		if !client.SignedIn() {
			if err := login(client); err != nil {
				return err
			}
		}
		return tui.Run(client, version)
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
		fmt.Println("usage: barad [login | logout | update | version]")
		return nil
	}
	return fmt.Errorf("unknown command %q (try: barad, barad login, barad logout, barad update, barad version)", command)
}

// selfUpdate replaces this barad with the newest release, once its signed
// checksums vouch for the download.
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

// login signs in with a device code: it opens the web app, where you are
// already signed in, at a page asking you to approve this computer, and waits
// for your answer. Your password never passes through the command line.
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
	if openBrowser(started.VerificationURL) == nil {
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

// openBrowser opens url in the default browser, without waiting for it.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Start()
}

// barad is Barad in the terminal: your sessions, their conversations, and
// everything the web app does with them.
//
//	barad            the app (signs you in first if needed)
//	barad login      sign in with your email and password
//	barad logout     sign out on this computer
//	barad version    the release this is
//
// Credentials live in the user config directory (~/.config/barad on Linux),
// readable only by you. BARAD_API_URL, BARAD_COGNITO_URL and
// BARAD_COGNITO_CLIENT_ID point it at another stack; BARAD_THEME=night|paper
// and BARAD_GRAPHICS=kitty|blocks override what it detects.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/tui"
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
		return tui.Run(client)
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
	case "help", "-h", "--help":
		fmt.Println("usage: barad [login | logout | version]")
		return nil
	}
	return fmt.Errorf("unknown command %q (try: barad, barad login, barad logout, barad version)", command)
}

// login asks for an email and password and signs in, choosing a new
// password first for an invited account.
func login(client *api.Client) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("signing in needs a terminal")
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Println("Sign in to Barad")
	fmt.Print("Email: ")
	email, err := in.ReadString('\n')
	if err != nil {
		return err
	}
	password, err := secret("Password: ")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err = client.SignIn(ctx, strings.TrimSpace(email), password)
	var challenge *api.NewPasswordRequired
	if errors.As(err, &challenge) {
		fmt.Println("This account needs a password of your own.")
		next, err := secret("New password: ")
		if err != nil {
			return err
		}
		again, err := secret("New password again: ")
		if err != nil {
			return err
		}
		if next != again {
			return errors.New("the passwords differ")
		}
		err = client.SetNewPassword(ctx, challenge, next)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := client.InitializeAccount(ctx); err != nil {
		return err
	}
	fmt.Printf("Signed in as %s.\n", client.Email())
	return nil
}

func secret(prompt string) (string, error) {
	fmt.Print(prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return string(b), err
}

// Package api is the Barad command line's client for the Barad API: Cognito
// sign-in with a stored refresh token, the authenticated REST routes the web
// app uses, and the live-update socket.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Endpoints is where the client talks to. The defaults are production; the
// BARAD_API_URL, BARAD_COGNITO_URL and BARAD_COGNITO_CLIENT_ID variables point
// it somewhere else (a development stack, or the fake API in tests).
type Endpoints struct {
	API      string // https://api.barad.io
	Cognito  string // https://cognito-idp.eu-west-1.amazonaws.com/
	ClientID string
}

func DefaultEndpoints() Endpoints {
	e := Endpoints{API: "https://api.barad.io", Cognito: "https://cognito-idp.eu-west-1.amazonaws.com/", ClientID: "41mukom8o592l2lk2vcjum953h"}
	if v := os.Getenv("BARAD_API_URL"); v != "" {
		e.API = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("BARAD_COGNITO_URL"); v != "" {
		e.Cognito = v
	}
	if v := os.Getenv("BARAD_COGNITO_CLIENT_ID"); v != "" {
		e.ClientID = v
	}
	return e
}

// Tokens is the signed-in identity, as stored on disk.
type Tokens struct {
	AccessToken  string    `json:"access_token"`
	IDToken      string    `json:"id_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Email        string    `json:"email"`
}

// Client is safe for concurrent use: the Bubble Tea program runs requests in
// commands, and the update socket runs beside them.
type Client struct {
	Endpoints Endpoints
	HTTP      *http.Client
	dir       string // where credentials.json lives

	mu        sync.Mutex
	tokens    Tokens
	refreshMu sync.Mutex // one token exchange at a time
}

// ErrSignedOut means there are no usable credentials; run `barad login`.
var ErrSignedOut = errors.New("not signed in; run `barad login`")

// Error is a failed API call, carrying the API's own error code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return e.Message
}

// IsCode reports whether err is an API error with the given code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// ConfigDir is where the command line keeps its files: $XDG_CONFIG_HOME/barad
// (~/.config/barad on Linux, ~/Library/Application Support/barad on macOS).
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "barad"), nil
}

// New returns a client for the stored credentials, if any.
func New() (*Client, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	c := &Client{Endpoints: DefaultEndpoints(), HTTP: &http.Client{Timeout: 30 * time.Second}, dir: dir}
	data, err := os.ReadFile(c.credentialsPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &c.tokens); err != nil {
			return nil, fmt.Errorf("%s is unreadable: %w", c.credentialsPath(), err)
		}
	}
	return c, nil
}

func (c *Client) credentialsPath() string { return filepath.Join(c.dir, "credentials.json") }

// SignedIn reports whether a refresh token is stored.
func (c *Client) SignedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens.RefreshToken != ""
}

// Email is the signed-in account's address.
func (c *Client) Email() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens.Email
}

func (c *Client) store(t Tokens) error {
	c.mu.Lock()
	c.tokens = t
	c.mu.Unlock()
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	// Written whole, then renamed, so a crash never leaves half a token.
	tmp := c.credentialsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.credentialsPath())
}

// accessToken returns a token that is valid for at least a minute,
// refreshing it first when it is not.
func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	t := c.tokens
	c.mu.Unlock()
	if t.RefreshToken == "" {
		return "", ErrSignedOut
	}
	if !force && t.AccessToken != "" && time.Until(t.ExpiresAt) > time.Minute {
		return t.AccessToken, nil
	}
	if err := c.refresh(ctx, t); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens.AccessToken, nil
}

// Do calls the API: method on path (with its query), in as the JSON body,
// out decoded from the JSON response. A 401 refreshes the token once.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	send := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, c.Endpoints.API+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return c.HTTP.Do(req)
	}
	token, err := c.accessToken(ctx, false)
	if err != nil {
		return err
	}
	resp, err := send(token)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if token, err = c.accessToken(ctx, true); err != nil {
			return err
		}
		if resp, err = send(token); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error.Message == "" {
			e.Error.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return &Error{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Fetch downloads a presigned URL (an image).
func (c *Client) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func escape(s string) string { return url.PathEscape(s) }

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

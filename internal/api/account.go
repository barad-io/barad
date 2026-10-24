package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/barad-io/barad/wire"
)

// The account's routes: provider connections, source control, billing and
// the password, as the web app's Settings uses them.

// ProviderTypes are the kinds of provider connection, in the web app's
// order, with its names for them.
var ProviderTypes = []struct{ Type, Name string }{
	{"chatgpt", "ChatGPT subscription"}, {"openai_api", "OpenAI API"}, {"grok", "Grok subscription"},
	{"claude", "Claude subscription"}, {"xai_api", "xAI API"},
}

// ProviderTypeName is the web app's name for a kind of connection.
func ProviderTypeName(providerType string) string {
	for _, p := range ProviderTypes {
		if p.Type == providerType {
			return p.Name
		}
	}
	return providerType
}

// Identity is who sessions' commits are by, as the web app previews it: the
// author is you, or Barad; the committer is the author, unless the
// identities are separate, when it is the other.
func Identity(s wire.GitSettings) (author, committer string) {
	you := "not set"
	if s.UserName != "" && s.HumanEmail != "" {
		you = fmt.Sprintf("%s <%s>", s.UserName, s.HumanEmail)
	}
	author, other := you, "Barad"
	if s.BaradIsAuthor {
		author, other = "Barad", you
	}
	if s.SeparateIdentities {
		return author, other
	}
	return author, author
}

// UsesAPIKey reports whether a provider type connects with an API key, not
// a subscription sign-in.
func UsesAPIKey(providerType string) bool {
	return providerType == "openai_api" || providerType == "xai_api"
}

// AddAPIKeyProvider connects a provider with an API key.
func (c *Client) AddAPIKeyProvider(ctx context.Context, providerType, name, key string) (*wire.ProviderView, error) {
	var out wire.ProviderView
	err := c.Do(ctx, http.MethodPost, "/account/providers/api-key", map[string]string{"type": providerType, "name": name, "api_key": key}, &out)
	return &out, err
}

// PutAPIKey adds or replaces a connection's API key.
func (c *Client) PutAPIKey(ctx context.Context, resourceID, key string) error {
	return c.Do(ctx, http.MethodPut, "/account/providers/"+escape(resourceID)+"/api-key", map[string]string{"api_key": key}, nil)
}

// AuthorizeProvider starts a subscription sign-in: for a new connection
// named name, or with resourceID, for an existing one.
func (c *Client) AuthorizeProvider(ctx context.Context, providerType, name, resourceID string) (*wire.ProviderAuthorization, error) {
	in := map[string]string{"type": providerType, "name": name}
	if resourceID != "" {
		in["resource_id"] = resourceID
	}
	var out wire.ProviderAuthorization
	err := c.Do(ctx, http.MethodPost, "/account/providers/authorize", in, &out)
	return &out, err
}

func authorizationPath(flow *wire.ProviderAuthorization, providerType, verb string) string {
	return fmt.Sprintf("/account/providers/authorization/%s/%s?type=%s&resource_id=%s",
		escape(flow.FlowID), verb, url.QueryEscape(providerType), url.QueryEscape(flow.ResourceID))
}

// PollProvider asks how a sign-in stands. A poll too soon is not an error:
// it reads as pending.
func (c *Client) PollProvider(ctx context.Context, flow *wire.ProviderAuthorization, providerType string) (*wire.ProviderAuthorizationPoll, error) {
	var out wire.ProviderAuthorizationPoll
	err := c.Do(ctx, http.MethodPost, authorizationPath(flow, providerType, "poll"), nil, &out)
	if IsCode(err, "poll_too_soon") {
		return &wire.ProviderAuthorizationPoll{Pending: true}, nil
	}
	return &out, err
}

// SubmitProviderCode passes Claude's sign-in the code its page showed.
func (c *Client) SubmitProviderCode(ctx context.Context, flow *wire.ProviderAuthorization, code string) error {
	return c.Do(ctx, http.MethodPost, authorizationPath(flow, "claude", "code"), map[string]string{"code": code}, nil)
}

// CancelProvider abandons Claude's sign-in.
func (c *Client) CancelProvider(ctx context.Context, flow *wire.ProviderAuthorization) error {
	return c.Do(ctx, http.MethodPost, authorizationPath(flow, "claude", "cancel"), nil, nil)
}

// RemoveCredential signs a connection out: sessions using it stop until it
// signs in again.
func (c *Client) RemoveCredential(ctx context.Context, resourceID string) error {
	return c.Do(ctx, http.MethodDelete, "/account/providers/"+escape(resourceID)+"/credential", nil, nil)
}

// Connections are provider connections ("providers") and Git sources
// ("git-sources"): both rename, list their sessions, and are removed alike.

func (c *Client) RenameConnection(ctx context.Context, route, resourceID, name string) error {
	return c.Do(ctx, http.MethodPatch, "/account/"+route+"/"+escape(resourceID), map[string]string{"name": name}, nil)
}

func (c *Client) RemoveConnection(ctx context.Context, route, resourceID string) error {
	return c.Do(ctx, http.MethodDelete, "/account/"+route+"/"+escape(resourceID), nil, nil)
}

// ConnectionUsers is the sessions using a connection.
func (c *Client) ConnectionUsers(ctx context.Context, route, resourceID string) ([]wire.ResourceDependency, error) {
	var out struct {
		Sessions []wire.ResourceDependency `json:"sessions"`
	}
	err := c.Do(ctx, http.MethodGet, "/account/"+route+"/"+escape(resourceID)+"/dependencies", nil, &out)
	return out.Sessions, err
}

// GitSources is the account's connected GitHub accounts and organizations.
func (c *Client) GitSources(ctx context.Context) ([]wire.AccountResource, error) {
	var out struct {
		Sources []wire.AccountResource `json:"sources"`
	}
	err := c.Do(ctx, http.MethodGet, "/account/git-sources", nil, &out)
	return out.Sources, err
}

// SourceRepositories is the repositories one Git source reaches.
func (c *Client) SourceRepositories(ctx context.Context, sourceID string) ([]wire.GitHubRepository, error) {
	var out struct {
		Repositories []wire.GitHubRepository `json:"repositories"`
	}
	err := c.Do(ctx, http.MethodGet, "/account/git-sources/"+escape(sourceID)+"/repositories", nil, &out)
	return out.Repositories, err
}

func (c *Client) GitHub(ctx context.Context) (*wire.GitHubAccount, error) {
	var out wire.GitHubAccount
	err := c.Do(ctx, http.MethodGet, "/account/github", nil, &out)
	return &out, err
}

// GitHubSignIn is the GitHub page that signs the account in.
func (c *Client) GitHubSignIn(ctx context.Context) (*wire.GitHubLink, error) {
	var out wire.GitHubLink
	err := c.Do(ctx, http.MethodPost, "/account/github/sign-in", map[string]any{}, &out)
	return &out, err
}

// GitHubInstall is the GitHub page that installs the app on another
// account or organization.
func (c *Client) GitHubInstall(ctx context.Context) (*wire.GitHubLink, error) {
	var out wire.GitHubLink
	err := c.Do(ctx, http.MethodPost, "/account/git-sources/github/install", map[string]any{}, &out)
	return &out, err
}

// ConnectInstallation connects an installation you can reach as a Git source.
func (c *Client) ConnectInstallation(ctx context.Context, installationID int64) (*wire.AccountResource, error) {
	var out wire.AccountResource
	err := c.Do(ctx, http.MethodPost, fmt.Sprintf("/account/github/installations/%d", installationID), map[string]any{}, &out)
	return &out, err
}

// GitHubSignOut forgets the GitHub sign-in. Sources stay connected, but
// their repositories are out of reach until you sign in again.
func (c *Client) GitHubSignOut(ctx context.Context) error {
	return c.Do(ctx, http.MethodDelete, "/account/github", nil, nil)
}

func (c *Client) GitSettings(ctx context.Context) (*wire.GitSettings, error) {
	var out wire.GitSettings
	err := c.Do(ctx, http.MethodGet, "/account/git-settings", nil, &out)
	return &out, err
}

func (c *Client) PutGitSettings(ctx context.Context, in wire.GitSettings) (*wire.GitSettings, error) {
	var out wire.GitSettings
	err := c.Do(ctx, http.MethodPut, "/account/git-settings", in, &out)
	return &out, err
}

// Billing is the account's credit balance and terms.
func (c *Client) Billing(ctx context.Context) (*wire.BillingAccount, error) {
	var out wire.BillingAccount
	err := c.Do(ctx, http.MethodGet, "/account/billing", nil, &out)
	return &out, err
}

// AcceptTerms accepts the terms at version.
func (c *Client) AcceptTerms(ctx context.Context, version string) error {
	return c.Do(ctx, http.MethodPost, "/account/billing/terms", map[string]string{"version": version}, nil)
}

// Checkout starts buying credits: it returns the payment page.
func (c *Client) Checkout(ctx context.Context, requestID string, cents int64, termsVersion string) (*wire.Checkout, error) {
	var out wire.Checkout
	err := c.Do(ctx, http.MethodPost, "/account/billing/checkout", map[string]any{
		"request_id": requestID, "amount_usd_cents": cents, "terms_version": termsVersion, "immediate_service_requested": true,
	}, &out)
	return &out, err
}

// BillingHistory is a page of the balance's history, newest first.
func (c *Client) BillingHistory(ctx context.Context, cursor string) (*wire.BillingHistory, error) {
	path := "/account/billing/history"
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	var out wire.BillingHistory
	err := c.Do(ctx, http.MethodGet, path, nil, &out)
	return &out, err
}

// ChangePassword changes the account's password, at Cognito, with this
// sign-in's access token.
func (c *Client) ChangePassword(ctx context.Context, previous, proposed string) error {
	token, err := c.accessToken(ctx, false)
	if err != nil {
		return err
	}
	c.mu.Lock()
	endpoint := c.tokens.CognitoURL
	c.mu.Unlock()
	err = c.cognito(ctx, endpoint, "ChangePassword", map[string]string{"AccessToken": token, "PreviousPassword": previous, "ProposedPassword": proposed}, nil)
	var ce *CognitoError
	if errors.As(err, &ce) && ce.Type == "NotAuthorizedException" {
		return errors.New("the current password is not right")
	}
	return err
}

// Owner commands are what you ask a session's computer directly, as the
// web app's Files panel does: list, read, upload, download.

// Command runs an owner command and waits for it to end.
func (c *Client) Command(ctx context.Context, sessionID, requestID, kind string, args any) (*wire.OwnerCommand, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/commands",
		map[string]any{"owner_request_id": requestID, "kind": kind, "arguments": json.RawMessage(raw)}, nil); err != nil {
		return nil, err
	}
	for wait := 100 * time.Millisecond; ; wait = min(2*wait, 650*time.Millisecond) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		var out wire.OwnerCommand
		if err := c.Do(ctx, http.MethodGet, "/sessions/"+escape(sessionID)+"/commands/"+escape(requestID), nil, &out); err != nil {
			return nil, err
		}
		switch out.Status {
		case "completed":
			return &out, nil
		case "stale":
			return nil, fmt.Errorf("%s expired before the session's computer was ready", kind)
		case "failed":
			if out.Error != nil && out.Error.Message != "" {
				return nil, errors.New(out.Error.Message)
			}
			return nil, fmt.Errorf("%s failed", kind)
		}
	}
}

// ReserveUploads reserves room for files to go to a workspace directory.
func (c *Client) ReserveUploads(ctx context.Context, sessionID, transferID, destination string, files []FileSize, overwrite bool) (*wire.FileUploads, error) {
	var out wire.FileUploads
	err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/file-uploads", map[string]any{
		"transfer_id": transferID, "destination": destination, "files": files, "overwrite": overwrite,
	}, &out)
	return &out, err
}

// FileSize names a file to upload.
type FileSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Put sends size bytes from body to a presigned request.
func (c *Client) Put(ctx context.Context, to wire.PresignedHTTPRequest, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, to.URL, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	for k, v := range to.Headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req) // no timeout: a file may be large; ctx ends it
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("upload failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// Download copies a presigned URL to w.
func (c *Client) Download(ctx context.Context, rawURL string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed (HTTP %d)", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// TerminalTicket is where a session's terminal waits: the relay to connect
// to, the route that pairs you with the session's computer, and its half of
// the key exchange.
type TerminalTicket struct {
	RelayURL  string `json:"relay_url"`
	Route     string `json:"route"`
	PublicKey string `json:"public_key"`
}

// OpenTerminal starts a login shell in an awake session's computer, for a
// caller with publicKey (ECDH P-256, base64url) and a window of rows×cols.
func (c *Client) OpenTerminal(ctx context.Context, sessionID, publicKey string, rows, cols int) (*TerminalTicket, error) {
	var out TerminalTicket
	err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/terminal", map[string]any{"public_key": publicKey, "rows": rows, "cols": cols}, &out)
	return &out, err
}

// Activity keeps an awake session awake while you type, in its terminal or
// a message to it: Barad cannot see either until it is sent. It sleeps ten
// minutes after the last.
func (c *Client) Activity(ctx context.Context, sessionID string) error {
	return c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/activity", map[string]any{}, nil)
}

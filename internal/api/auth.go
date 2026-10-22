package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/barad-io/barad/wire"
)

// Sign-in is a device code: barad asks the API for a code, you approve it in
// the web app where you are already signed in, and the API hands barad a
// Cognito session on the command line's own app client. barad never sees your
// password. Refreshing and revoking that session talk to Cognito directly.

// StartSignIn asks for a sign-in code for this computer (device names it on
// the approval page).
func (c *Client) StartSignIn(ctx context.Context, device string) (*wire.CLISignInStarted, error) {
	var out wire.CLISignInStarted
	if err := c.doPublic(ctx, http.MethodPost, "/cli/sign-in", map[string]string{"device": device}, &out); err != nil {
		return nil, err
	}
	if out.UserCode == "" || out.DeviceCode == "" || out.VerificationURL == "" {
		return nil, errors.New("the sign-in request was incomplete")
	}
	return &out, nil
}

// FinishSignIn waits for the code to be approved, then stores the session.
// It returns when you approve or deny it, when it expires, or when ctx ends.
func (c *Client) FinishSignIn(ctx context.Context, started *wire.CLISignInStarted) error {
	interval := time.Duration(max(started.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(started.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		var out wire.CLITokens
		err := c.doPublic(ctx, http.MethodPost, "/cli/sign-in/token", map[string]string{"user_code": started.UserCode, "device_code": started.DeviceCode}, &out)
		var e *Error
		switch {
		case err == nil:
			if out.RefreshToken == "" || out.ClientID == "" || out.CognitoURL == "" {
				return errors.New("the sign-in returned no session")
			}
			return c.store(Tokens{AccessToken: out.AccessToken, IDToken: out.IDToken, RefreshToken: out.RefreshToken,
				ExpiresAt: time.Now().Add(time.Duration(max(out.ExpiresIn, 60)) * time.Second), Email: emailOf(out.IDToken),
				ClientID: out.ClientID, CognitoURL: out.CognitoURL})
		case errors.As(err, &e) && e.Code == "authorization_pending":
		case errors.As(err, &e) && e.Code == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &e) && e.Code == "access_denied":
			return errors.New("the sign-in was denied")
		case errors.As(err, &e) && (e.Code == "expired_token" || e.Code == "invalid_grant"):
			return errors.New("the sign-in code expired; run barad login again")
		case errors.As(err, &e) && e.Status < 500:
			return err
		default:
			// The network or the API failed; keep waiting while the code lives.
		}
		if time.Now().After(deadline) {
			return errors.New("the sign-in code expired; run barad login again")
		}
	}
}

// emailOf reads the email claim of an ID token, for showing who is signed in.
// The token is not verified here: it came from the API over TLS, and nothing
// is decided by it.
func emailOf(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Email
}

// CognitoError is a failed Cognito call; Type is its exception name
// (NotAuthorizedException, TooManyRequestsException…).
type CognitoError struct {
	Type    string
	Message string
}

func (e *CognitoError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Type
}

func (c *Client) cognito(ctx context.Context, endpoint, action string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+action)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		_, name, _ := strings.Cut(e.Type, "#")
		if name == "" {
			name = e.Type
		}
		if name == "" {
			name = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return &CognitoError{Type: name, Message: e.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// refresh exchanges the refresh token in t for new tokens. Concurrent callers
// wait for one exchange; a caller that waited finds the tokens already new.
func (c *Client) refresh(ctx context.Context, t Tokens) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.Lock()
	current := c.tokens
	c.mu.Unlock()
	if current.AccessToken != t.AccessToken && time.Until(current.ExpiresAt) > time.Minute {
		return nil // someone else refreshed while this caller waited
	}
	var out struct {
		AuthenticationResult *struct {
			AccessToken  string `json:"AccessToken"`
			IDToken      string `json:"IdToken"`
			RefreshToken string `json:"RefreshToken"`
			ExpiresIn    int    `json:"ExpiresIn"`
		} `json:"AuthenticationResult"`
	}
	err := c.cognito(ctx, current.CognitoURL, "InitiateAuth", map[string]any{
		"AuthFlow": "REFRESH_TOKEN_AUTH", "ClientId": current.ClientID,
		"AuthParameters": map[string]string{"REFRESH_TOKEN": current.RefreshToken},
	}, &out)
	var ce *CognitoError
	if errors.As(err, &ce) && ce.Type == "NotAuthorizedException" {
		return ErrSignedOut // the refresh token expired or was revoked
	}
	if err != nil {
		return err
	}
	r := out.AuthenticationResult
	if r == nil {
		return errors.New("the sign-in service returned no tokens")
	}
	next := current
	next.AccessToken, next.IDToken = r.AccessToken, r.IDToken
	next.ExpiresAt = time.Now().Add(time.Duration(max(r.ExpiresIn, 60)) * time.Second)
	if r.RefreshToken != "" {
		next.RefreshToken = r.RefreshToken // Cognito returns a new one only when it rotates
	}
	return c.store(next)
}

// SignOut revokes the refresh token and forgets the credentials.
func (c *Client) SignOut(ctx context.Context) error {
	c.mu.Lock()
	t := c.tokens
	c.mu.Unlock()
	if t.RefreshToken != "" && t.ClientID != "" && t.CognitoURL != "" {
		// Best effort: an unreachable Cognito must not keep the credentials on disk.
		_ = c.cognito(ctx, t.CognitoURL, "RevokeToken", map[string]string{"Token": t.RefreshToken, "ClientId": t.ClientID}, nil)
	}
	c.mu.Lock()
	c.tokens = Tokens{}
	c.mu.Unlock()
	return removeIfExists(c.credentialsPath())
}

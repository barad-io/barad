package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sign-in talks to Cognito directly, as the web app does: USER_PASSWORD_AUTH
// for email and password, NEW_PASSWORD_REQUIRED for invited accounts, and
// REFRESH_TOKEN_AUTH to keep the session alive.

// CognitoError is a failed Cognito call; Type is its exception name
// (NotAuthorizedException, UserNotConfirmedException…).
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

type authResult struct {
	AccessToken  string `json:"AccessToken"`
	IDToken      string `json:"IdToken"`
	RefreshToken string `json:"RefreshToken"`
	ExpiresIn    int    `json:"ExpiresIn"`
}

type initiateResponse struct {
	AuthenticationResult *authResult `json:"AuthenticationResult"`
	ChallengeName        string      `json:"ChallengeName"`
	Session              string      `json:"Session"`
}

func (c *Client) cognito(ctx context.Context, action string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoints.Cognito, bytes.NewReader(body))
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

func (c *Client) adopt(r *authResult, email, previousRefresh string) error {
	t := Tokens{AccessToken: r.AccessToken, IDToken: r.IDToken, RefreshToken: r.RefreshToken, Email: email,
		ExpiresAt: time.Now().Add(time.Duration(max(r.ExpiresIn, 60)) * time.Second)}
	if t.RefreshToken == "" {
		t.RefreshToken = previousRefresh // Cognito returns a new one only when it rotates
	}
	return c.store(t)
}

// NewPasswordRequired is returned by SignIn for an invited account that must
// choose its own password; finish with SetNewPassword.
type NewPasswordRequired struct {
	Email   string
	session string
}

func (e *NewPasswordRequired) Error() string { return "this account needs a new password" }

// SignIn signs in with email and password and stores the tokens.
func (c *Client) SignIn(ctx context.Context, email, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	var out initiateResponse
	err := c.cognito(ctx, "InitiateAuth", map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": c.Endpoints.ClientID,
		"AuthParameters": map[string]string{"USERNAME": email, "PASSWORD": password},
	}, &out)
	if err != nil {
		return err
	}
	if out.ChallengeName == "NEW_PASSWORD_REQUIRED" {
		return &NewPasswordRequired{Email: email, session: out.Session}
	}
	if out.AuthenticationResult == nil {
		return fmt.Errorf("unsupported sign-in challenge %q; sign in on app.barad.io first", out.ChallengeName)
	}
	return c.adopt(out.AuthenticationResult, email, "")
}

// SetNewPassword answers a NEW_PASSWORD_REQUIRED challenge.
func (c *Client) SetNewPassword(ctx context.Context, challenge *NewPasswordRequired, password string) error {
	var out initiateResponse
	err := c.cognito(ctx, "RespondToAuthChallenge", map[string]any{
		"ClientId": c.Endpoints.ClientID, "ChallengeName": "NEW_PASSWORD_REQUIRED", "Session": challenge.session,
		"ChallengeResponses": map[string]string{"USERNAME": challenge.Email, "NEW_PASSWORD": password},
	}, &out)
	if err != nil {
		return err
	}
	if out.AuthenticationResult == nil {
		return errors.New("the password change did not complete; sign in again")
	}
	return c.adopt(out.AuthenticationResult, challenge.Email, "")
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
	var out initiateResponse
	err := c.cognito(ctx, "InitiateAuth", map[string]any{
		"AuthFlow": "REFRESH_TOKEN_AUTH", "ClientId": c.Endpoints.ClientID,
		"AuthParameters": map[string]string{"REFRESH_TOKEN": current.RefreshToken},
	}, &out)
	var ce *CognitoError
	if errors.As(err, &ce) && ce.Type == "NotAuthorizedException" {
		return ErrSignedOut // the refresh token expired or was revoked
	}
	if err != nil {
		return err
	}
	if out.AuthenticationResult == nil {
		return errors.New("the sign-in service returned no tokens")
	}
	return c.adopt(out.AuthenticationResult, current.Email, current.RefreshToken)
}

// SignOut revokes the refresh token and forgets the credentials.
func (c *Client) SignOut(ctx context.Context) error {
	c.mu.Lock()
	t := c.tokens
	c.mu.Unlock()
	if t.RefreshToken != "" {
		// Best effort: an unreachable Cognito must not keep the credentials on disk.
		_ = c.cognito(ctx, "RevokeToken", map[string]string{"Token": t.RefreshToken, "ClientId": c.Endpoints.ClientID}, nil)
	}
	c.mu.Lock()
	c.tokens = Tokens{}
	c.mu.Unlock()
	if err := removeIfExists(c.credentialsPath()); err != nil {
		return err
	}
	return nil
}

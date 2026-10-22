package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/barad-io/barad/wire"
)

// The routes the command line uses, with the shapes the API sends. Session,
// transcript and turn values are the server's own types.

// InitializeAccount is called once after every sign-in, as the web app does.
func (c *Client) InitializeAccount(ctx context.Context) error {
	return c.Do(ctx, http.MethodPost, "/account/initialize", map[string]any{}, nil)
}

func (c *Client) Sessions(ctx context.Context) ([]wire.Session, error) {
	var out struct {
		Sessions []wire.Session `json:"sessions"`
	}
	err := c.Do(ctx, http.MethodGet, "/sessions", nil, &out)
	return out.Sessions, err
}

// PendingMessage is a message the server holds for the agent: in the inbox
// until a turn starts, or steering an active turn at its next step.
type PendingMessage struct {
	RequestID string                `json:"request_id"`
	Text      string                `json:"text"`
	Kind      string                `json:"kind"`
	Media     []wire.PromptMediaRef `json:"media,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
}

// Sync is one page of a session: its state, the transcript after a cursor
// (or the newest page), and live events after a cursor.
type Sync struct {
	Session                wire.Session               `json:"session"`
	Turns                  []wire.Turn                `json:"turns"`
	PendingMessages        []PendingMessage           `json:"pending_messages"`
	Proposals              []wire.SessionProposalView `json:"proposals"`
	Items                  []wire.TranscriptItem      `json:"items"`
	TranscriptNextCursor   int64                      `json:"transcript_next_cursor"`
	TranscriptHasMore      bool                       `json:"transcript_has_more"`
	TranscriptBeforeCursor int64                      `json:"transcript_before_cursor"`
	TranscriptHasOlder     bool                       `json:"transcript_has_older"`
	Events                 []wire.Event               `json:"events"`
	EventNextCursor        int64                      `json:"event_next_cursor"`
	EventHasMore           bool                       `json:"event_has_more"`
}

// Sync reads a session. With initial set it returns the newest transcript
// page; otherwise the items after afterTranscript.
func (c *Client) Sync(ctx context.Context, sessionID string, initial bool, afterTranscript, afterEvent int64) (*Sync, error) {
	query := fmt.Sprintf("after_event=%d", afterEvent)
	if !initial {
		query = fmt.Sprintf("after_transcript=%d&%s", afterTranscript, query)
	}
	var out Sync
	err := c.Do(ctx, http.MethodGet, "/sessions/"+escape(sessionID)+"/sync?"+query, nil, &out)
	return &out, err
}

// Older is a page of the transcript before a cursor.
type Older struct {
	Items        []wire.TranscriptItem      `json:"items"`
	Turns        []wire.Turn                `json:"turns"`
	BeforeCursor int64                      `json:"before_cursor"`
	HasOlder     bool                       `json:"has_older"`
	Proposals    []wire.SessionProposalView `json:"proposals"`
}

func (c *Client) TranscriptBefore(ctx context.Context, sessionID string, before int64) (*Older, error) {
	var out Older
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/sessions/%s/transcript?before=%d", escape(sessionID), before), nil, &out)
	return &out, err
}

func (c *Client) SendMessage(ctx context.Context, sessionID, requestID, text string, mediaIDs []string) (*wire.MessageAdmission, error) {
	if mediaIDs == nil {
		mediaIDs = []string{}
	}
	var out wire.MessageAdmission
	err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/messages", map[string]any{"request_id": requestID, "text": text, "media_ids": mediaIDs}, &out)
	return &out, err
}

func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/cancel", map[string]any{}, nil)
}

func (c *Client) Archive(ctx context.Context, sessionID string) (*wire.Session, error) {
	var out wire.Session
	err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/archive", map[string]any{}, &out)
	return &out, err
}

// Settings is a session's editable settings. The API replaces all of them,
// so a change starts from the session's current values.
type Settings struct {
	Name            string               `json:"name"`
	Model           string               `json:"model,omitempty"`
	ExpectedModel   string               `json:"expected_model,omitempty"`
	ReasoningEffort wire.ReasoningEffort `json:"reasoning_effort"`
	FastMode        bool                 `json:"fast_mode"`
}

// SettingsOf is s's current settings, to change one of.
func SettingsOf(s wire.Session) Settings {
	return Settings{Name: s.Name, Model: s.Model, ExpectedModel: s.Model, ReasoningEffort: s.ReasoningEffort, FastMode: s.FastMode}
}

func (c *Client) UpdateSettings(ctx context.Context, sessionID string, in Settings) (*wire.Session, error) {
	var out wire.Session
	err := c.Do(ctx, http.MethodPatch, "/sessions/"+escape(sessionID), in, &out)
	return &out, err
}

// NewSession is what a session starts with.
type NewSession struct {
	ProviderResourceID string                     `json:"provider_resource_id"`
	Model              string                     `json:"model,omitempty"`
	ReasoningEffort    wire.ReasoningEffort       `json:"reasoning_effort,omitempty"`
	ProjectID          string                     `json:"project_id,omitempty"`
	Repositories       []RepositoryRequest        `json:"repositories,omitempty"`
	Environment        []wire.EnvironmentVariable `json:"environment,omitempty"`
}

func (c *Client) CreateSession(ctx context.Context, in NewSession) (*wire.Session, error) {
	cloneDirectories(in.Repositories)
	var out wire.Session
	err := c.Do(ctx, http.MethodPost, "/sessions", in, &out)
	return &out, err
}

func (c *Client) Providers(ctx context.Context) ([]wire.ProviderView, error) {
	var out struct {
		Providers []wire.ProviderView `json:"providers"`
	}
	err := c.Do(ctx, http.MethodGet, "/account/providers", nil, &out)
	return out.Providers, err
}

// Models is a provider connection's model catalog.
type Models struct {
	Models       []wire.ModelCapabilities `json:"models"`
	DefaultModel string                   `json:"default_model"`
}

func (c *Client) Models(ctx context.Context, resourceID string) (*Models, error) {
	var out Models
	err := c.Do(ctx, http.MethodGet, "/account/providers/"+escape(resourceID)+"/models", nil, &out)
	return &out, err
}

func (c *Client) Projects(ctx context.Context) ([]wire.Project, error) {
	var out struct {
		Projects []wire.Project `json:"projects"`
	}
	err := c.Do(ctx, http.MethodGet, "/projects", nil, &out)
	return out.Projects, err
}

// ProjectSettings are what a project is: its name, colour, the agent its new
// sessions start with, and the repositories they clone. Saving replaces all
// of them.
type ProjectSettings struct {
	Name         string                   `json:"name"`
	Colour       string                   `json:"colour"`
	Agent        *wire.ProjectAgent       `json:"agent"`
	Repositories []wire.ProjectRepository `json:"repositories"`
}

// SettingsOfProject is p's current settings, to change one of.
func SettingsOfProject(p wire.Project) ProjectSettings {
	return ProjectSettings{Name: p.Name, Colour: p.Colour, Agent: p.Agent, Repositories: append([]wire.ProjectRepository{}, p.Repositories...)}
}

func (c *Client) CreateProject(ctx context.Context, in ProjectSettings) (*wire.Project, error) {
	var out wire.Project
	err := c.Do(ctx, http.MethodPost, "/projects", in, &out)
	return &out, err
}

func (c *Client) UpdateProject(ctx context.Context, projectID string, in ProjectSettings) (*wire.Project, error) {
	var out wire.Project
	err := c.Do(ctx, http.MethodPut, "/projects/"+escape(projectID), in, &out)
	return &out, err
}

// DeleteProject closes a project: its sessions move to Default; no session,
// transcript or workspace is deleted.
func (c *Client) DeleteProject(ctx context.Context, projectID string) error {
	return c.Do(ctx, http.MethodDelete, "/projects/"+escape(projectID), nil, nil)
}

// An EnvironmentOwner is whose variables they are: a session's or a project's.
type EnvironmentOwner struct {
	Kind string // "sessions" or "projects"
	ID   string
}

func (o EnvironmentOwner) path() string { return "/" + o.Kind + "/" + escape(o.ID) + "/environment" }

func (c *Client) Environment(ctx context.Context, o EnvironmentOwner) (*wire.EnvironmentView, error) {
	var out wire.EnvironmentView
	err := c.Do(ctx, http.MethodGet, o.path(), nil, &out)
	return &out, err
}

func (c *Client) ChangeEnvironment(ctx context.Context, o EnvironmentOwner, in wire.EnvironmentMutation) (*wire.EnvironmentView, error) {
	if in.SetVariables == nil {
		in.SetVariables = []wire.EnvironmentVariable{}
	}
	if in.RemoveVariables == nil {
		in.RemoveVariables = []string{}
	}
	var out wire.EnvironmentView
	err := c.Do(ctx, http.MethodPut, o.path(), in, &out)
	return &out, err
}

// Branches lists a repository's branches through the Git source that reaches it.
func (c *Client) Branches(ctx context.Context, sourceID, repository string) (*wire.GitBranches, error) {
	var out wire.GitBranches
	err := c.Do(ctx, http.MethodGet, "/account/git-sources/"+escape(sourceID)+"/branches?repository="+url.QueryEscape(repository), nil, &out)
	return &out, err
}

func (c *Client) DismissProposal(ctx context.Context, sessionID string, sequence int64) error {
	return c.Do(ctx, http.MethodPost, fmt.Sprintf("/sessions/%s/proposals/%d/dismiss", escape(sessionID), sequence), map[string]any{}, nil)
}

// Repository is a GitHub repository one of the account's connections reaches.
type Repository struct {
	SourceID      string `json:"-"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// Repositories lists every repository the account's GitHub connections reach.
func (c *Client) Repositories(ctx context.Context) ([]Repository, error) {
	var sources struct {
		Sources []wire.AccountResource `json:"sources"`
	}
	if err := c.Do(ctx, http.MethodGet, "/account/git-sources", nil, &sources); err != nil {
		return nil, err
	}
	var all []Repository
	for _, source := range sources.Sources {
		if source.Deleting {
			continue
		}
		var out struct {
			Repositories []Repository `json:"repositories"`
		}
		if err := c.Do(ctx, http.MethodGet, "/account/git-sources/"+escape(source.ResourceID)+"/repositories", nil, &out); err != nil {
			return nil, err
		}
		for _, r := range out.Repositories {
			r.SourceID = source.ResourceID
			all = append(all, r)
		}
	}
	return all, nil
}

// RepositoryRequest is one repository a new session clones.
type RepositoryRequest struct {
	SourceID     string `json:"source_id"`
	Repository   string `json:"repository"`
	RequestedRef string `json:"requested_ref,omitempty"`
	Directory    string `json:"directory,omitempty"` // where it is cloned, when its name alone would clash
}

// cloneDirectories gives each of two repositories of one name (acme/web,
// other/web), which cannot both clone to web, a directory of its own:
// owner-name, as the web app suggests.
func cloneDirectories(repos []RepositoryRequest) {
	count := map[string]int{}
	for _, r := range repos {
		count[strings.ToLower(path.Base(r.Repository))]++
	}
	for i, r := range repos {
		if count[strings.ToLower(path.Base(r.Repository))] > 1 && r.Directory == "" {
			repos[i].Directory = strings.Replace(r.Repository, "/", "-", 1)
		}
	}
}

// ApproveProposal starts the session a proposal describes, with the proposing
// session's provider connection, model and effort, its proposed
// repositories (each through the connection that reaches it) and the proposed
// variables. The decision names the new session.
func (c *Client) ApproveProposal(ctx context.Context, s wire.Session, view wire.SessionProposalView) (*wire.SessionProposalDecision, error) {
	repositories := []RepositoryRequest{}
	if len(view.Proposal.Repositories) > 0 {
		reachable, err := c.Repositories(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range view.Proposal.Repositories {
			source := ""
			for _, candidate := range reachable {
				if strings.EqualFold(candidate.FullName, r.Repository) {
					source = candidate.SourceID
					break
				}
			}
			if source == "" {
				return nil, fmt.Errorf("%s is not reachable through your GitHub connections", r.Repository)
			}
			ref := ""
			if r.RequestedRef != nil {
				ref = *r.RequestedRef
			}
			repositories = append(repositories, RepositoryRequest{SourceID: source, Repository: r.Repository, RequestedRef: ref})
		}
	}
	cloneDirectories(repositories)
	environment := []string{}
	for _, v := range view.Proposal.Environment {
		environment = append(environment, v.Name)
	}
	body := map[string]any{
		"provider_resource_id": s.ProviderResourceID, "model": s.Model, "repositories": repositories,
		"title": view.Proposal.Title, "prompt": view.Proposal.Prompt, "proposal_environment": environment,
	}
	if s.SupportsReasoningEffort && s.ReasoningEffort != "" {
		body["reasoning_effort"] = s.ReasoningEffort
	}
	var out wire.SessionProposalDecision
	err := c.Do(ctx, http.MethodPost, fmt.Sprintf("/sessions/%s/proposals/%d/approve", escape(s.SessionID), view.Sequence), body, &out)
	return &out, err
}

// UploadMedia attaches an image to the message with requestID: reserve, upload
// to the presigned URL, complete. It returns the media's id for SendMessage.
func (c *Client) UploadMedia(ctx context.Context, sessionID, requestID, mediaID, filename, mediaType string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	var reserved struct {
		Media  wire.PromptMedia           `json:"media"`
		Upload *wire.PresignedHTTPRequest `json:"upload"`
	}
	err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/prompt-media", map[string]any{
		"request_id": requestID, "media_id": mediaID, "filename": filename, "media_type": mediaType,
		"bytes": len(data), "sha256": hex.EncodeToString(sum[:]),
	}, &reserved)
	if err != nil {
		return "", err
	}
	if up := reserved.Upload; up != nil {
		method := up.Method
		if method == "" {
			method = http.MethodPut
		}
		req, err := http.NewRequestWithContext(ctx, method, up.URL, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		for k, v := range up.Headers {
			req.Header.Set(k, v)
		}
		upload := &http.Client{Timeout: 5 * time.Minute}
		resp, err := upload.Do(req)
		if err != nil {
			return "", fmt.Errorf("%s: upload failed: %w", filename, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		// 412: an earlier attempt already stored these exact bytes.
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusPreconditionFailed {
			return "", fmt.Errorf("%s: upload failed (HTTP %d)", filename, resp.StatusCode)
		}
	}
	if err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/prompt-media/"+escape(reserved.Media.MediaID)+"/complete", map[string]any{}, nil); err != nil {
		return "", err
	}
	return reserved.Media.MediaID, nil
}

// MediaURL is a short-lived link to an image you attached.
func (c *Client) MediaURL(ctx context.Context, sessionID, mediaID string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := c.Do(ctx, http.MethodGet, "/sessions/"+escape(sessionID)+"/prompt-media/"+escape(mediaID)+"/view", nil, &out)
	return out.URL, err
}

// ScreenshotURL is a short-lived link to the screenshot in the tool result at
// sequence.
func (c *Client) ScreenshotURL(ctx context.Context, sessionID string, sequence int64) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/sessions/%s/screenshots/%d/view", escape(sessionID), sequence), nil, &out)
	return out.URL, err
}

// Usage is the account's provider usage over the last days, by local day.
func (c *Client) Usage(ctx context.Context, days int, timeZone string) (*wire.UsageReport, error) {
	var out wire.UsageReport
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/account/usage?days=%d&time_zone=%s", days, escape(timeZone)), nil, &out)
	return &out, err
}

// Wake asks for a sleeping session's computer.
func (c *Client) Wake(ctx context.Context, sessionID string) error {
	return c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/hydrate", nil, nil)
}

// Share is a read-only link to a session's transcript.
type Share struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c *Client) Share(ctx context.Context, sessionID, requestID string) (*Share, error) {
	var out Share
	err := c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/shares", map[string]any{"request_id": requestID}, &out)
	return &out, err
}

// SessionRepositories names the repositories a session can reach (owner/name).
func (c *Client) SessionRepositories(ctx context.Context, sessionID string) ([]wire.SessionGitRepository, error) {
	var out struct {
		Repositories []wire.SessionGitRepository `json:"repositories"`
	}
	err := c.Do(ctx, http.MethodGet, "/sessions/"+escape(sessionID)+"/repositories", nil, &out)
	return out.Repositories, err
}

// AddRepository gives a session's Git credentials access to a repository of
// one of your Git sources. The workspace's files are not touched.
func (c *Client) AddRepository(ctx context.Context, sessionID, sourceID, repository string) error {
	return c.Do(ctx, http.MethodPost, "/sessions/"+escape(sessionID)+"/repositories", map[string]string{"source_id": sourceID, "repository": repository}, nil)
}

// RemoveRepository takes that access away. A clone in the workspace stays.
func (c *Client) RemoveRepository(ctx context.Context, sessionID string, r wire.SessionGitRepository) error {
	q := url.Values{"host": {r.Host}, "repository": {r.CanonicalPath}}
	return c.Do(ctx, http.MethodDelete, "/sessions/"+escape(sessionID)+"/repositories?"+q.Encode(), nil, nil)
}

// MoveSession lists a session under another project, whose variables it
// then receives.
func (c *Client) MoveSession(ctx context.Context, sessionID, projectID string) (*wire.Session, error) {
	var out wire.Session
	err := c.Do(ctx, http.MethodPut, "/sessions/"+escape(sessionID)+"/project", map[string]string{"project_id": projectID}, &out)
	return &out, err
}

// DeleteSession asks for a session, its transcript and its workspace to be
// removed for good.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.Do(ctx, http.MethodDelete, "/sessions/"+escape(sessionID), nil, nil)
}

// WorkspaceInstructions are the instruction files the session's agent reads.
func (c *Client) WorkspaceInstructions(ctx context.Context, sessionID string) (*wire.WorkspaceInstructions, error) {
	var out wire.WorkspaceInstructions
	err := c.Do(ctx, http.MethodGet, "/sessions/"+escape(sessionID)+"/workspace-instructions", nil, &out)
	return &out, err
}

// WorkspaceUsage is the account's computer time over the last days, by local day.
func (c *Client) WorkspaceUsage(ctx context.Context, days int, timeZone string) (*wire.WorkspaceUsage, error) {
	var out wire.WorkspaceUsage
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/account/billing/usage?days=%d&time_zone=%s", days, escape(timeZone)), nil, &out)
	return &out, err
}

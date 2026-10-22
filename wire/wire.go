// Package wire is the part of the Barad API's JSON that this command line
// reads and writes: the fields it uses, with the server's names. The server
// keeps a test that each of these is a subset of its own types, so a change
// on the server that would break the command line fails there first.
package wire

import (
	"encoding/json"
	"time"
)

type (
	Provider        string // claude, openai, codex, xai, grok
	ReasoningEffort string
	MachineStatus   string
	TurnStatus      string
	ItemKind        string
	EventKind       string
)

const (
	MachineStatusDehydrated  MachineStatus = "dehydrated"
	MachineStatusHydrating   MachineStatus = "hydrating"
	MachineStatusHydrated    MachineStatus = "hydrated"
	MachineStatusDehydrating MachineStatus = "dehydrating"
	MachineStatusBroken      MachineStatus = "broken"
	MachineStatusDeleting    MachineStatus = "deleting"

	TurnStatusActive     TurnStatus = "active"
	TurnStatusCompacting TurnStatus = "compacting"
	TurnStatusCompleted  TurnStatus = "completed"
	TurnStatusFailed     TurnStatus = "failed"
	TurnStatusCancelled  TurnStatus = "cancelled"

	ItemKindUser              ItemKind = "user"
	ItemKindAssistant         ItemKind = "assistant"
	ItemKindReasoning         ItemKind = "reasoning"
	ItemKindToolCall          ItemKind = "tool_call"
	ItemKindToolResult        ItemKind = "tool_result"
	ItemKindContextCompaction ItemKind = "context_compaction"
	ItemKindProviderOutput    ItemKind = "provider_output"

	EventKindModelDelta EventKind = "model_delta"
	EventKindStatus     EventKind = "status"

	// CommandKindNewSession is the tool call that proposes a new session.
	CommandKindNewSession = "new_session"

	DefaultProjectID   = "default"
	DefaultProjectName = "Default"
)

// The API's limits on a message and what goes with it.
const (
	PromptMediaMaximumItems      = 10
	PromptMediaMaximumBytes      = 20 << 20
	PromptMediaMaximumTotalBytes = 50 << 20
	PromptTextMaximumBytes       = 1 << 20
	MessageTextMaximumBytes      = 64 << 10
	PromptTextType               = "text/plain"
)

type TokenUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	ContextTokens int `json:"context_tokens,omitempty"`
}

type Session struct {
	SessionID                 string            `json:"session_id"`
	Name                      string            `json:"name,omitempty"`
	ProviderResourceID        string            `json:"provider_resource_id"`
	Provider                  Provider          `json:"provider"`
	Model                     string            `json:"model"`
	ReasoningEffort           ReasoningEffort   `json:"reasoning_effort,omitempty"`
	FastMode                  bool              `json:"fast_mode"`
	MachineStatus             MachineStatus     `json:"machine_status"`
	PendingTurn               bool              `json:"pending_turn,omitempty"`
	CurrentTurnID             string            `json:"current_turn_id,omitempty"`
	LatestTerminalTurnID      string            `json:"latest_terminal_turn_id,omitempty"`
	LatestTerminalTurnStatus  TurnStatus        `json:"latest_terminal_turn_status,omitempty"`
	LatestTerminalTurnAt      time.Time         `json:"latest_terminal_turn_at,omitzero"`
	LatestUserPromptAt        time.Time         `json:"latest_user_prompt_at,omitzero"`
	ContextWindowTokens       int               `json:"context_window_tokens"`
	SupportsReasoningEffort   bool              `json:"supports_reasoning_effort,omitempty"`
	SupportedReasoningEfforts []ReasoningEffort `json:"supported_reasoning_efforts,omitempty"`
	AutoCompactTokenLimit     int               `json:"auto_compact_token_limit"`
	LatestProviderUsage       TokenUsage        `json:"latest_provider_usage"`
	ProjectID                 string            `json:"project_id"`
	ArchivedAt                time.Time         `json:"archived_at,omitzero"`
	CreatedAt                 time.Time         `json:"created_at"`
	UpdatedAt                 time.Time         `json:"updated_at"`
}

type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Turn struct {
	TurnID          string     `json:"turn_id"`
	Status          TurnStatus `json:"status"`
	CancelRequested bool       `json:"cancel_requested"`
	Failure         *ErrorInfo `json:"failure,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     time.Time  `json:"completed_at,omitempty"`
}

type TranscriptItem struct {
	SessionID  string          `json:"session_id"`
	Sequence   int64           `json:"sequence"`
	TurnID     string          `json:"turn_id,omitempty"`
	StreamID   string          `json:"stream_id,omitempty"`
	Kind       ItemKind        `json:"kind"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Event struct {
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	StreamID  string          `json:"stream_id"`
	Sequence  int64           `json:"sequence"`
	Kind      EventKind       `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type UserPromptPayload struct {
	Text      string           `json:"text,omitempty"`
	RequestID string           `json:"request_id,omitempty"`
	Media     []PromptMediaRef `json:"media,omitempty"`
}

type PromptMediaRef struct {
	MediaID   string `json:"media_id"`
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
}

type PromptMedia struct {
	MediaID string `json:"media_id"`
}

type PresignedHTTPRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
}

type SessionProposalRepository struct {
	Repository   string  `json:"repository"`
	RequestedRef *string `json:"requested_ref"`
}

type SessionProposalVariable struct {
	Name string `json:"name"`
}

type SessionProposal struct {
	Title        string                      `json:"title"`
	Reason       string                      `json:"reason"`
	Prompt       string                      `json:"prompt"`
	Repositories []SessionProposalRepository `json:"repositories"`
	Environment  []SessionProposalVariable   `json:"environment"`
}

type SessionProposalDecision struct {
	Sequence      int64     `json:"sequence"`
	Status        string    `json:"status"`
	DestinationID string    `json:"destination_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type SessionProposalView struct {
	Sequence int64                    `json:"sequence"`
	Proposal SessionProposal          `json:"proposal"`
	Decision *SessionProposalDecision `json:"decision,omitempty"`
}

type MessageAdmission struct {
	Session *Session `json:"session"`
}

type SessionGitRepository struct {
	CanonicalPath string `json:"canonical_path"`
}

type ProjectAgent struct {
	ProviderResourceID string          `json:"provider_resource_id"`
	Model              string          `json:"model"`
	ReasoningEffort    ReasoningEffort `json:"reasoning_effort,omitempty"`
}

type ProjectRepository struct {
	SourceID   string `json:"source_id"`
	Repository string `json:"repository"`
}

type Project struct {
	ProjectID    string              `json:"project_id"`
	Name         string              `json:"name"`
	Agent        *ProjectAgent       `json:"agent,omitempty"`
	Repositories []ProjectRepository `json:"repositories"`
	ClosingAt    time.Time           `json:"closing_at,omitzero"`
}

type ModelCapabilities struct {
	Model                   string            `json:"model"`
	SupportsReasoningEffort bool              `json:"supports_reasoning_effort"`
	ReasoningEfforts        []ReasoningEffort `json:"supported_reasoning_efforts"`
	DefaultReasoningEffort  ReasoningEffort   `json:"reasoning_effort"`
}

// AccountResource is a provider connection or a Git source.
type AccountResource struct {
	ResourceID   string `json:"resource_id"`
	Name         string `json:"name"`
	ProviderType string `json:"provider_type,omitempty"`
	Deleting     bool   `json:"deleting,omitempty"`
}

// ProviderView is a provider connection as the API lists it.
type ProviderView struct {
	AccountResource
	Ready bool `json:"ready"`
}

// UpdatesProtocolVersion is the update socket's protocol.
const UpdatesProtocolVersion = 1

// DirtyMessage is the update socket's message: a hint, a ping or a pong.
type DirtyMessage struct {
	Type      string   `json:"type"`
	Version   int      `json:"version"`
	Sessions  []string `json:"sessions"`
	Summaries []string `json:"summaries"`
}

// Usage.

type CostMetrics struct {
	ProviderReportedNanodollars  int64 `json:"provider_reported_nanodollars"`
	CatalogCalculatedNanodollars int64 `json:"catalog_calculated_nanodollars"`
}

func (c CostMetrics) Nanodollars() int64 {
	return c.ProviderReportedNanodollars + c.CatalogCalculatedNanodollars
}

type UsageMetrics struct {
	InputTokens       int64       `json:"input_tokens"`
	CachedInputTokens int64       `json:"cached_input_tokens"`
	ProcessedTokens   int64       `json:"processed_tokens"`
	ActualCost        CostMetrics `json:"actual_cost"`
	APIEquivalentCost CostMetrics `json:"api_equivalent_cost"`
}

type UsageDay struct {
	Date    string       `json:"date"`
	Metrics UsageMetrics `json:"metrics"`
}

// UsageReport is the account's provider usage by local day.
type UsageReport struct {
	Totals UsageMetrics `json:"totals"`
	Daily  []UsageDay   `json:"daily"`
}

// WorkspaceTime is computer time and its charge.
type WorkspaceTime struct {
	BilledMinutes   int64 `json:"billed_minutes"`
	ChargedUSDNanos int64 `json:"charged_usd_nanos"`
	Runs            int64 `json:"runs"`
}

type WorkspaceDay struct {
	Date string `json:"date"`
	WorkspaceTime
}

// WorkspaceUsage is the account's computer time by local day.
type WorkspaceUsage struct {
	Totals WorkspaceTime  `json:"totals"`
	Daily  []WorkspaceDay `json:"daily"`
}

// Signing in: the command line's device-code flow.

// CLISignInStarted is a new sign-in: the code you approve at VerificationURL,
// and the secret barad polls with.
type CLISignInStarted struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	VerificationURL string `json:"verification_url"`
}

// CLITokens is an approved sign-in: a Cognito session on the command line's
// app client, and where to refresh and revoke it.
type CLITokens struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	ClientID     string `json:"client_id"`
	CognitoURL   string `json:"cognito_url"`
}

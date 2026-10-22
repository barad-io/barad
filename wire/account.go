package wire

import (
	"encoding/json"
	"time"
)

// The account: provider connections, source control, billing.

// ProviderAuthorization is a started subscription sign-in. ChatGPT and Grok
// give a code to enter at VerificationURL; Claude's sign-in page arrives
// with a poll, and gives a code to paste back.
type ProviderAuthorization struct {
	FlowID                  string    `json:"flow_id"`
	ResourceID              string    `json:"resource_id"`
	VerificationURL         string    `json:"verification_url"`
	VerificationURLComplete string    `json:"verification_url_complete,omitempty"`
	UserCode                string    `json:"user_code"`
	ExpiresAt               time.Time `json:"expires_at"`
}

// ProviderAuthorizationPoll is how a sign-in stands.
type ProviderAuthorizationPoll struct {
	VerificationURL string `json:"verification_url,omitempty"` // Claude's sign-in page, once it is ready
	Pending         bool   `json:"pending"`
	Connected       bool   `json:"connected"`
	Failed          bool   `json:"failed"`
}

// ResourceDependency is a session that uses a connection.
type ResourceDependency struct {
	SessionID  string    `json:"session_id"`
	AttachedAt time.Time `json:"attached_at"`
}

// GitHubAccount is who the account signed in to GitHub as, and the app's
// installations that person can connect.
type GitHubAccount struct {
	SignedIn      bool                 `json:"signed_in"`
	Login         string               `json:"login,omitempty"` // set when signed out but known: sign in again
	Installations []GitHubInstallation `json:"installations"`
}

type GitHubInstallation struct {
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
	AccountType    string `json:"account_type"`
	AccountURL     string `json:"account_url"`
	SourceID       string `json:"source_id,omitempty"` // the Git source it is connected as
}

// GitHubLink is a page on GitHub to finish a step at: signing in, or
// installing the app on an account or organization.
type GitHubLink struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GitHubRepository is a repository a Git source reaches.
type GitHubRepository struct {
	ID            string `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// GitSettings is who sessions' commits are by.
type GitSettings struct {
	UserName           string `json:"user_name"`
	HumanEmail         string `json:"human_email"`
	SeparateIdentities bool   `json:"separate_identities"`
	BaradIsAuthor      bool   `json:"barad_is_author"`
}

// BillingAccount is the credit balance and the terms it is bought under.
type BillingAccount struct {
	Currency                     string `json:"currency"`
	BalanceUSDNanos              int64  `json:"balance_usd_nanos"`
	WorkspaceRateUSDNanosPerHour int64  `json:"workspace_rate_usd_nanos_per_hour"`
	TermsVersion                 string `json:"terms_version"`
	TermsURL                     string `json:"terms_url"`
	TermsAccepted                bool   `json:"terms_accepted"`
	MinimumPurchaseUSDCents      int64  `json:"minimum_purchase_usd_cents"`
	MaximumPurchaseUSDCents      int64  `json:"maximum_purchase_usd_cents"`
}

// BillingEntry is one change to the balance: a purchase, a grant, a
// workspace's running time.
type BillingEntry struct {
	OperationID    string    `json:"operation_id"`
	Kind           string    `json:"kind"`
	AmountUSDNanos int64     `json:"amount_usd_nanos"`
	SessionID      string    `json:"session_id,omitempty"`
	EffectiveAt    time.Time `json:"effective_at"`
	RecordedAt     time.Time `json:"recorded_at"`
}

type BillingHistory struct {
	Entries []BillingEntry `json:"entries"`
	Cursor  string         `json:"cursor,omitempty"`
}

// Checkout is a payment page for credits.
type Checkout struct {
	PurchaseID string `json:"purchase_id"`
	URL        string `json:"url"`
}

// Owner commands: what the web app's Files panel asks a session's computer.

// OwnerCommand is a command's state; Result's shape is its kind's.
type OwnerCommand struct {
	OwnerRequestID string                `json:"owner_request_id"`
	Kind           string                `json:"kind"`
	Status         string                `json:"status"` // queued, running, completed, failed, stale
	Result         json.RawMessage       `json:"result,omitempty"`
	Error          *ErrorInfo            `json:"error,omitempty"`
	Download       *OwnerCommandDownload `json:"download,omitempty"`
}

// OwnerCommandDownload is the object a command left: a file's range, or a
// zip of files.
type OwnerCommandDownload struct {
	URL       string    `json:"url"`
	Filename  string    `json:"filename"`
	Bytes     int64     `json:"bytes"`
	ExpiresAt time.Time `json:"expires_at"`
}

// FileEntry is a name in a workspace directory.
type FileEntry struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // file, directory, symlink
	Size   int64  `json:"size,omitempty"`
	Target string `json:"target,omitempty"`
}

type FileList struct {
	Entries    []FileEntry `json:"entries"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// FileRange is a range of a file; its bytes are the command's download.
type FileRange struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
	Size   int64 `json:"size"`
	EOF    bool  `json:"eof"`
}

// FileUploads is a reservation for files to upload: PUT each to its
// request, then run fs_upload with the arguments.
type FileUploads struct {
	Arguments json.RawMessage        `json:"arguments"`
	Uploads   []PresignedHTTPRequest `json:"uploads"`
}

// The API's limits on a file transfer.
const (
	FileTransferMaximumFiles       = 32
	FileTransferMaximumBytes int64 = 512 << 20
	FileReadMaximumBytes     int64 = 16 << 20
)

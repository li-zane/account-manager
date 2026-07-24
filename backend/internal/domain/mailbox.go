package domain

import (
	"encoding/json"
	"time"
)

type ProviderKey string

const (
	ProviderMicrosoft       ProviderKey = "microsoft"
	ProviderGmail           ProviderKey = "gmail"
	ProviderCloudflareRoute ProviderKey = "cloudflare_route"
)

type MailboxStatus string

const (
	MailboxStatusActive   MailboxStatus = "active"
	MailboxStatusDisabled MailboxStatus = "disabled"
	MailboxStatusError    MailboxStatus = "error"
)

type Mailbox struct {
	ID                string          `json:"id"`
	Provider          ProviderKey     `json:"provider"`
	Address           string          `json:"address"`
	NormalizedAddress string          `json:"normalized_address"`
	DisplayName       string          `json:"display_name,omitempty"`
	ExternalReference string          `json:"external_reference,omitempty"`
	Status            MailboxStatus   `json:"status"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type AliasKind string

const (
	AliasKindSplit   AliasKind = "split"
	AliasKindForward AliasKind = "forward"
)

// MailboxAlias belongs to the destination/parent mailbox. Provider describes
// where the alias is hosted, so a Cloudflare address can route to a Gmail box.
type MailboxAlias struct {
	ID                string          `json:"id"`
	MailboxID         string          `json:"mailbox_id"`
	Provider          ProviderKey     `json:"provider"`
	Address           string          `json:"address"`
	NormalizedAddress string          `json:"normalized_address"`
	Kind              AliasKind       `json:"kind"`
	Enabled           bool            `json:"enabled"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type CredentialKind string

const (
	CredentialMicrosoftGraphOAuth CredentialKind = "microsoft_graph_oauth"
	CredentialMicrosoftIMAPOAuth  CredentialKind = "microsoft_imap_oauth"
	CredentialMicrosoftDualToken  CredentialKind = "microsoft_dual_token"
	CredentialGmailOAuth          CredentialKind = "gmail_oauth"
	CredentialIMAPPassword        CredentialKind = "imap_password"
)

// MailboxCredential never exposes plaintext. EncryptedSecret is produced and
// consumed through ports.SecretBroker and is excluded from JSON responses.
type MailboxCredential struct {
	ID               string          `json:"id"`
	MailboxID        string          `json:"mailbox_id"`
	Kind             CredentialKind  `json:"kind"`
	ClientID         string          `json:"client_id,omitempty"`
	EncryptedSecret  []byte          `json:"-"`
	KeyVersion       string          `json:"key_version"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	RefreshAfter     *time.Time      `json:"refresh_after,omitempty"`
	RefreshStatus    string          `json:"refresh_status,omitempty"`
	LastRefreshedAt  *time.Time      `json:"last_refreshed_at,omitempty"`
	LastRefreshError string          `json:"last_refresh_error,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Version          int64           `json:"version"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type PlatformAccount struct {
	ID                string          `json:"id"`
	Platform          string          `json:"platform"`
	ExternalReference string          `json:"external_reference"`
	MailboxID         string          `json:"mailbox_id,omitempty"`
	MailboxAliasID    *string         `json:"mailbox_alias_id,omitempty"`
	LoginAddress      string          `json:"login_address"`
	Status            string          `json:"status"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	Routes            []MailboxRoute  `json:"mailbox_routes,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// MailboxRoute is a value relation rather than ownership. A platform account
// may be created before a mailbox is connected and may use multiple primary or
// split addresses over its lifetime.
type MailboxRoute struct {
	ID        string `json:"id,omitempty"`
	MailboxID string `json:"mailbox_id,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
	Role      string `json:"role"`
	Address   string `json:"address"`
}

// MailboxPickupKey stores only the keyed digest and a display prefix. The raw
// token is returned once by the service and has no persistence field.
type MailboxPickupKey struct {
	ID        string     `json:"id"`
	MailboxID string     `json:"mailbox_id"`
	Digest    []byte     `json:"-"`
	Prefix    string     `json:"prefix"`
	Label     string     `json:"label,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

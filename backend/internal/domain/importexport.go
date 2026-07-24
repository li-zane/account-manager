package domain

import (
	"encoding/json"
	"time"
)

type MailboxFormatKind string

const (
	MailboxFormatDelimited MailboxFormatKind = "delimited"
	MailboxFormatTemplate  MailboxFormatKind = "template"
	MailboxFormatJSON      MailboxFormatKind = "json"
)

type MailboxFormatDirection string

const (
	MailboxFormatImport MailboxFormatDirection = "import"
	MailboxFormatExport MailboxFormatDirection = "export"
	MailboxFormatBoth   MailboxFormatDirection = "both"
)

// MailboxFormatField keeps the external column and canonical field mapping.
// Field order is preserved for headerless imports and every export.
type MailboxFormatField struct {
	Column    string `json:"column"`
	Target    string `json:"target"`
	Required  bool   `json:"required,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
	Default   string `json:"default,omitempty"`
}

type MailboxFormat struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Kind         MailboxFormatKind      `json:"kind"`
	Direction    MailboxFormatDirection `json:"direction"`
	Delimiter    string                 `json:"delimiter"`
	Fields       []MailboxFormatField   `json:"fields"`
	Provider     *ProviderKey           `json:"provider,omitempty"`
	HasHeader    bool                   `json:"has_header"`
	Template     string                 `json:"template,omitempty"`
	ParserConfig json.RawMessage        `json:"parser_config,omitempty"`
	Builtin      bool                   `json:"builtin"`
	Enabled      bool                   `json:"enabled"`
	Version      int64                  `json:"version"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

type ConflictStrategy string

const (
	ConflictSkip   ConflictStrategy = "skip"
	ConflictUpdate ConflictStrategy = "update"
	ConflictError  ConflictStrategy = "error"
)

// MailboxImportItem crosses the repository boundary only after secrets are
// sealed, preventing plaintext tokens from reaching persistence adapters.
type MailboxImportItem struct {
	Mailbox            Mailbox
	Credential         *MailboxCredential
	PickupKey          *MailboxPickupKey
	PlatformAccount    *PlatformAccount
	PlatformCredential *PlatformAccountCredential
}

type MailboxImportResult struct {
	Created    int      `json:"created"`
	Updated    int      `json:"updated"`
	Skipped    int      `json:"skipped"`
	MailboxIDs []string `json:"mailbox_ids"`
}

type MailboxCredentialSecret struct {
	ClientID     string `json:"client_id,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	UseTLS       *bool  `json:"use_tls,omitempty"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	InboxFolder  string `json:"inbox_folder,omitempty"`
	JunkFolder   string `json:"junk_folder,omitempty"`
}

type PlatformAccountCredential struct {
	ID                string          `json:"id"`
	PlatformAccountID string          `json:"platform_account_id"`
	Kind              string          `json:"kind"`
	EncryptedSecret   []byte          `json:"-"`
	KeyVersion        string          `json:"key_version"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type PlatformAccountCredentialSecret struct {
	Password    string `json:"password,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
}

package domain

import (
	"encoding/json"
	"time"
)

type RetrievalMethod string

const (
	RetrievalMicrosoftGraph RetrievalMethod = "microsoft_graph"
	RetrievalOutlookREST    RetrievalMethod = "outlook_rest"
	RetrievalIMAPOAuth      RetrievalMethod = "imap_oauth"
	RetrievalIMAPPassword   RetrievalMethod = "imap_password"
	RetrievalDualToken      RetrievalMethod = "dual_token"
	RetrievalGmailAPI       RetrievalMethod = "gmail_api"
	RetrievalForwarded      RetrievalMethod = "forwarded_mailbox"
)

type ProviderCapabilities struct {
	ProvisionMailbox bool              `json:"provision_mailbox"`
	ManageAliases    bool              `json:"manage_aliases"`
	Forwarding       bool              `json:"forwarding"`
	RefreshTokens    bool              `json:"refresh_tokens"`
	RetrievalMethods []RetrievalMethod `json:"retrieval_methods"`
}

type ProviderDescriptor struct {
	Key          ProviderKey          `json:"key"`
	DisplayName  string               `json:"display_name"`
	Capabilities ProviderCapabilities `json:"capabilities"`
	Configured   bool                 `json:"configured"`
}

// ProviderConnection stores an encrypted provider configuration independently
// from mailboxes. EncryptedConfig and its key version are persistence details
// and must never be serialized into API responses.
type ProviderConnection struct {
	ID              string               `json:"id"`
	Provider        ProviderKey          `json:"provider"`
	Name            string               `json:"name"`
	EncryptedConfig []byte               `json:"-"`
	KeyVersion      string               `json:"-"`
	Enabled         bool                 `json:"enabled"`
	Capabilities    ProviderCapabilities `json:"capabilities"`
	Metadata        json.RawMessage      `json:"metadata,omitempty"`
	Version         int64                `json:"version"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type ProvisionMailboxRequest struct {
	Address  string
	Metadata json.RawMessage
}

type ProvisionMailboxResult struct {
	ExternalReference string
	Metadata          json.RawMessage
}

type MessageFolder string

const (
	MessageFolderInbox MessageFolder = "INBOX"
	MessageFolderJunk  MessageFolder = "Junk"
)

type MessageQuery struct {
	After            *time.Time
	Limit            int
	Unread           bool
	Folder           MessageFolder
	RecipientAddress string
	RetrievalMethod  RetrievalMethod
	PageSize         int
	MaxPages         int
}

type Message struct {
	ID                 string              `json:"id"`
	InternetMessageID  string              `json:"internet_message_id,omitempty"`
	From               string              `json:"from"`
	To                 []string            `json:"to"`
	Cc                 []string            `json:"cc,omitempty"`
	RecipientAddresses []string            `json:"recipient_addresses,omitempty"`
	Subject            string              `json:"subject"`
	Text               string              `json:"text,omitempty"`
	HTML               string              `json:"html,omitempty"`
	ReceivedAt         time.Time           `json:"received_at"`
	Unread             bool                `json:"unread"`
	Headers            map[string][]string `json:"headers,omitempty"`
}

type RefreshedCredential struct {
	EncryptedSecret []byte     `json:"-"`
	KeyVersion      string     `json:"key_version"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	RefreshAfter    *time.Time `json:"refresh_after,omitempty"`
	// PersistOnError marks EncryptedSecret as a valid checkpoint produced
	// before a later refresh step failed. Callers must persist that checkpoint
	// together with the failed refresh state before returning the error.
	PersistOnError bool `json:"-"`
}

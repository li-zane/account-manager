package domain

import (
	"encoding/json"
	"time"
)

type CachedMessage struct {
	ID                 string              `json:"id"`
	MailboxID          string              `json:"mailbox_id"`
	ExternalID         string              `json:"-"`
	ProviderMessageID  string              `json:"provider_message_id"`
	RetrievalMethod    RetrievalMethod     `json:"retrieval_method,omitempty"`
	InternetMessageID  string              `json:"internet_message_id,omitempty"`
	Folder             MessageFolder       `json:"folder"`
	From               string              `json:"from"`
	To                 []string            `json:"to"`
	Cc                 []string            `json:"cc,omitempty"`
	RecipientAddresses []string            `json:"recipient_addresses,omitempty"`
	Subject            string              `json:"subject"`
	Text               string              `json:"text,omitempty"`
	HTML               string              `json:"html,omitempty"`
	ReceivedAt         time.Time           `json:"received_at"`
	Unread             bool                `json:"unread"`
	ViewedAt           *time.Time          `json:"viewed_at,omitempty"`
	Headers            map[string][]string `json:"headers,omitempty"`
	DiscoveredAt       time.Time           `json:"discovered_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

type MessageSyncState struct {
	TargetID        string          `json:"target_id"`
	MailboxID       string          `json:"mailbox_id"`
	AliasID         string          `json:"alias_id,omitempty"`
	Folder          MessageFolder   `json:"folder"`
	LastMessageAt   *time.Time      `json:"last_message_at,omitempty"`
	LastSyncedAt    time.Time       `json:"last_synced_at"`
	LastError       string          `json:"last_error,omitempty"`
	RetrievalMethod RetrievalMethod `json:"retrieval_method,omitempty"`
	Cursor          string          `json:"cursor,omitempty"`
	UIDValidity     uint32          `json:"uid_validity,omitempty"`
	HighestUID      uint32          `json:"highest_uid,omitempty"`
}

type RetrievalCapabilityStatus string

const (
	RetrievalCapabilityPending     RetrievalCapabilityStatus = "pending"
	RetrievalCapabilityAvailable   RetrievalCapabilityStatus = "available"
	RetrievalCapabilityUnavailable RetrievalCapabilityStatus = "unavailable"
	RetrievalCapabilityError       RetrievalCapabilityStatus = "error"
)

type MailboxRetrievalCapability struct {
	MailboxID      string                    `json:"mailbox_id"`
	Method         RetrievalMethod           `json:"method"`
	Status         RetrievalCapabilityStatus `json:"status"`
	Preferred      bool                      `json:"preferred"`
	TokenExpiresAt *time.Time                `json:"token_expires_at,omitempty"`
	CheckedAt      *time.Time                `json:"checked_at,omitempty"`
	ErrorCode      string                    `json:"error_code,omitempty"`
	ErrorMessage   string                    `json:"error_message,omitempty"`
}

func CloneMessageHeaders(value map[string][]string) map[string][]string {
	if value == nil {
		return nil
	}
	result := make(map[string][]string, len(value))
	for key, fields := range value {
		result[key] = append([]string(nil), fields...)
	}
	return result
}

func MessageHeadersJSON(value map[string][]string) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

package domain

import "time"

const MicrosoftCredentialSecretVersion = 1

// MicrosoftCredentialSecret is the JSON envelope stored inside
// MailboxCredential.EncryptedSecret. It is never an API response type.
// AccessTokenMethod must be present before AccessToken is accepted for mail
// retrieval, which prevents a legacy platform-account token from being treated
// as a Microsoft mailbox token.
type MicrosoftCredentialSecret struct {
	SchemaVersion int    `json:"schema_version"`
	ClientID      string `json:"client_id"`
	Password      string `json:"password,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	// Deprecated compatibility fields are read from older encrypted payloads.
	// New writes keep the shared Microsoft RT in RefreshToken only.
	GraphRefreshToken    string          `json:"graph_refresh_token,omitempty"`
	IMAPRefreshToken     string          `json:"imap_refresh_token,omitempty"`
	AccessToken          string          `json:"access_token,omitempty"`
	AccessTokenMethod    RetrievalMethod `json:"access_token_method,omitempty"`
	AccessTokenExpiresAt *time.Time      `json:"access_token_expires_at,omitempty"`
	GraphAccessToken     string          `json:"graph_access_token,omitempty"`
	GraphTokenExpiresAt  *time.Time      `json:"graph_access_token_expires_at,omitempty"`
	IMAPAccessToken      string          `json:"imap_access_token,omitempty"`
	IMAPTokenExpiresAt   *time.Time      `json:"imap_access_token_expires_at,omitempty"`
	GraphScope           string          `json:"graph_scope,omitempty"`
	IMAPScope            string          `json:"imap_scope,omitempty"`
	IMAPUsername         string          `json:"imap_username,omitempty"`
	IMAPHost             string          `json:"imap_host,omitempty"`
	IMAPPort             int             `json:"imap_port,omitempty"`
	IMAPProxyURL         string          `json:"imap_proxy_url,omitempty"`
	IMAPInboxFolder      string          `json:"imap_inbox_folder,omitempty"`
	IMAPJunkFolder       string          `json:"imap_junk_folder,omitempty"`
	TokenType            string          `json:"token_type,omitempty"`
}

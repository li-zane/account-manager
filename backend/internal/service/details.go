package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type MailboxDetailService struct {
	mailboxes ports.MailboxRepository
	accounts  ports.PlatformAccountRepository
	secrets   ports.SecretBroker
	settings  CredentialRefreshSettingsReader
	clock     func() time.Time
}

func NewMailboxDetailService(mailboxes ports.MailboxRepository, accounts ports.PlatformAccountRepository, secrets ports.SecretBroker) (*MailboxDetailService, error) {
	if mailboxes == nil || accounts == nil || secrets == nil {
		return nil, fmt.Errorf("%w: mailbox detail dependencies are required", domain.ErrInvalid)
	}
	return &MailboxDetailService{mailboxes: mailboxes, accounts: accounts, secrets: secrets, clock: time.Now}, nil
}

func (s *MailboxDetailService) SetSettingsReader(settings CredentialRefreshSettingsReader) {
	s.settings = settings
}

type CredentialSummary struct {
	CredentialType       domain.CredentialKind    `json:"credential_type"`
	ClientID             string                   `json:"client_id,omitempty"`
	RetrievalMethods     []domain.RetrievalMethod `json:"retrieval_methods,omitempty"`
	RefreshToken         string                   `json:"refresh_token,omitempty"`
	HasRefreshToken      bool                     `json:"has_refresh_token"`
	GraphRefreshToken    string                   `json:"graph_refresh_token,omitempty"`
	HasGraphRefreshToken bool                     `json:"has_graph_refresh_token"`
	IMAPRefreshToken     string                   `json:"imap_refresh_token,omitempty"`
	HasIMAPRefreshToken  bool                     `json:"has_imap_refresh_token"`
	ExpiresAt            *time.Time               `json:"expires_at,omitempty"`
	GraphTokenExpiresAt  *time.Time               `json:"graph_token_expires_at,omitempty"`
	IMAPTokenExpiresAt   *time.Time               `json:"imap_token_expires_at,omitempty"`
	RefreshAfter         *time.Time               `json:"refresh_after,omitempty"`
	RefreshStatus        string                   `json:"refresh_status"`
	LastRefreshedAt      *time.Time               `json:"last_refreshed_at,omitempty"`
	LastRefreshError     string                   `json:"last_refresh_error,omitempty"`
	AutoRefresh          bool                     `json:"auto_refresh"`
}

type MailboxDetail struct {
	ID               string                   `json:"id"`
	Provider         domain.ProviderKey       `json:"provider"`
	Address          string                   `json:"address"`
	DisplayName      string                   `json:"display_name,omitempty"`
	Status           domain.MailboxStatus     `json:"status"`
	ClientID         string                   `json:"client_id,omitempty"`
	CredentialType   domain.CredentialKind    `json:"credential_type,omitempty"`
	ExpiresAt        *time.Time               `json:"expires_at,omitempty"`
	RefreshStatus    string                   `json:"refresh_status,omitempty"`
	Credentials      []CredentialSummary      `json:"credentials"`
	Aliases          []domain.MailboxAlias    `json:"aliases"`
	Accounts         []domain.PlatformAccount `json:"accounts"`
	MainMailboxCount int                      `json:"main_mailbox_count"`
	AliasCount       int                      `json:"alias_count"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
}

func (s *MailboxDetailService) Get(ctx context.Context, mailboxID string) (MailboxDetail, error) {
	mailbox, err := s.mailboxes.GetMailbox(ctx, mailboxID)
	if err != nil {
		return MailboxDetail{}, err
	}
	aliases, err := s.mailboxes.ListAliases(ctx, mailboxID, ports.ListOptions{Limit: 500})
	if err != nil {
		return MailboxDetail{}, err
	}
	credentials, err := s.mailboxes.ListCredentials(ctx, mailboxID)
	if err != nil {
		return MailboxDetail{}, err
	}
	accounts, err := s.accounts.ListPlatformAccountsByMailbox(ctx, mailboxID, ports.ListOptions{Limit: 500})
	if err != nil {
		return MailboxDetail{}, err
	}
	autoRefresh := false
	if s.settings != nil {
		settings, err := s.settings.Get(ctx)
		if err != nil {
			return MailboxDetail{}, err
		}
		autoRefresh = settings.Enabled
	}
	summaries := make([]CredentialSummary, 0, len(credentials))
	for _, credential := range credentials {
		summaries = append(summaries, s.summarizeCredential(ctx, credential, autoRefresh))
	}
	detail := MailboxDetail{
		ID: mailbox.ID, Provider: mailbox.Provider, Address: mailbox.Address,
		DisplayName: mailbox.DisplayName, Status: mailbox.Status, Credentials: summaries,
		Aliases: aliases, Accounts: accounts, MainMailboxCount: 1, AliasCount: len(aliases),
		CreatedAt: mailbox.CreatedAt, UpdatedAt: mailbox.UpdatedAt,
	}
	if len(summaries) > 0 {
		primary := summaries[0]
		detail.ClientID = primary.ClientID
		detail.CredentialType = primary.CredentialType
		detail.ExpiresAt = primary.ExpiresAt
		detail.RefreshStatus = primary.RefreshStatus
	}
	return detail, nil
}

func (s *MailboxDetailService) summarizeCredential(ctx context.Context, credential domain.MailboxCredential, autoRefresh bool) CredentialSummary {
	summary := CredentialSummary{
		CredentialType: credential.Kind, ClientID: credential.ClientID,
		RetrievalMethods: credentialRetrievalMethods(credential.Kind),
		ExpiresAt:        credential.ExpiresAt, RefreshAfter: credential.RefreshAfter,
		RefreshStatus: credential.RefreshStatus, LastRefreshedAt: credential.LastRefreshedAt,
		LastRefreshError: credential.LastRefreshError,
		AutoRefresh:      autoRefresh && refreshableCredential(credential.Kind),
	}
	secret, err := s.openMailboxSecret(ctx, credential)
	if err == nil {
		if summary.ClientID == "" {
			summary.ClientID = secret.ClientID
		}
		summary.GraphTokenExpiresAt = secret.GraphTokenExpiresAt
		summary.IMAPTokenExpiresAt = secret.IMAPTokenExpiresAt
		summary.ExpiresAt = credentialSummaryExpiry(credential, secret)
		summary.HasGraphRefreshToken = secret.GraphRefreshToken != ""
		summary.HasIMAPRefreshToken = secret.IMAPRefreshToken != ""
		if summary.HasGraphRefreshToken {
			summary.GraphRefreshToken = maskedCredentialValue
		}
		if summary.HasIMAPRefreshToken {
			summary.IMAPRefreshToken = maskedCredentialValue
		}
		if primaryRefreshToken(credential.Kind, secret) != "" {
			summary.HasRefreshToken = true
			summary.RefreshToken = maskedCredentialValue
		}
	}
	if summary.RefreshStatus == "" || summary.RefreshStatus == "unknown" {
		summary.RefreshStatus = derivedRefreshStatus(s.clock().UTC(), credential, refreshCredentialComplete(credential.Kind, secret), err)
	}
	return summary
}

type RevealedCredential struct {
	ClientID            string                   `json:"client_id,omitempty"`
	RefreshToken        string                   `json:"refresh_token"`
	GraphRefreshToken   string                   `json:"graph_refresh_token,omitempty"`
	IMAPRefreshToken    string                   `json:"imap_refresh_token,omitempty"`
	CredentialType      domain.CredentialKind    `json:"credential_type"`
	RetrievalMethods    []domain.RetrievalMethod `json:"retrieval_methods,omitempty"`
	ExpiresAt           *time.Time               `json:"expires_at,omitempty"`
	GraphTokenExpiresAt *time.Time               `json:"graph_token_expires_at,omitempty"`
	IMAPTokenExpiresAt  *time.Time               `json:"imap_token_expires_at,omitempty"`
	RevealedUntil       time.Time                `json:"revealed_until"`
}

func (s *MailboxDetailService) Reveal(ctx context.Context, mailboxID string, kind domain.CredentialKind) (RevealedCredential, error) {
	if _, err := s.mailboxes.GetMailbox(ctx, mailboxID); err != nil {
		return RevealedCredential{}, err
	}
	var credential domain.MailboxCredential
	var err error
	if kind != "" {
		credential, err = s.mailboxes.GetCredential(ctx, mailboxID, kind)
	} else {
		var credentials []domain.MailboxCredential
		credentials, err = s.mailboxes.ListCredentials(ctx, mailboxID)
		if err == nil {
			for _, candidate := range credentials {
				secret, openErr := s.openMailboxSecret(ctx, candidate)
				if openErr == nil && primaryRefreshToken(candidate.Kind, secret) != "" {
					credential = candidate
					break
				}
			}
			if credential.ID == "" {
				err = fmt.Errorf("%w: refresh token credential", domain.ErrNotFound)
			}
		}
	}
	if err != nil {
		return RevealedCredential{}, err
	}
	secret, err := s.openMailboxSecret(ctx, credential)
	if err != nil {
		return RevealedCredential{}, err
	}
	refreshToken := primaryRefreshToken(credential.Kind, secret)
	if refreshToken == "" {
		return RevealedCredential{}, fmt.Errorf("%w: credential has no refresh token", domain.ErrNotFound)
	}
	clientID := credential.ClientID
	if clientID == "" {
		clientID = secret.ClientID
	}
	return RevealedCredential{
		ClientID: clientID, RefreshToken: refreshToken,
		GraphRefreshToken: secret.GraphRefreshToken, IMAPRefreshToken: secret.IMAPRefreshToken,
		CredentialType: credential.Kind, RetrievalMethods: credentialRetrievalMethods(credential.Kind),
		ExpiresAt:           credentialSummaryExpiry(credential, secret),
		GraphTokenExpiresAt: secret.GraphTokenExpiresAt, IMAPTokenExpiresAt: secret.IMAPTokenExpiresAt,
		RevealedUntil: s.clock().UTC().Add(60 * time.Second),
	}, nil
}

const maskedCredentialValue = "********"

type openedMailboxCredential struct {
	ClientID            string
	RefreshToken        string
	GraphRefreshToken   string
	IMAPRefreshToken    string
	GraphTokenExpiresAt *time.Time
	IMAPTokenExpiresAt  *time.Time
}

func (s *MailboxDetailService) openMailboxSecret(ctx context.Context, credential domain.MailboxCredential) (openedMailboxCredential, error) {
	if len(credential.EncryptedSecret) == 0 {
		return openedMailboxCredential{}, fmt.Errorf("%w: credential secret", domain.ErrNotFound)
	}
	plaintext, err := s.secrets.Open(ctx, credential.EncryptedSecret, credential.KeyVersion)
	if err != nil {
		return openedMailboxCredential{}, err
	}
	defer clearCredentialBytes(plaintext)
	var generic domain.MailboxCredentialSecret
	if err := json.Unmarshal(plaintext, &generic); err != nil {
		return openedMailboxCredential{}, fmt.Errorf("%w: credential secret payload", domain.ErrInvalid)
	}
	secret := openedMailboxCredential{ClientID: generic.ClientID, RefreshToken: generic.RefreshToken}
	switch credential.Kind {
	case domain.CredentialMicrosoftGraphOAuth, domain.CredentialMicrosoftIMAPOAuth, domain.CredentialMicrosoftDualToken:
		var microsoft domain.MicrosoftCredentialSecret
		if err := json.Unmarshal(plaintext, &microsoft); err != nil {
			return openedMailboxCredential{}, fmt.Errorf("%w: Microsoft credential secret payload", domain.ErrInvalid)
		}
		if microsoft.ClientID != "" {
			secret.ClientID = microsoft.ClientID
		}
		secret.RefreshToken = microsoft.RefreshToken
		switch credential.Kind {
		case domain.CredentialMicrosoftGraphOAuth:
			secret.GraphRefreshToken = firstCredentialValue(microsoft.GraphRefreshToken, microsoft.RefreshToken)
			secret.GraphTokenExpiresAt = microsoftTokenExpiry(microsoft.GraphTokenExpiresAt, microsoft, domain.RetrievalMicrosoftGraph, credential.ExpiresAt)
		case domain.CredentialMicrosoftIMAPOAuth:
			secret.IMAPRefreshToken = firstCredentialValue(microsoft.IMAPRefreshToken, microsoft.RefreshToken)
			secret.IMAPTokenExpiresAt = microsoftTokenExpiry(microsoft.IMAPTokenExpiresAt, microsoft, domain.RetrievalIMAPOAuth, credential.ExpiresAt)
		case domain.CredentialMicrosoftDualToken:
			secret.GraphRefreshToken = firstCredentialValue(microsoft.GraphRefreshToken, microsoft.RefreshToken)
			secret.IMAPRefreshToken = firstCredentialValue(microsoft.IMAPRefreshToken, microsoft.RefreshToken)
			secret.GraphTokenExpiresAt = microsoftTokenExpiry(microsoft.GraphTokenExpiresAt, microsoft, domain.RetrievalMicrosoftGraph, credential.ExpiresAt)
			secret.IMAPTokenExpiresAt = microsoftTokenExpiry(microsoft.IMAPTokenExpiresAt, microsoft, domain.RetrievalIMAPOAuth, credential.ExpiresAt)
		}
	}
	return secret, nil
}

func credentialRetrievalMethods(kind domain.CredentialKind) []domain.RetrievalMethod {
	switch kind {
	case domain.CredentialMicrosoftGraphOAuth:
		return []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph}
	case domain.CredentialMicrosoftIMAPOAuth:
		return []domain.RetrievalMethod{domain.RetrievalIMAPOAuth}
	case domain.CredentialMicrosoftDualToken:
		return []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth}
	case domain.CredentialGmailOAuth:
		return []domain.RetrievalMethod{domain.RetrievalGmailAPI}
	case domain.CredentialIMAPPassword:
		return []domain.RetrievalMethod{domain.RetrievalIMAPPassword}
	default:
		return nil
	}
}

func primaryRefreshToken(kind domain.CredentialKind, secret openedMailboxCredential) string {
	switch kind {
	case domain.CredentialMicrosoftGraphOAuth, domain.CredentialMicrosoftDualToken:
		return firstCredentialValue(secret.GraphRefreshToken, secret.IMAPRefreshToken, secret.RefreshToken)
	case domain.CredentialMicrosoftIMAPOAuth:
		return firstCredentialValue(secret.IMAPRefreshToken, secret.RefreshToken)
	default:
		return secret.RefreshToken
	}
}

func refreshCredentialComplete(kind domain.CredentialKind, secret openedMailboxCredential) bool {
	switch kind {
	case domain.CredentialMicrosoftGraphOAuth:
		return secret.GraphRefreshToken != ""
	case domain.CredentialMicrosoftIMAPOAuth:
		return secret.IMAPRefreshToken != ""
	case domain.CredentialMicrosoftDualToken:
		return secret.GraphRefreshToken != "" && secret.IMAPRefreshToken != ""
	case domain.CredentialGmailOAuth:
		return secret.RefreshToken != ""
	default:
		return false
	}
}

func credentialSummaryExpiry(credential domain.MailboxCredential, secret openedMailboxCredential) *time.Time {
	switch credential.Kind {
	case domain.CredentialMicrosoftGraphOAuth:
		return firstCredentialTime(secret.GraphTokenExpiresAt, credential.ExpiresAt)
	case domain.CredentialMicrosoftIMAPOAuth:
		return firstCredentialTime(secret.IMAPTokenExpiresAt, credential.ExpiresAt)
	case domain.CredentialMicrosoftDualToken:
		return firstCredentialTime(earliestCredentialTime(secret.GraphTokenExpiresAt, secret.IMAPTokenExpiresAt), credential.ExpiresAt)
	default:
		return credential.ExpiresAt
	}
}

func microsoftTokenExpiry(specific *time.Time, secret domain.MicrosoftCredentialSecret, method domain.RetrievalMethod, fallback *time.Time) *time.Time {
	if specific != nil {
		return copyCredentialTime(specific)
	}
	if secret.AccessTokenMethod == method && secret.AccessTokenExpiresAt != nil {
		return copyCredentialTime(secret.AccessTokenExpiresAt)
	}
	return copyCredentialTime(fallback)
}

func firstCredentialValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstCredentialTime(values ...*time.Time) *time.Time {
	for _, value := range values {
		if value != nil {
			return copyCredentialTime(value)
		}
	}
	return nil
}

func earliestCredentialTime(values ...*time.Time) *time.Time {
	var earliest *time.Time
	for _, value := range values {
		if value == nil {
			continue
		}
		if earliest == nil || value.Before(*earliest) {
			earliest = copyCredentialTime(value)
		}
	}
	return earliest
}

func copyCredentialTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func clearCredentialBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func derivedRefreshStatus(now time.Time, credential domain.MailboxCredential, hasRefreshToken bool, openErr error) string {
	if openErr != nil && !errors.Is(openErr, domain.ErrNotFound) {
		return "unreadable"
	}
	if credential.LastRefreshError != "" {
		return "error"
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(now) {
		return "expired"
	}
	if credential.RefreshAfter != nil && !credential.RefreshAfter.After(now) {
		return "due"
	}
	if hasRefreshToken {
		return "active"
	}
	return "missing"
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type MailboxDetailService struct {
	mailboxes    ports.MailboxRepository
	accounts     ports.PlatformAccountRepository
	secrets      ports.SecretBroker
	settings     CredentialRefreshSettingsReader
	capabilities ports.RetrievalCapabilityRepository
	clock        func() time.Time
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

func (s *MailboxDetailService) SetCapabilityRepository(repository ports.RetrievalCapabilityRepository) {
	s.capabilities = repository
}

type CredentialSummary struct {
	CredentialType        domain.CredentialKind        `json:"credential_type"`
	ClientID              string                       `json:"client_id,omitempty"`
	RetrievalMethods      []domain.RetrievalMethod     `json:"retrieval_methods,omitempty"`
	RetrievalCapabilities []RetrievalCapabilitySummary `json:"retrieval_capabilities,omitempty"`
	RefreshToken          string                       `json:"refresh_token,omitempty"`
	HasRefreshToken       bool                         `json:"has_refresh_token"`
	RefreshTokenValidity  string                       `json:"refresh_token_validity,omitempty"`
	ExpiresAt             *time.Time                   `json:"expires_at,omitempty"`
	GraphTokenExpiresAt   *time.Time                   `json:"graph_token_expires_at,omitempty"`
	IMAPTokenExpiresAt    *time.Time                   `json:"imap_token_expires_at,omitempty"`
	RefreshAfter          *time.Time                   `json:"refresh_after,omitempty"`
	RefreshStatus         string                       `json:"refresh_status"`
	LastRefreshedAt       *time.Time                   `json:"last_refreshed_at,omitempty"`
	LastRefreshError      string                       `json:"last_refresh_error,omitempty"`
	AutoRefresh           bool                         `json:"auto_refresh"`
}

type RetrievalCapabilitySummary struct {
	Method               domain.RetrievalMethod `json:"method"`
	Status               string                 `json:"status"`
	AccessTokenExpiresAt *time.Time             `json:"access_token_expires_at,omitempty"`
	CheckedAt            *time.Time             `json:"checked_at,omitempty"`
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
	summaries, err := s.Summaries(ctx, mailboxID, autoRefresh)
	if err != nil {
		return MailboxDetail{}, err
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

func (s *MailboxDetailService) Summaries(ctx context.Context, mailboxID string, autoRefresh bool) ([]CredentialSummary, error) {
	credentials, err := s.mailboxes.ListCredentials(ctx, mailboxID)
	if err != nil {
		return nil, err
	}
	summaries := make([]CredentialSummary, 0, len(credentials))
	for _, credential := range credentials {
		summaries = append(summaries, s.summarizeCredential(ctx, credential, autoRefresh))
	}
	s.applyPersistedCapabilities(ctx, mailboxID, summaries)
	return summaries, nil
}

func (s *MailboxDetailService) applyPersistedCapabilities(ctx context.Context, mailboxID string, summaries []CredentialSummary) {
	if s.capabilities == nil {
		return
	}
	items, err := s.capabilities.ListRetrievalCapabilities(ctx, mailboxID)
	if err != nil {
		return
	}
	byMethod := make(map[domain.RetrievalMethod]domain.MailboxRetrievalCapability, len(items))
	for _, item := range items {
		byMethod[item.Method] = item
	}
	for summaryIndex := range summaries {
		for capabilityIndex := range summaries[summaryIndex].RetrievalCapabilities {
			capability := &summaries[summaryIndex].RetrievalCapabilities[capabilityIndex]
			persisted, ok := byMethod[capability.Method]
			if !ok {
				continue
			}
			switch persisted.Status {
			case domain.RetrievalCapabilityAvailable:
				capability.Status = "verified"
			case domain.RetrievalCapabilityUnavailable, domain.RetrievalCapabilityError:
				capability.Status = "failed"
			case domain.RetrievalCapabilityPending:
				capability.Status = "configured"
			}
			capability.CheckedAt = copyCredentialTime(persisted.CheckedAt)
			if persisted.TokenExpiresAt != nil {
				capability.AccessTokenExpiresAt = copyCredentialTime(persisted.TokenExpiresAt)
			}
		}
	}
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
		if primaryRefreshToken(credential.Kind, secret) != "" {
			summary.HasRefreshToken = true
			summary.RefreshToken = maskedCredentialValue
		}
	}
	if summary.RefreshStatus == "" || summary.RefreshStatus == "unknown" {
		summary.RefreshStatus = derivedRefreshStatus(s.clock().UTC(), credential, refreshCredentialComplete(credential.Kind, secret), err)
	}
	summary.RefreshTokenValidity = refreshTokenValidity(credential.Kind, summary.HasRefreshToken, summary.RefreshStatus)
	summary.RetrievalCapabilities = retrievalCapabilities(s.clock().UTC(), credential, summary, secret, err)
	return summary
}

type RevealedCredential struct {
	ClientID              string                       `json:"client_id,omitempty"`
	RefreshToken          string                       `json:"refresh_token"`
	CredentialType        domain.CredentialKind        `json:"credential_type"`
	RetrievalMethods      []domain.RetrievalMethod     `json:"retrieval_methods,omitempty"`
	RetrievalCapabilities []RetrievalCapabilitySummary `json:"retrieval_capabilities,omitempty"`
	RefreshTokenValidity  string                       `json:"refresh_token_validity,omitempty"`
	ExpiresAt             *time.Time                   `json:"expires_at,omitempty"`
	GraphTokenExpiresAt   *time.Time                   `json:"graph_token_expires_at,omitempty"`
	IMAPTokenExpiresAt    *time.Time                   `json:"imap_token_expires_at,omitempty"`
	RevealedUntil         time.Time                    `json:"revealed_until"`
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
	summary := s.summarizeCredential(ctx, credential, false)
	summaries := []CredentialSummary{summary}
	s.applyPersistedCapabilities(ctx, mailboxID, summaries)
	summary = summaries[0]
	return RevealedCredential{
		ClientID: clientID, RefreshToken: refreshToken,
		CredentialType: credential.Kind, RetrievalMethods: credentialRetrievalMethods(credential.Kind),
		RetrievalCapabilities: summary.RetrievalCapabilities, RefreshTokenValidity: summary.RefreshTokenValidity,
		ExpiresAt:           credentialSummaryExpiry(credential, secret),
		GraphTokenExpiresAt: secret.GraphTokenExpiresAt, IMAPTokenExpiresAt: secret.IMAPTokenExpiresAt,
		RevealedUntil: s.clock().UTC().Add(60 * time.Second),
	}, nil
}

const maskedCredentialValue = "********"

type openedMailboxCredential struct {
	ClientID            string
	RefreshToken        string
	GraphAccessToken    bool
	IMAPAccessToken     bool
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
			secret.RefreshToken = firstCredentialValue(microsoft.RefreshToken, microsoft.GraphRefreshToken)
			secret.GraphAccessToken = microsoftGraphAccessTokenPresent(microsoft)
			if secret.GraphAccessToken {
				secret.GraphTokenExpiresAt = microsoftTokenExpiry(microsoft.GraphTokenExpiresAt, microsoft, domain.RetrievalMicrosoftGraph, credential.ExpiresAt)
			}
		case domain.CredentialMicrosoftIMAPOAuth:
			secret.RefreshToken = firstCredentialValue(microsoft.RefreshToken, microsoft.IMAPRefreshToken)
			secret.IMAPAccessToken = microsoftIMAPAccessTokenPresent(microsoft)
			if secret.IMAPAccessToken {
				secret.IMAPTokenExpiresAt = microsoftTokenExpiry(microsoft.IMAPTokenExpiresAt, microsoft, domain.RetrievalIMAPOAuth, credential.ExpiresAt)
			}
		case domain.CredentialMicrosoftDualToken:
			secret.RefreshToken = firstCredentialValue(microsoft.RefreshToken, microsoft.GraphRefreshToken, microsoft.IMAPRefreshToken)
			secret.GraphAccessToken = microsoftGraphAccessTokenPresent(microsoft)
			secret.IMAPAccessToken = microsoftIMAPAccessTokenPresent(microsoft)
			if secret.GraphAccessToken {
				secret.GraphTokenExpiresAt = microsoftTokenExpiry(microsoft.GraphTokenExpiresAt, microsoft, domain.RetrievalMicrosoftGraph, credential.ExpiresAt)
			}
			if secret.IMAPAccessToken {
				secret.IMAPTokenExpiresAt = microsoftTokenExpiry(microsoft.IMAPTokenExpiresAt, microsoft, domain.RetrievalIMAPOAuth, credential.ExpiresAt)
			}
		}
	}
	return secret, nil
}

func microsoftGraphAccessTokenPresent(secret domain.MicrosoftCredentialSecret) bool {
	return strings.TrimSpace(secret.GraphAccessToken) != "" ||
		(secret.AccessTokenMethod == domain.RetrievalMicrosoftGraph && strings.TrimSpace(secret.AccessToken) != "")
}

func microsoftIMAPAccessTokenPresent(secret domain.MicrosoftCredentialSecret) bool {
	return strings.TrimSpace(secret.IMAPAccessToken) != "" ||
		(secret.AccessTokenMethod == domain.RetrievalIMAPOAuth && strings.TrimSpace(secret.AccessToken) != "")
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
	return secret.RefreshToken
}

func refreshCredentialComplete(kind domain.CredentialKind, secret openedMailboxCredential) bool {
	switch kind {
	case domain.CredentialMicrosoftGraphOAuth, domain.CredentialMicrosoftIMAPOAuth,
		domain.CredentialMicrosoftDualToken, domain.CredentialGmailOAuth:
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

type retrievalVerificationRecord struct {
	Status    string     `json:"status"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

type credentialOperationalMetadata struct {
	RetrievalVerification map[string]retrievalVerificationRecord `json:"retrieval_verification,omitempty"`
}

func refreshTokenValidity(kind domain.CredentialKind, hasRefreshToken bool, refreshStatus string) string {
	if !refreshableCredential(kind) {
		return "not_applicable"
	}
	switch strings.ToLower(strings.TrimSpace(refreshStatus)) {
	case "error", "unreadable":
		return "error"
	}
	if hasRefreshToken {
		return "expiry_not_returned"
	}
	return "missing"
}

func retrievalCapabilities(now time.Time, credential domain.MailboxCredential, summary CredentialSummary, secret openedMailboxCredential, openErr error) []RetrievalCapabilitySummary {
	records := retrievalVerificationRecords(credential.Metadata)
	capabilities := make([]RetrievalCapabilitySummary, 0, len(summary.RetrievalMethods))
	for _, method := range summary.RetrievalMethods {
		capability := RetrievalCapabilitySummary{Method: method, Status: "unknown"}
		switch method {
		case domain.RetrievalMicrosoftGraph:
			capability.AccessTokenExpiresAt = copyCredentialTime(secret.GraphTokenExpiresAt)
		case domain.RetrievalIMAPOAuth:
			capability.AccessTokenExpiresAt = copyCredentialTime(secret.IMAPTokenExpiresAt)
		case domain.RetrievalGmailAPI:
			capability.AccessTokenExpiresAt = copyCredentialTime(summary.ExpiresAt)
		}
		if record, ok := records[string(method)]; ok && validRetrievalCapabilityStatus(record.Status) {
			capability.Status = record.Status
			capability.CheckedAt = copyCredentialTime(record.CheckedAt)
		} else if openErr != nil && !errors.Is(openErr, domain.ErrNotFound) {
			capability.Status = "failed"
		} else if retrievalMethodRefreshFailed(credential.LastRefreshError, method) {
			capability.Status = "failed"
		} else if summary.HasRefreshToken || credential.Kind == domain.CredentialIMAPPassword {
			capability.Status = "configured"
		}
		if capability.Status == "verified" && capability.AccessTokenExpiresAt != nil && !capability.AccessTokenExpiresAt.After(now) {
			// Channel verification remains valid evidence after a short-lived AT expires.
			capability.AccessTokenExpiresAt = copyCredentialTime(capability.AccessTokenExpiresAt)
		}
		capabilities = append(capabilities, capability)
	}
	return capabilities
}

func retrievalVerificationRecords(metadata json.RawMessage) map[string]retrievalVerificationRecord {
	if len(metadata) == 0 || !json.Valid(metadata) {
		return nil
	}
	var operational credentialOperationalMetadata
	if err := json.Unmarshal(metadata, &operational); err != nil {
		return nil
	}
	return operational.RetrievalVerification
}

func validRetrievalCapabilityStatus(status string) bool {
	switch status {
	case "configured", "verified", "failed", "unknown":
		return true
	default:
		return false
	}
}

func retrievalMethodRefreshFailed(message string, method domain.RetrievalMethod) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return false
	}
	switch method {
	case domain.RetrievalMicrosoftGraph:
		return strings.Contains(message, "graph") || strings.Contains(message, "outlook")
	case domain.RetrievalIMAPOAuth, domain.RetrievalIMAPPassword:
		return strings.Contains(message, "imap")
	case domain.RetrievalGmailAPI:
		return strings.Contains(message, "gmail") || strings.Contains(message, "google")
	default:
		return false
	}
}

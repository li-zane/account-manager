package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

var errProviderCredentialRefresh = errors.New("provider credential refresh failed")

const credentialRefreshCheckpointTimeout = 3 * time.Second

// RetrieveMessagesInput identifies either a primary mailbox or one of its
// aliases. Exactly one identifier is required.
type RetrieveMessagesInput struct {
	MailboxID string
	AliasID   string
	Query     domain.MessageQuery
}

type RecipientMatcher func(message domain.Message, recipient string) bool

// MessageRetrievalService owns routing, credential selection, refresh
// persistence, and the final alias isolation check. Provider adapters remain
// responsible for provider-native protocols and encrypted credential payloads.
type MessageRetrievalService struct {
	mailboxes ports.MailboxRepository
	providers ports.ProviderRegistry
	matches   RecipientMatcher
	clock     func() time.Time

	refreshLocksMu sync.Mutex
	refreshLocks   map[string]*credentialRefreshLock
}

type credentialRefreshLock struct {
	mu   sync.Mutex
	refs int
}

func NewMessageRetrievalService(mailboxes ports.MailboxRepository, registry ports.ProviderRegistry, matches RecipientMatcher) (*MessageRetrievalService, error) {
	if mailboxes == nil || registry == nil || matches == nil {
		return nil, fmt.Errorf("%w: mailbox repository, provider registry, and recipient matcher are required", domain.ErrInvalid)
	}
	return &MessageRetrievalService{
		mailboxes:    mailboxes,
		providers:    registry,
		matches:      matches,
		clock:        time.Now,
		refreshLocks: make(map[string]*credentialRefreshLock),
	}, nil
}

func (s *MessageRetrievalService) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

func (s *MessageRetrievalService) Retrieve(ctx context.Context, input RetrieveMessagesInput) ([]domain.Message, error) {
	mailbox, alias, err := s.resolveTarget(ctx, input)
	if err != nil {
		return nil, err
	}
	registration, err := s.providers.Get(mailbox.Provider)
	if err != nil {
		return nil, err
	}
	if registration.Retriever == nil {
		return nil, fmt.Errorf("%w: provider %q has no mail retriever", domain.ErrNotConfigured, mailbox.Provider)
	}

	query := input.Query
	if alias != nil {
		if strings.TrimSpace(alias.NormalizedAddress) == "" {
			return nil, fmt.Errorf("%w: alias has no normalized address", domain.ErrInvalid)
		}
		query.RecipientAddress = alias.NormalizedAddress
		if query.RetrievalMethod == domain.RetrievalForwarded {
			query.RetrievalMethod = ""
		}
	} else if strings.TrimSpace(query.RecipientAddress) == "" {
		query.RecipientAddress = firstRetrievalAddress(mailbox)
	}
	if strings.TrimSpace(query.RecipientAddress) == "" {
		return nil, fmt.Errorf("%w: retrieval recipient address is required", domain.ErrInvalid)
	}

	credentials, err := s.mailboxes.ListCredentials(ctx, mailbox.ID)
	if err != nil {
		return nil, err
	}
	credential, method, err := selectRetrievalCredential(credentials, registration.Retriever.RetrievalMethods(), query.RetrievalMethod)
	if err != nil {
		return nil, err
	}
	query.RetrievalMethod = method

	credential, err = s.ensureFreshCredential(ctx, mailbox, registration.Retriever, credential)
	if err != nil {
		return nil, err
	}
	messages, err := registration.Retriever.Retrieve(ctx, mailbox, credential, query)
	if err != nil && errors.Is(err, domain.ErrUnauthorized) && refreshableCredential(credential.Kind) {
		credential, err = s.refreshCredential(ctx, mailbox, registration.Retriever, credential, true)
		if err != nil {
			return nil, err
		}
		messages, err = registration.Retriever.Retrieve(ctx, mailbox, credential, query)
	}
	if err != nil {
		return nil, err
	}
	if alias == nil {
		return messages, nil
	}

	filtered := make([]domain.Message, 0, len(messages))
	for _, message := range messages {
		if s.matches(message, alias.NormalizedAddress) {
			filtered = append(filtered, message)
		}
	}
	return filtered, nil
}

// RefreshDueCredential refreshes one credential only when its persisted state
// is due. Background workers and request-time retrieval share this entry point
// so they also share the same per-credential lock and optimistic update path.
func (s *MessageRetrievalService) RefreshDueCredential(ctx context.Context, mailboxID string, kind domain.CredentialKind) (domain.MailboxCredential, error) {
	mailboxID = strings.TrimSpace(mailboxID)
	if mailboxID == "" || kind == "" {
		return domain.MailboxCredential{}, fmt.Errorf("%w: mailbox id and credential kind are required", domain.ErrInvalid)
	}
	if !refreshableCredential(kind) {
		return domain.MailboxCredential{}, fmt.Errorf("%w: credential kind %q does not support refresh", domain.ErrInvalid, kind)
	}
	mailbox, err := s.mailboxes.GetMailbox(ctx, mailboxID)
	if err != nil {
		return domain.MailboxCredential{}, err
	}
	registration, err := s.providers.Get(mailbox.Provider)
	if err != nil {
		return domain.MailboxCredential{}, err
	}
	if registration.Retriever == nil {
		return domain.MailboxCredential{}, fmt.Errorf("%w: provider %q has no mail retriever", domain.ErrNotConfigured, mailbox.Provider)
	}
	credential, err := s.mailboxes.GetCredential(ctx, mailbox.ID, kind)
	if err != nil {
		return domain.MailboxCredential{}, err
	}
	return s.refreshCredential(ctx, mailbox, registration.Retriever, credential, false)
}

func (s *MessageRetrievalService) resolveTarget(ctx context.Context, input RetrieveMessagesInput) (domain.Mailbox, *domain.MailboxAlias, error) {
	mailboxID := strings.TrimSpace(input.MailboxID)
	aliasID := strings.TrimSpace(input.AliasID)
	if (mailboxID == "") == (aliasID == "") {
		return domain.Mailbox{}, nil, fmt.Errorf("%w: exactly one of mailbox id or alias id is required", domain.ErrInvalid)
	}
	if aliasID == "" {
		mailbox, err := s.mailboxes.GetMailbox(ctx, mailboxID)
		return mailbox, nil, err
	}
	alias, err := s.mailboxes.GetAlias(ctx, aliasID)
	if err != nil {
		return domain.Mailbox{}, nil, err
	}
	if !alias.Enabled {
		return domain.Mailbox{}, nil, fmt.Errorf("%w: mailbox alias is disabled", domain.ErrInvalid)
	}
	mailbox, err := s.mailboxes.GetMailbox(ctx, alias.MailboxID)
	if err != nil {
		return domain.Mailbox{}, nil, err
	}
	return mailbox, &alias, nil
}

func selectRetrievalCredential(credentials []domain.MailboxCredential, methods []domain.RetrievalMethod, requested domain.RetrievalMethod) (domain.MailboxCredential, domain.RetrievalMethod, error) {
	byKind := make(map[domain.CredentialKind]domain.MailboxCredential, len(credentials))
	for _, credential := range credentials {
		byKind[credential.Kind] = credential
	}
	if requested != "" {
		if !containsRetrievalMethod(methods, requested) {
			return domain.MailboxCredential{}, "", fmt.Errorf("%w: retrieval method %q is not supported", domain.ErrInvalid, requested)
		}
		for _, kind := range credentialKindsForMethod(requested) {
			if credential, ok := byKind[kind]; ok {
				return credential, concreteRetrievalMethod(requested, methods), nil
			}
		}
		return domain.MailboxCredential{}, "", fmt.Errorf("%w: no credential for retrieval method %q", domain.ErrNotFound, requested)
	}
	for _, method := range methods {
		if method == domain.RetrievalForwarded {
			continue
		}
		for _, kind := range credentialKindsForMethod(method) {
			if credential, ok := byKind[kind]; ok {
				return credential, concreteRetrievalMethod(method, methods), nil
			}
		}
	}
	return domain.MailboxCredential{}, "", fmt.Errorf("%w: mailbox has no compatible retrieval credential", domain.ErrNotFound)
}

func credentialKindsForMethod(method domain.RetrievalMethod) []domain.CredentialKind {
	switch method {
	case domain.RetrievalMicrosoftGraph:
		return []domain.CredentialKind{domain.CredentialMicrosoftGraphOAuth, domain.CredentialMicrosoftDualToken}
	case domain.RetrievalIMAPOAuth:
		return []domain.CredentialKind{domain.CredentialMicrosoftIMAPOAuth, domain.CredentialMicrosoftDualToken}
	case domain.RetrievalDualToken:
		return []domain.CredentialKind{domain.CredentialMicrosoftDualToken}
	case domain.RetrievalGmailAPI:
		return []domain.CredentialKind{domain.CredentialGmailOAuth}
	case domain.RetrievalIMAPPassword:
		return []domain.CredentialKind{domain.CredentialIMAPPassword}
	default:
		return nil
	}
}

func concreteRetrievalMethod(method domain.RetrievalMethod, supported []domain.RetrievalMethod) domain.RetrievalMethod {
	if method != domain.RetrievalDualToken {
		return method
	}
	if containsRetrievalMethod(supported, domain.RetrievalMicrosoftGraph) {
		return domain.RetrievalMicrosoftGraph
	}
	if containsRetrievalMethod(supported, domain.RetrievalIMAPOAuth) {
		return domain.RetrievalIMAPOAuth
	}
	return method
}

func containsRetrievalMethod(methods []domain.RetrievalMethod, target domain.RetrievalMethod) bool {
	for _, method := range methods {
		if method == target {
			return true
		}
	}
	return false
}

func (s *MessageRetrievalService) ensureFreshCredential(ctx context.Context, mailbox domain.Mailbox, retriever ports.MailRetriever, credential domain.MailboxCredential) (domain.MailboxCredential, error) {
	if !credentialNeedsRefresh(credential, s.clock().UTC()) {
		return credential, nil
	}
	return s.refreshCredential(ctx, mailbox, retriever, credential, false)
}

func (s *MessageRetrievalService) refreshCredential(ctx context.Context, mailbox domain.Mailbox, retriever ports.MailRetriever, observed domain.MailboxCredential, force bool) (domain.MailboxCredential, error) {
	release := s.acquireRefreshLock(mailbox.ID + "\x00" + string(observed.Kind))
	defer release()

	current, err := s.mailboxes.GetCredential(ctx, mailbox.ID, observed.Kind)
	if err != nil {
		return domain.MailboxCredential{}, err
	}
	if current.Version != observed.Version {
		return current, nil
	}
	if !force && !credentialNeedsRefresh(current, s.clock().UTC()) {
		return current, nil
	}

	refreshed, refreshErr := retriever.Refresh(ctx, mailbox, current)
	if refreshErr == nil && (len(refreshed.EncryptedSecret) == 0 || strings.TrimSpace(refreshed.KeyVersion) == "") {
		refreshErr = fmt.Errorf("%w: provider returned an empty refreshed credential", domain.ErrInvalid)
	}
	if refreshErr != nil {
		safeErr := safeCredentialRefreshError(refreshErr)
		if refreshed.PersistOnError && validRefreshedCredential(refreshed) {
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialRefreshCheckpointTimeout)
			persistErr := s.recordRefreshCheckpoint(persistCtx, current, refreshed, safeErr)
			cancel()
			if persistErr != nil {
				return domain.MailboxCredential{}, fmt.Errorf("record credential refresh checkpoint: %w", persistErr)
			}
		} else if persistErr := s.recordRefreshFailure(ctx, current, safeErr); persistErr != nil && !errors.Is(persistErr, domain.ErrConflict) {
			return domain.MailboxCredential{}, fmt.Errorf("record credential refresh failure: %w", persistErr)
		}
		return domain.MailboxCredential{}, fmt.Errorf("refresh mailbox credential: %w", safeErr)
	}

	now := s.clock().UTC()
	updated := current
	updated.EncryptedSecret = append([]byte(nil), refreshed.EncryptedSecret...)
	updated.KeyVersion = refreshed.KeyVersion
	updated.ExpiresAt = refreshed.ExpiresAt
	updated.RefreshAfter = refreshed.RefreshAfter
	updated.RefreshStatus = "active"
	updated.LastRefreshedAt = &now
	updated.LastRefreshError = ""
	updated.UpdatedAt = now
	if err := s.mailboxes.UpsertCredential(ctx, updated); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return s.mailboxes.GetCredential(ctx, mailbox.ID, current.Kind)
		}
		return domain.MailboxCredential{}, err
	}
	return s.mailboxes.GetCredential(ctx, mailbox.ID, current.Kind)
}

func validRefreshedCredential(credential domain.RefreshedCredential) bool {
	return len(credential.EncryptedSecret) > 0 && strings.TrimSpace(credential.KeyVersion) != ""
}

func (s *MessageRetrievalService) recordRefreshCheckpoint(ctx context.Context, credential domain.MailboxCredential, refreshed domain.RefreshedCredential, safeErr error) error {
	now := s.clock().UTC()
	credential.EncryptedSecret = append([]byte(nil), refreshed.EncryptedSecret...)
	credential.KeyVersion = refreshed.KeyVersion
	credential.ExpiresAt = refreshed.ExpiresAt
	credential.RefreshAfter = refreshed.RefreshAfter
	credential.RefreshStatus = "error"
	credential.LastRefreshError = safeErr.Error()
	credential.UpdatedAt = now
	return s.mailboxes.UpsertCredential(ctx, credential)
}

func (s *MessageRetrievalService) recordRefreshFailure(ctx context.Context, credential domain.MailboxCredential, safeErr error) error {
	now := s.clock().UTC()
	credential.RefreshStatus = "error"
	credential.LastRefreshError = safeErr.Error()
	credential.UpdatedAt = now
	return s.mailboxes.UpsertCredential(ctx, credential)
}

func (s *MessageRetrievalService) acquireRefreshLock(key string) func() {
	s.refreshLocksMu.Lock()
	lock := s.refreshLocks[key]
	if lock == nil {
		lock = &credentialRefreshLock{}
		s.refreshLocks[key] = lock
	}
	lock.refs++
	s.refreshLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.refreshLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.refreshLocks, key)
		}
		s.refreshLocksMu.Unlock()
	}
}

func credentialNeedsRefresh(credential domain.MailboxCredential, now time.Time) bool {
	if !refreshableCredential(credential.Kind) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(credential.RefreshStatus)) {
	case "missing", "expired", "due", "error":
		return true
	}
	if credential.ExpiresAt == nil || !credential.ExpiresAt.After(now) {
		return true
	}
	return credential.RefreshAfter != nil && !credential.RefreshAfter.After(now)
}

func refreshableCredential(kind domain.CredentialKind) bool {
	switch kind {
	case domain.CredentialMicrosoftGraphOAuth, domain.CredentialMicrosoftIMAPOAuth,
		domain.CredentialMicrosoftDualToken, domain.CredentialGmailOAuth:
		return true
	default:
		return false
	}
}

func safeCredentialRefreshError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, domain.ErrNotConfigured):
		return domain.ErrNotConfigured
	case errors.Is(err, domain.ErrUnauthorized):
		return domain.ErrUnauthorized
	case errors.Is(err, domain.ErrInvalid):
		return domain.ErrInvalid
	default:
		return errProviderCredentialRefresh
	}
}

func firstRetrievalAddress(mailbox domain.Mailbox) string {
	if value := strings.TrimSpace(mailbox.NormalizedAddress); value != "" {
		return value
	}
	return strings.TrimSpace(mailbox.Address)
}

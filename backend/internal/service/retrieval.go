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
	MailboxID     string
	AliasID       string
	AllRecipients bool
	Query         domain.MessageQuery
}

type RecipientMatcher func(message domain.Message, recipient string) bool

// MessageRetrievalService owns routing, credential selection, refresh
// persistence, and the final alias isolation check. Provider adapters remain
// responsible for provider-native protocols and encrypted credential payloads.
type MessageRetrievalService struct {
	mailboxes    ports.MailboxRepository
	providers    ports.ProviderRegistry
	matches      RecipientMatcher
	settings     CredentialRefreshSettingsReader
	capabilities ports.RetrievalCapabilityRepository
	clock        func() time.Time

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

// SetSettingsReader installs the runtime refresh switch. Refreshing is
// fail-closed when no reader is configured or when the setting is disabled.
func (s *MessageRetrievalService) SetSettingsReader(settings CredentialRefreshSettingsReader) {
	s.settings = settings
}

func (s *MessageRetrievalService) SetCapabilityRepository(repository ports.RetrievalCapabilityRepository) {
	s.capabilities = repository
}

func (s *MessageRetrievalService) Retrieve(ctx context.Context, input RetrieveMessagesInput) ([]domain.Message, error) {
	messages, _, err := s.RetrieveWithMethod(ctx, input)
	return messages, err
}

// RetrieveWithMethod also reports the concrete provider channel selected by
// automatic routing so callers can persist accurate message provenance.
func (s *MessageRetrievalService) RetrieveWithMethod(ctx context.Context, input RetrieveMessagesInput) ([]domain.Message, domain.RetrievalMethod, error) {
	mailbox, alias, err := s.resolveTarget(ctx, input)
	if err != nil {
		return nil, "", err
	}
	registration, err := s.providers.Get(mailbox.Provider)
	if err != nil {
		return nil, "", err
	}
	if registration.Retriever == nil {
		return nil, "", fmt.Errorf("%w: provider %q has no mail retriever", domain.ErrNotConfigured, mailbox.Provider)
	}

	query := input.Query
	if alias != nil {
		if strings.TrimSpace(alias.NormalizedAddress) == "" {
			return nil, "", fmt.Errorf("%w: alias has no normalized address", domain.ErrInvalid)
		}
		query.RecipientAddress = alias.NormalizedAddress
		if query.RetrievalMethod == domain.RetrievalForwarded {
			query.RetrievalMethod = ""
		}
	} else if strings.TrimSpace(query.RecipientAddress) == "" && !input.AllRecipients {
		query.RecipientAddress = firstRetrievalAddress(mailbox)
	}
	if strings.TrimSpace(query.RecipientAddress) == "" && !input.AllRecipients {
		return nil, "", fmt.Errorf("%w: retrieval recipient address is required", domain.ErrInvalid)
	}

	credentials, err := s.mailboxes.ListCredentials(ctx, mailbox.ID)
	if err != nil {
		return nil, "", err
	}
	candidates, err := s.retrievalCandidates(ctx, mailbox.ID, credentials, registration.Retriever.RetrievalMethods(), query.RetrievalMethod)
	if err != nil {
		return nil, "", err
	}
	var messages []domain.Message
	var retrievalErr error
	var selectedMethod domain.RetrievalMethod
	for _, candidate := range candidates {
		query.RetrievalMethod = candidate.method
		credential, ensureErr := s.ensureFreshCredential(ctx, mailbox, registration.Retriever, candidate.credential, candidate.method)
		if ensureErr == nil {
			messages, retrievalErr = registration.Retriever.Retrieve(ctx, mailbox, credential, query)
		} else {
			retrievalErr = ensureErr
		}
		if retrievalErr != nil && errors.Is(retrievalErr, domain.ErrUnauthorized) && refreshableCredential(candidate.credential.Kind) {
			credential, ensureErr = s.refreshMethodCredential(ctx, mailbox, registration.Retriever, candidate.credential, candidate.method, true)
			if ensureErr == nil {
				messages, retrievalErr = registration.Retriever.Retrieve(ctx, mailbox, credential, query)
			} else {
				retrievalErr = ensureErr
			}
		}
		s.recordCapabilityResult(ctx, mailbox.ID, candidate.method, credential.ExpiresAt, retrievalErr)
		if retrievalErr == nil {
			selectedMethod = candidate.method
			break
		}
		if queryRequested(input.Query.RetrievalMethod) || errors.Is(retrievalErr, context.Canceled) || errors.Is(retrievalErr, context.DeadlineExceeded) {
			break
		}
	}
	if retrievalErr != nil {
		return nil, "", retrievalErr
	}
	if alias == nil {
		return messages, selectedMethod, nil
	}

	filtered := make([]domain.Message, 0, len(messages))
	for _, message := range messages {
		if s.matches(message, alias.NormalizedAddress) {
			filtered = append(filtered, message)
		}
	}
	return filtered, selectedMethod, nil
}

func (s *MessageRetrievalService) SyncIncremental(ctx context.Context, mailboxID string, request domain.MessageSyncRequest) (domain.MessageSyncResult, error) {
	mailbox, err := s.mailboxes.GetMailbox(ctx, strings.TrimSpace(mailboxID))
	if err != nil {
		return domain.MessageSyncResult{}, err
	}
	registration, err := s.providers.Get(mailbox.Provider)
	if err != nil {
		return domain.MessageSyncResult{}, err
	}
	incremental, ok := registration.Retriever.(ports.IncrementalMailRetriever)
	if !ok {
		return domain.MessageSyncResult{}, fmt.Errorf("%w: provider has no incremental retriever", domain.ErrNotConfigured)
	}
	credentials, err := s.mailboxes.ListCredentials(ctx, mailbox.ID)
	if err != nil {
		return domain.MessageSyncResult{}, err
	}
	explicitMethod := queryRequested(request.Method)
	candidates, err := s.retrievalCandidates(ctx, mailbox.ID, credentials, registration.Retriever.RetrievalMethods(), request.Method)
	if err != nil {
		return domain.MessageSyncResult{}, err
	}
	var lastErr error
	for _, candidate := range candidates {
		request.Method = candidate.method
		credential, ensureErr := s.ensureFreshCredential(ctx, mailbox, registration.Retriever, candidate.credential, candidate.method)
		if ensureErr == nil {
			var result domain.MessageSyncResult
			result, lastErr = incremental.SyncIncremental(ctx, mailbox, credential, request)
			if lastErr == nil {
				result.Method = candidate.method
				s.recordCapabilityResult(ctx, mailbox.ID, candidate.method, credential.ExpiresAt, nil)
				return result, nil
			}
		} else {
			lastErr = ensureErr
		}
		if errors.Is(lastErr, domain.ErrUnauthorized) {
			credential, ensureErr = s.refreshMethodCredential(ctx, mailbox, registration.Retriever, candidate.credential, candidate.method, true)
			if ensureErr == nil {
				result, retryErr := incremental.SyncIncremental(ctx, mailbox, credential, request)
				lastErr = retryErr
				if retryErr == nil {
					result.Method = candidate.method
					s.recordCapabilityResult(ctx, mailbox.ID, candidate.method, credential.ExpiresAt, nil)
					return result, nil
				}
			} else {
				lastErr = ensureErr
			}
		}
		s.recordCapabilityResult(ctx, mailbox.ID, candidate.method, credential.ExpiresAt, lastErr)
		if explicitMethod || errors.Is(lastErr, domain.ErrSyncCursorInvalid) {
			break
		}
	}
	return domain.MessageSyncResult{}, lastErr
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
	refreshEnabled, err := s.refreshEnabled(ctx)
	if err != nil {
		return domain.MailboxCredential{}, err
	}
	if !refreshEnabled {
		return s.mailboxes.GetCredential(ctx, mailboxID, kind)
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
	if _, ok := registration.Retriever.(ports.MethodAccessTokenManager); ok {
		methods := registration.Retriever.RetrievalMethods()
		var result = credential
		var refreshErrors []error
		for _, method := range methods {
			if method != domain.RetrievalMicrosoftGraph && method != domain.RetrievalIMAPOAuth {
				continue
			}
			updated, methodErr := s.refreshMethodCredential(ctx, mailbox, registration.Retriever, result, method, false)
			if methodErr != nil {
				refreshErrors = append(refreshErrors, methodErr)
				continue
			}
			result = updated
		}
		return result, errors.Join(refreshErrors...)
	}
	return s.refreshCredential(ctx, mailbox, registration.Retriever, credential, false)
}

type retrievalCandidate struct {
	credential domain.MailboxCredential
	method     domain.RetrievalMethod
}

func queryRequested(method domain.RetrievalMethod) bool {
	return method != "" && method != domain.RetrievalDualToken
}

func (s *MessageRetrievalService) retrievalCandidates(ctx context.Context, mailboxID string, credentials []domain.MailboxCredential, methods []domain.RetrievalMethod, requested domain.RetrievalMethod) ([]retrievalCandidate, error) {
	if queryRequested(requested) {
		credential, method, err := selectRetrievalCredential(credentials, methods, requested)
		if err != nil {
			return nil, err
		}
		return []retrievalCandidate{{credential: credential, method: method}}, nil
	}
	ordered := []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth}
	for _, method := range methods {
		if method != domain.RetrievalMicrosoftGraph && method != domain.RetrievalIMAPOAuth && method != domain.RetrievalDualToken && method != domain.RetrievalForwarded {
			ordered = append(ordered, method)
		}
	}
	capabilityState := map[domain.RetrievalMethod]domain.RetrievalCapabilityStatus{}
	if s.capabilities != nil {
		if items, err := s.capabilities.ListRetrievalCapabilities(ctx, mailboxID); err == nil {
			for _, item := range items {
				capabilityState[item.Method] = item.Status
			}
		}
	}
	result := make([]retrievalCandidate, 0, len(ordered))
	for _, method := range ordered {
		if !containsRetrievalMethod(methods, method) || capabilityState[method] == domain.RetrievalCapabilityUnavailable {
			continue
		}
		credential, concrete, err := selectRetrievalCredential(credentials, methods, method)
		if err == nil {
			result = append(result, retrievalCandidate{credential: credential, method: concrete})
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: mailbox has no available retrieval capability", domain.ErrNotFound)
	}
	return result, nil
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

func (s *MessageRetrievalService) ensureFreshCredential(ctx context.Context, mailbox domain.Mailbox, retriever ports.MailRetriever, credential domain.MailboxCredential, method domain.RetrievalMethod) (domain.MailboxCredential, error) {
	if _, ok := retriever.(ports.MethodAccessTokenManager); ok {
		return s.refreshMethodCredential(ctx, mailbox, retriever, credential, method, false)
	}
	if !credentialNeedsRefresh(credential, s.clock().UTC()) {
		return credential, nil
	}
	return s.refreshCredential(ctx, mailbox, retriever, credential, false)
}

func (s *MessageRetrievalService) refreshEnabled(ctx context.Context) (bool, error) {
	if s.settings == nil {
		return false, nil
	}
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return false, fmt.Errorf("read token refresh setting: %w", err)
	}
	return settings.Enabled, nil
}

func (s *MessageRetrievalService) refreshMethodCredential(ctx context.Context, mailbox domain.Mailbox, retriever ports.MailRetriever, observed domain.MailboxCredential, method domain.RetrievalMethod, force bool) (domain.MailboxCredential, error) {
	manager, ok := retriever.(ports.MethodAccessTokenManager)
	if !ok {
		return s.refreshCredential(ctx, mailbox, retriever, observed, force)
	}
	lockKey := mailbox.ID + "\x00" + string(observed.Kind)
	release := s.acquireRefreshLock(lockKey)
	defer release()
	if locker, ok := s.mailboxes.(ports.CredentialRefreshLocker); ok {
		releaseDistributed, err := locker.AcquireCredentialRefreshLock(ctx, lockKey)
		if err != nil {
			return domain.MailboxCredential{}, err
		}
		defer releaseDistributed()
	}
	current, err := s.mailboxes.GetCredential(ctx, mailbox.ID, observed.Kind)
	if err != nil {
		return domain.MailboxCredential{}, err
	}
	refreshed, changed, refreshErr := manager.EnsureAccessToken(ctx, mailbox, current, method, force)
	if refreshErr != nil {
		safeErr := safeCredentialRefreshError(refreshErr)
		if persistErr := s.recordRefreshFailure(ctx, current, safeErr); persistErr != nil && !errors.Is(persistErr, domain.ErrConflict) {
			return domain.MailboxCredential{}, persistErr
		}
		return domain.MailboxCredential{}, fmt.Errorf("refresh mailbox credential: %w", safeErr)
	}
	if !changed {
		return current, nil
	}
	if !validRefreshedCredential(refreshed) {
		return domain.MailboxCredential{}, fmt.Errorf("%w: provider returned an empty refreshed credential", domain.ErrInvalid)
	}
	now := s.clock().UTC()
	current.EncryptedSecret = append([]byte(nil), refreshed.EncryptedSecret...)
	current.KeyVersion = refreshed.KeyVersion
	current.ExpiresAt, current.RefreshAfter = refreshed.ExpiresAt, refreshed.RefreshAfter
	current.RefreshStatus, current.LastRefreshError, current.UpdatedAt, current.LastRefreshedAt = "active", "", now, &now
	if err := s.mailboxes.UpsertCredential(ctx, current); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return s.mailboxes.GetCredential(ctx, mailbox.ID, current.Kind)
		}
		return domain.MailboxCredential{}, err
	}
	return s.mailboxes.GetCredential(ctx, mailbox.ID, current.Kind)
}

func (s *MessageRetrievalService) InitializeCapabilities(ctx context.Context, mailboxIDs []string) error {
	if s.capabilities == nil {
		return nil
	}
	for _, mailboxID := range mailboxIDs {
		mailbox, err := s.mailboxes.GetMailbox(ctx, mailboxID)
		if err != nil {
			return err
		}
		if mailbox.Provider != domain.ProviderMicrosoft {
			continue
		}
		for _, method := range []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth} {
			if _, err := s.capabilities.GetRetrievalCapability(ctx, mailboxID, method); err == nil {
				continue
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if err := s.capabilities.UpsertRetrievalCapability(ctx, domain.MailboxRetrievalCapability{MailboxID: mailboxID, Method: method, Status: domain.RetrievalCapabilityPending}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *MessageRetrievalService) ProbeMailbox(ctx context.Context, mailboxID string) error {
	var probeErrors []error
	for _, method := range []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth} {
		_, err := s.Retrieve(ctx, RetrieveMessagesInput{MailboxID: mailboxID, Query: domain.MessageQuery{Folder: domain.MessageFolderInbox, RetrievalMethod: method, Limit: 1, PageSize: 1, MaxPages: 1}})
		if err != nil {
			probeErrors = append(probeErrors, fmt.Errorf("%s: %w", method, err))
		}
	}
	return errors.Join(probeErrors...)
}

func (s *MessageRetrievalService) recordCapabilityResult(ctx context.Context, mailboxID string, method domain.RetrievalMethod, expiresAt *time.Time, retrievalErr error) {
	if s.capabilities == nil || (method != domain.RetrievalMicrosoftGraph && method != domain.RetrievalIMAPOAuth) {
		return
	}
	now := s.clock().UTC()
	capability := domain.MailboxRetrievalCapability{MailboxID: mailboxID, Method: method, CheckedAt: &now, TokenExpiresAt: expiresAt}
	if retrievalErr == nil {
		capability.Status = domain.RetrievalCapabilityAvailable
		capability.Preferred = method == domain.RetrievalMicrosoftGraph
		if method == domain.RetrievalIMAPOAuth {
			graph, err := s.capabilities.GetRetrievalCapability(ctx, mailboxID, domain.RetrievalMicrosoftGraph)
			capability.Preferred = err != nil || graph.Status != domain.RetrievalCapabilityAvailable
		}
	} else if errors.Is(retrievalErr, domain.ErrUnauthorized) || errors.Is(retrievalErr, domain.ErrNotConfigured) {
		capability.Status, capability.ErrorCode, capability.ErrorMessage = domain.RetrievalCapabilityUnavailable, messageSyncError(retrievalErr), messageSyncError(retrievalErr)
	} else {
		capability.Status, capability.ErrorCode, capability.ErrorMessage = domain.RetrievalCapabilityError, messageSyncError(retrievalErr), messageSyncError(retrievalErr)
		if existing, err := s.capabilities.GetRetrievalCapability(ctx, mailboxID, method); err == nil && existing.Status == domain.RetrievalCapabilityAvailable {
			capability.Status, capability.Preferred = domain.RetrievalCapabilityAvailable, existing.Preferred
			if capability.TokenExpiresAt == nil {
				capability.TokenExpiresAt = existing.TokenExpiresAt
			}
		}
	}
	_ = s.capabilities.UpsertRetrievalCapability(context.WithoutCancel(ctx), capability)
}

func (s *MessageRetrievalService) refreshCredential(ctx context.Context, mailbox domain.Mailbox, retriever ports.MailRetriever, observed domain.MailboxCredential, force bool) (domain.MailboxCredential, error) {
	lockKey := mailbox.ID + "\x00" + string(observed.Kind)
	release := s.acquireRefreshLock(lockKey)
	defer release()
	if locker, ok := s.mailboxes.(ports.CredentialRefreshLocker); ok {
		releaseDistributed, err := locker.AcquireCredentialRefreshLock(ctx, lockKey)
		if err != nil {
			return domain.MailboxCredential{}, err
		}
		defer releaseDistributed()
	}

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

package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

type retrievalTestProvider struct {
	key domain.ProviderKey
}

func (p retrievalTestProvider) Descriptor(context.Context) domain.ProviderDescriptor {
	return domain.ProviderDescriptor{Key: p.key}
}

func (p retrievalTestProvider) NormalizeAddress(address string) (string, error) {
	return strings.ToLower(strings.TrimSpace(address)), nil
}

func (p retrievalTestProvider) Provision(context.Context, domain.ProvisionMailboxRequest) (domain.ProvisionMailboxResult, error) {
	return domain.ProvisionMailboxResult{}, domain.ErrNotConfigured
}

type retrievalTestRetriever struct {
	methods  []domain.RetrievalMethod
	retrieve func(context.Context, domain.Mailbox, domain.MailboxCredential, domain.MessageQuery) ([]domain.Message, error)
	refresh  func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error)
}

func (r *retrievalTestRetriever) RetrievalMethods() []domain.RetrievalMethod {
	return append([]domain.RetrievalMethod(nil), r.methods...)
}

func (r *retrievalTestRetriever) Retrieve(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
	return r.retrieve(ctx, mailbox, credential, query)
}

func (r *retrievalTestRetriever) Refresh(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential) (domain.RefreshedCredential, error) {
	return r.refresh(ctx, mailbox, credential)
}

type retrievalTestRegistry struct {
	key          domain.ProviderKey
	registration ports.ProviderRegistration
}

type retrievalRefreshSettingsFunc func(context.Context) (service.TokenRefreshSettings, error)

func (f retrievalRefreshSettingsFunc) Get(ctx context.Context) (service.TokenRefreshSettings, error) {
	return f(ctx)
}

func (r retrievalTestRegistry) Register(ports.ProviderRegistration) error { return domain.ErrConflict }

func (r retrievalTestRegistry) Get(key domain.ProviderKey) (ports.ProviderRegistration, error) {
	if key != r.key {
		return ports.ProviderRegistration{}, domain.ErrNotFound
	}
	return r.registration, nil
}

func (r retrievalTestRegistry) List(context.Context) []domain.ProviderDescriptor {
	return []domain.ProviderDescriptor{r.registration.Provider.Descriptor(context.Background())}
}

func TestMessageRetrievalRoutesAliasAndFiltersFailClosed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	alias := domain.MailboxAlias{
		ID: "alias_fixture", MailboxID: mailbox.ID, Provider: domain.ProviderCloudflareRoute,
		Address: "Split@Example.net", NormalizedAddress: "split@example.net", Kind: domain.AliasKindForward,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateAlias(ctx, alias); err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Hour)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftGraphOAuth, "graph-secret", future, now)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftIMAPOAuth, "imap-secret", future, now)

	var mu sync.Mutex
	var calls []struct {
		kind      domain.CredentialKind
		method    domain.RetrievalMethod
		recipient string
	}
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth},
		retrieve: func(_ context.Context, receivedMailbox domain.Mailbox, credential domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
			if receivedMailbox.ID != mailbox.ID {
				t.Fatalf("retrieval mailbox = %q", receivedMailbox.ID)
			}
			mu.Lock()
			calls = append(calls, struct {
				kind      domain.CredentialKind
				method    domain.RetrievalMethod
				recipient string
			}{credential.Kind, query.RetrievalMethod, query.RecipientAddress})
			mu.Unlock()
			return []domain.Message{
				{ID: "direct", RecipientAddresses: []string{"SPLIT@example.net"}},
				{ID: "header", Headers: map[string][]string{"X-Original-To": {"split@example.net"}}},
				{ID: "other", RecipientAddresses: []string{"other@example.net"}},
				{ID: "missing-evidence"},
			}, nil
		},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			t.Fatal("fresh credentials must not be refreshed")
			return domain.RefreshedCredential{}, nil
		},
	}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	messages, err := service.Retrieve(ctx, servicepkgInput("", alias.ID, domain.MessageQuery{
		RetrievalMethod: domain.RetrievalIMAPOAuth, RecipientAddress: "attacker@example.net",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID != "direct" || messages[1].ID != "header" {
		t.Fatalf("alias messages = %+v", messages)
	}
	if len(calls) != 1 || calls[0].kind != domain.CredentialMicrosoftIMAPOAuth || calls[0].method != domain.RetrievalIMAPOAuth || calls[0].recipient != alias.NormalizedAddress {
		t.Fatalf("alias retrieval call = %+v", calls)
	}

	_, err = service.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1].kind != domain.CredentialMicrosoftGraphOAuth || calls[1].method != domain.RetrievalMicrosoftGraph || calls[1].recipient != mailbox.NormalizedAddress {
		t.Fatalf("primary retrieval call = %+v", calls)
	}
}

func TestMessageRetrievalFallsBackFromGraphToVerifiedIMAP(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 2, 0, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	future := now.Add(time.Hour)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftDualToken, "sealed", future, now)
	for _, method := range []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth} {
		if err := store.UpsertRetrievalCapability(ctx, domain.MailboxRetrievalCapability{MailboxID: mailbox.ID, Method: method, Status: domain.RetrievalCapabilityAvailable}); err != nil {
			t.Fatal(err)
		}
	}
	var methods []domain.RetrievalMethod
	retriever := &retrievalTestRetriever{methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth}, retrieve: func(_ context.Context, _ domain.Mailbox, _ domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
		methods = append(methods, query.RetrievalMethod)
		if query.RetrievalMethod == domain.RetrievalMicrosoftGraph {
			return nil, domain.ErrNotConfigured
		}
		return []domain.Message{{ID: "imap", ReceivedAt: now, RecipientAddresses: []string{mailbox.NormalizedAddress}}}, nil
	}, refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
		return domain.RefreshedCredential{}, domain.ErrNotConfigured
	}}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	service.SetCapabilityRepository(store)
	messages, err := service.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(methods) != 2 || methods[0] != domain.RetrievalMicrosoftGraph || methods[1] != domain.RetrievalIMAPOAuth {
		t.Fatalf("methods=%v messages=%v", methods, messages)
	}
}

func TestMessageRetrievalRefreshesAndPersistsCredential(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	expired := now.Add(-time.Minute)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftGraphOAuth, "old-sealed-secret", expired, now)
	newExpiry := now.Add(time.Hour)
	refreshAfter := now.Add(55 * time.Minute)
	var refreshCalls atomic.Int32
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph},
		refresh: func(_ context.Context, _ domain.Mailbox, credential domain.MailboxCredential) (domain.RefreshedCredential, error) {
			refreshCalls.Add(1)
			if string(credential.EncryptedSecret) != "old-sealed-secret" || credential.Version != 1 {
				t.Fatalf("refresh credential = %+v", credential)
			}
			return domain.RefreshedCredential{
				EncryptedSecret: []byte("new-sealed-secret"), KeyVersion: "v2",
				ExpiresAt: &newExpiry, RefreshAfter: &refreshAfter,
			}, nil
		},
		retrieve: func(_ context.Context, _ domain.Mailbox, credential domain.MailboxCredential, _ domain.MessageQuery) ([]domain.Message, error) {
			if string(credential.EncryptedSecret) != "new-sealed-secret" || credential.Version != 2 {
				t.Fatalf("retrieved with credential = %+v", credential)
			}
			return []domain.Message{{ID: "message-1"}}, nil
		},
	}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	messages, err := service.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{RetrievalMethod: domain.RetrievalMicrosoftGraph}))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || refreshCalls.Load() != 1 {
		t.Fatalf("messages=%+v refresh calls=%d", messages, refreshCalls.Load())
	}
	stored, err := store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 2 || stored.RefreshStatus != "active" || stored.LastRefreshError != "" || stored.LastRefreshedAt == nil || !stored.LastRefreshedAt.Equal(now) {
		t.Fatalf("stored refreshed credential = %+v", stored)
	}
	if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(newExpiry) || stored.RefreshAfter == nil || !stored.RefreshAfter.Equal(refreshAfter) {
		t.Fatalf("stored refresh timing = expires %v, refresh after %v", stored.ExpiresAt, stored.RefreshAfter)
	}
}

func TestMessageRetrievalRefreshesAfterRetrieverReportsMissingAccessToken(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 13, 30, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	future := now.Add(time.Hour)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftGraphOAuth, "credential-without-at", future, now)
	newExpiry := now.Add(2 * time.Hour)
	var retrieveCalls atomic.Int32
	var refreshCalls atomic.Int32
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph},
		retrieve: func(_ context.Context, _ domain.Mailbox, credential domain.MailboxCredential, _ domain.MessageQuery) ([]domain.Message, error) {
			retrieveCalls.Add(1)
			if string(credential.EncryptedSecret) == "credential-without-at" {
				return nil, fmt.Errorf("%w: access token is missing", domain.ErrUnauthorized)
			}
			return []domain.Message{{ID: "message-after-refresh"}}, nil
		},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			refreshCalls.Add(1)
			return domain.RefreshedCredential{EncryptedSecret: []byte("credential-with-at"), KeyVersion: "v2", ExpiresAt: &newExpiry}, nil
		},
	}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	messages, err := service.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "message-after-refresh" || retrieveCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("messages=%+v retrieve calls=%d refresh calls=%d", messages, retrieveCalls.Load(), refreshCalls.Load())
	}
	stored, err := store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 2 || string(stored.EncryptedSecret) != "credential-with-at" {
		t.Fatalf("stored credential = %+v", stored)
	}
}

func TestMessageRetrievalRepairsExpiredAccessTokenWhenBackgroundRefreshIsDisabled(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 13, 45, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	expired := now.Add(-time.Minute)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftDualToken, "existing-sealed-secret", expired, now)
	var retrieveCalls atomic.Int32
	var refreshCalls atomic.Int32
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth},
		retrieve: func(_ context.Context, _ domain.Mailbox, credential domain.MailboxCredential, _ domain.MessageQuery) ([]domain.Message, error) {
			retrieveCalls.Add(1)
			if string(credential.EncryptedSecret) == "credential-with-at" {
				return []domain.Message{}, nil
			}
			return nil, fmt.Errorf("%w: access token expired", domain.ErrUnauthorized)
		},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			refreshCalls.Add(1)
			future := now.Add(time.Hour)
			return domain.RefreshedCredential{EncryptedSecret: []byte("credential-with-at"), KeyVersion: "v2", ExpiresAt: &future, RefreshAfter: &future}, nil
		},
	}
	retrieval := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	retrieval.SetSettingsReader(retrievalRefreshSettingsFunc(func(context.Context) (service.TokenRefreshSettings, error) {
		return service.TokenRefreshSettings{Enabled: false}, nil
	}))

	_, err := retrieval.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{}))
	if err != nil {
		t.Fatal(err)
	}
	if retrieveCalls.Load() != 1 || refreshCalls.Load() != 1 {
		t.Fatalf("retrieve calls=%d refresh calls=%d", retrieveCalls.Load(), refreshCalls.Load())
	}
	stored, err := store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftDualToken)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 2 || string(stored.EncryptedSecret) != "credential-with-at" {
		t.Fatalf("request-time refresh was not persisted = %+v", stored)
	}
	returned, err := retrieval.RefreshDueCredential(ctx, mailbox.ID, domain.CredentialMicrosoftDualToken)
	if err != nil {
		t.Fatal(err)
	}
	if returned.Version != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("due refresh returned=%+v calls=%d", returned, refreshCalls.Load())
	}
}

func TestMessageRetrievalRefreshFailureIsRedacted(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	expired := now.Add(-time.Minute)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftGraphOAuth, "old-sealed-secret", expired, now)
	const secret = "TOKEN_fixture_secret"
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			return domain.RefreshedCredential{}, fmt.Errorf("upstream rejected %s", secret)
		},
		retrieve: func(context.Context, domain.Mailbox, domain.MailboxCredential, domain.MessageQuery) ([]domain.Message, error) {
			t.Fatal("retrieve must not run after refresh failure")
			return nil, nil
		},
	}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	_, err := service.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{}))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("refresh error leaked a secret: %v", err)
	}
	stored, getErr := store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.RefreshStatus != "error" || stored.LastRefreshError == "" || strings.Contains(stored.LastRefreshError, secret) {
		t.Fatalf("stored refresh failure = %+v", stored)
	}
	if string(stored.EncryptedSecret) != "old-sealed-secret" || stored.Version != 2 {
		t.Fatalf("failed refresh changed protected credential = %+v", stored)
	}
}

func TestMessageRetrievalPersistsPartialRefreshCheckpointAfterCancellation(t *testing.T) {
	now := time.Date(2026, 7, 24, 14, 30, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	expired := now.Add(-time.Minute)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftDualToken, "old-sealed-secret", expired, now)
	checkpointExpiry := now.Add(time.Hour)
	refreshAfter := now.Add(55 * time.Minute)
	const secret = "TOKEN_rotated_fixture"
	ctx, cancel := context.WithCancel(context.Background())
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			cancel()
			return domain.RefreshedCredential{
				EncryptedSecret: []byte("checkpoint-sealed-secret"), KeyVersion: "v2",
				ExpiresAt: &checkpointExpiry, RefreshAfter: &refreshAfter, PersistOnError: true,
			}, fmt.Errorf("%w: rejected %s", context.Canceled, secret)
		},
		retrieve: func(context.Context, domain.Mailbox, domain.MailboxCredential, domain.MessageQuery) ([]domain.Message, error) {
			t.Fatal("retrieve must not run after partial refresh failure")
			return nil, nil
		},
	}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	_, err := service.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{RetrievalMethod: domain.RetrievalMicrosoftGraph}))
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secret) {
		t.Fatalf("refresh error = %v", err)
	}

	stored, getErr := store.GetCredential(context.Background(), mailbox.ID, domain.CredentialMicrosoftDualToken)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Version != 2 || string(stored.EncryptedSecret) != "checkpoint-sealed-secret" || stored.KeyVersion != "v2" {
		t.Fatalf("stored checkpoint = %+v", stored)
	}
	if stored.RefreshStatus != "error" || stored.LastRefreshError != context.Canceled.Error() || strings.Contains(stored.LastRefreshError, secret) {
		t.Fatalf("stored checkpoint error = %+v", stored)
	}
	if stored.LastRefreshedAt != nil {
		t.Fatalf("partial checkpoint marked as fully refreshed at %v", stored.LastRefreshedAt)
	}
	if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(checkpointExpiry) || stored.RefreshAfter == nil || !stored.RefreshAfter.Equal(refreshAfter) {
		t.Fatalf("stored checkpoint timing = expires %v, refresh after %v", stored.ExpiresAt, stored.RefreshAfter)
	}
}

func TestMessageRetrievalCoalescesConcurrentRefreshes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	expired := now.Add(-time.Minute)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftGraphOAuth, "old-sealed-secret", expired, now)
	newExpiry := now.Add(time.Hour)
	var refreshCalls atomic.Int32
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			refreshCalls.Add(1)
			time.Sleep(20 * time.Millisecond)
			return domain.RefreshedCredential{EncryptedSecret: []byte("new-sealed-secret"), KeyVersion: "v2", ExpiresAt: &newExpiry}, nil
		},
		retrieve: func(_ context.Context, _ domain.Mailbox, credential domain.MailboxCredential, _ domain.MessageQuery) ([]domain.Message, error) {
			if string(credential.EncryptedSecret) != "new-sealed-secret" {
				return nil, errors.New("retrieve observed a stale credential")
			}
			return []domain.Message{{ID: "message-1"}}, nil
		},
	}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)

	const workers = 24
	start := make(chan struct{})
	errorsCh := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			messages, err := service.Retrieve(ctx, servicepkgInput(mailbox.ID, "", domain.MessageQuery{}))
			if err == nil && (len(messages) != 1 || messages[0].ID != "message-1") {
				err = fmt.Errorf("unexpected messages: %+v", messages)
			}
			errorsCh <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Error(err)
		}
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
	stored, err := store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 2 {
		t.Fatalf("credential version = %d, want 2", stored.Version)
	}
}

func TestMessageRetrievalRefreshDueCredentialUsesPersistedRefreshPath(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 15, 30, 0, 0, time.UTC)
	store, mailbox := createRetrievalMailbox(t, now)
	expired := now.Add(-time.Minute)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftGraphOAuth, "old-sealed-secret", expired, now)
	newExpiry := now.Add(time.Hour)
	var refreshCalls atomic.Int32
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph},
		refresh: func(_ context.Context, receivedMailbox domain.Mailbox, credential domain.MailboxCredential) (domain.RefreshedCredential, error) {
			refreshCalls.Add(1)
			if receivedMailbox.ID != mailbox.ID || credential.Kind != domain.CredentialMicrosoftGraphOAuth {
				t.Fatalf("refresh target = mailbox %q, kind %q", receivedMailbox.ID, credential.Kind)
			}
			return domain.RefreshedCredential{
				EncryptedSecret: []byte("new-sealed-secret"), KeyVersion: "v2", ExpiresAt: &newExpiry,
			}, nil
		},
	}
	service := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	credential, err := service.RefreshDueCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalls.Load() != 1 || credential.Version != 2 || string(credential.EncryptedSecret) != "new-sealed-secret" {
		t.Fatalf("refreshed credential = %+v, calls = %d", credential, refreshCalls.Load())
	}
	stored, err := store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshStatus != "active" || stored.LastRefreshedAt == nil || !stored.LastRefreshedAt.Equal(now) {
		t.Fatalf("stored credential = %+v", stored)
	}
}

func createRetrievalMailbox(t *testing.T, now time.Time) (*memory.Store, domain.Mailbox) {
	t.Helper()
	store := memory.New()
	mailbox := domain.Mailbox{
		ID: "mbx_microsoft_fixture", Provider: domain.ProviderMicrosoft,
		Address: "Primary@Example.com", NormalizedAddress: "primary@example.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateMailbox(context.Background(), mailbox); err != nil {
		t.Fatal(err)
	}
	return store, mailbox
}

func createRetrievalCredential(t *testing.T, store *memory.Store, mailboxID string, kind domain.CredentialKind, encrypted string, expiresAt time.Time, now time.Time) {
	t.Helper()
	credential := domain.MailboxCredential{
		ID: "cred_" + string(kind), MailboxID: mailboxID, Kind: kind,
		EncryptedSecret: []byte(encrypted), KeyVersion: "v1", ExpiresAt: &expiresAt,
		RefreshStatus: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
}

func newRetrievalService(t *testing.T, store *memory.Store, providerKey domain.ProviderKey, retriever ports.MailRetriever, now time.Time) *service.MessageRetrievalService {
	t.Helper()
	provider := retrievalTestProvider{key: providerKey}
	registry := retrievalTestRegistry{
		key:          providerKey,
		registration: ports.ProviderRegistration{Provider: provider, Retriever: retriever},
	}
	result, err := service.NewMessageRetrievalService(store, registry, providers.MessageMatchesRecipient)
	if err != nil {
		t.Fatal(err)
	}
	result.SetClock(func() time.Time { return now })
	result.SetSettingsReader(retrievalRefreshSettingsFunc(func(context.Context) (service.TokenRefreshSettings, error) {
		return service.TokenRefreshSettings{Enabled: true}, nil
	}))
	return result
}

func servicepkgInput(mailboxID, aliasID string, query domain.MessageQuery) service.RetrieveMessagesInput {
	return service.RetrieveMessagesInput{MailboxID: mailboxID, AliasID: aliasID, Query: query}
}

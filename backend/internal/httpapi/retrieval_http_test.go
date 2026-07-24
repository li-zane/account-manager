package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/httpapi"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

const retrievalHTTPAdminToken = "fixture-admin-token"

type retrievalHTTPProvider struct{}

func (retrievalHTTPProvider) Descriptor(context.Context) domain.ProviderDescriptor {
	return domain.ProviderDescriptor{Key: domain.ProviderMicrosoft}
}

func (retrievalHTTPProvider) NormalizeAddress(address string) (string, error) {
	return address, nil
}

func (retrievalHTTPProvider) Provision(context.Context, domain.ProvisionMailboxRequest) (domain.ProvisionMailboxResult, error) {
	return domain.ProvisionMailboxResult{}, domain.ErrNotConfigured
}

type retrievalHTTPCall struct {
	MailboxID string
	Query     domain.MessageQuery
}

type retrievalHTTPRetriever struct {
	mu         sync.Mutex
	calls      []retrievalHTTPCall
	receivedAt time.Time
}

func (r *retrievalHTTPRetriever) RetrievalMethods() []domain.RetrievalMethod {
	return []domain.RetrievalMethod{domain.RetrievalIMAPPassword}
}

func (r *retrievalHTTPRetriever) Retrieve(_ context.Context, mailbox domain.Mailbox, _ domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
	r.mu.Lock()
	r.calls = append(r.calls, retrievalHTTPCall{MailboxID: mailbox.ID, Query: query})
	r.mu.Unlock()
	return []domain.Message{
		{ID: "matching", InternetMessageID: "<matching@example.test>", RecipientAddresses: []string{query.RecipientAddress}, ReceivedAt: r.receivedAt},
		{ID: "other", InternetMessageID: "<other@example.test>", RecipientAddresses: []string{"other@example.net"}, ReceivedAt: r.receivedAt},
	}, nil
}

func (*retrievalHTTPRetriever) Refresh(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
	return domain.RefreshedCredential{}, domain.ErrNotConfigured
}

func (r *retrievalHTTPRetriever) snapshot() []retrievalHTTPCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]retrievalHTTPCall(nil), r.calls...)
}

type retrievalHTTPFixture struct {
	router    http.Handler
	store     *memory.Store
	pickup    *security.PickupKeyService
	retriever *retrievalHTTPRetriever
	mailbox1  domain.Mailbox
	mailbox2  domain.Mailbox
	alias1    domain.MailboxAlias
	alias2    domain.MailboxAlias
	now       time.Time
}

func TestAdminMessageRetrievalAuthAndQueryMapping(t *testing.T) {
	fixture := newRetrievalHTTPFixture(t)

	response := retrievalGET(fixture.router, "/api/v1/mailboxes/"+fixture.mailbox1.ID+"/messages", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing admin token status = %d, body=%s", response.Code, response.Body.String())
	}
	response = retrievalGET(fixture.router, "/api/v1/mailboxes/"+fixture.mailbox1.ID+"/messages", "wrong-token")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong admin token status = %d, body=%s", response.Code, response.Body.String())
	}

	after := fixture.now.Add(-time.Hour)
	parameters := url.Values{
		"after":     {after.Format(time.RFC3339)},
		"limit":     {"7"},
		"unread":    {"true"},
		"folder":    {"junk"},
		"method":    {string(domain.RetrievalIMAPPassword)},
		"page_size": {"11"},
		"max_pages": {"4"},
	}
	response = retrievalGET(fixture.router, "/api/v1/mailboxes/"+fixture.mailbox1.ID+"/messages?"+parameters.Encode(), retrievalHTTPAdminToken)
	body := decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(2) {
		t.Fatalf("mailbox response = %+v", body)
	}
	calls := fixture.retriever.snapshot()
	if len(calls) != 1 {
		t.Fatalf("retrieval calls = %+v", calls)
	}
	query := calls[0].Query
	if calls[0].MailboxID != fixture.mailbox1.ID || query.After == nil || !query.After.Equal(after) || query.Limit != 7 || !query.Unread || query.Folder != domain.MessageFolderJunk || query.RetrievalMethod != domain.RetrievalIMAPPassword || query.PageSize != 11 || query.MaxPages != 4 || query.RecipientAddress != fixture.mailbox1.NormalizedAddress {
		t.Fatalf("mapped query = %+v", calls[0])
	}

	response = retrievalGET(fixture.router, "/api/v1/mailbox-aliases/"+fixture.alias1.ID+"/messages?method=imap_password", retrievalHTTPAdminToken)
	body = decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(1) {
		t.Fatalf("alias response = %+v", body)
	}
	calls = fixture.retriever.snapshot()
	if len(calls) != 2 || calls[1].MailboxID != fixture.mailbox1.ID || calls[1].Query.RecipientAddress != fixture.alias1.NormalizedAddress {
		t.Fatalf("alias retrieval call = %+v", calls)
	}

	response = retrievalGET(fixture.router, "/api/v1/mailboxes/"+fixture.mailbox1.ID+"/messages?after=not-a-time", retrievalHTTPAdminToken)
	decodeRetrievalResponse(t, response, http.StatusBadRequest)

	response = retrievalGET(fixture.router, "/api/v1/pickup/messages/extra", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("non-exact pickup path bypassed admin auth: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPickupMessageRetrievalKeyStateAndAliasScope(t *testing.T) {
	fixture := newRetrievalHTTPFixture(t)
	ctx := context.Background()
	_, validToken, err := fixture.pickup.Issue(ctx, fixture.mailbox1.ID, "valid", nil)
	if err != nil {
		t.Fatal(err)
	}
	expiredAt := fixture.now.Add(-time.Minute)
	_, expiredToken, err := fixture.pickup.Issue(ctx, fixture.mailbox1.ID, "expired", &expiredAt)
	if err != nil {
		t.Fatal(err)
	}
	revokedKey, revokedToken, err := fixture.pickup.Issue(ctx, fixture.mailbox1.ID, "revoked", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.pickup.Revoke(ctx, fixture.mailbox1.ID, revokedKey.ID); err != nil {
		t.Fatal(err)
	}

	response := retrievalGET(fixture.router, "/api/v1/pickup/messages?method=imap_password&limit=3", validToken)
	body := decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(2) {
		t.Fatalf("pickup mailbox response = %+v", body)
	}
	calls := fixture.retriever.snapshot()
	if len(calls) != 1 || calls[0].MailboxID != fixture.mailbox1.ID || calls[0].Query.Limit != 3 {
		t.Fatalf("pickup mailbox call = %+v", calls)
	}

	response = retrievalGET(fixture.router, "/api/v1/pickup/messages?alias_id="+url.QueryEscape(fixture.alias1.ID)+"&method=imap_password", validToken)
	body = decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(1) {
		t.Fatalf("pickup alias response = %+v", body)
	}
	calls = fixture.retriever.snapshot()
	if len(calls) != 2 || calls[1].Query.RecipientAddress != fixture.alias1.NormalizedAddress {
		t.Fatalf("pickup alias call = %+v", calls)
	}

	response = retrievalGET(fixture.router, "/api/v1/pickup/messages?alias_id="+url.QueryEscape(fixture.alias2.ID), validToken)
	decodeRetrievalResponse(t, response, http.StatusForbidden)
	if len(fixture.retriever.snapshot()) != 2 {
		t.Fatal("cross-mailbox alias reached the retriever")
	}

	response = retrievalGET(fixture.router, "/api/v1/pickup/messages", validToken+"tampered")
	decodeRetrievalResponse(t, response, http.StatusUnauthorized)
	response = retrievalGET(fixture.router, "/api/v1/pickup/messages", "")
	decodeRetrievalResponse(t, response, http.StatusUnauthorized)
	response = retrievalGET(fixture.router, "/api/v1/pickup/messages", expiredToken)
	expiredBody := decodeRetrievalResponse(t, response, http.StatusGone)
	if expiredBody["error"] != "pickup_key_expired" {
		t.Fatalf("expired response = %+v", expiredBody)
	}
	response = retrievalGET(fixture.router, "/api/v1/pickup/messages", revokedToken)
	revokedBody := decodeRetrievalResponse(t, response, http.StatusGone)
	if revokedBody["error"] != "pickup_key_revoked" {
		t.Fatalf("revoked response = %+v", revokedBody)
	}
}

func TestCachedMessageHTTPRoutesSyncMethodAndPreserveAliasIsolation(t *testing.T) {
	fixture := newRetrievalHTTPFixture(t)
	path := "/api/v1/mailbox-aliases/" + fixture.alias1.ID + "/cached-messages?folder=Junk&limit=100"
	response := retrievalRequest(fixture.router, http.MethodGet, path, retrievalHTTPAdminToken)
	body := decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(0) {
		t.Fatalf("initial cache = %+v", body)
	}

	syncPath := "/api/v1/mailbox-aliases/" + fixture.alias1.ID + "/messages/sync?folder=Junk&method=imap_password&limit=100"
	response = retrievalRequest(fixture.router, http.MethodPost, syncPath, "")
	decodeRetrievalResponse(t, response, http.StatusUnauthorized)
	response = retrievalRequest(fixture.router, http.MethodPost, syncPath, retrievalHTTPAdminToken)
	body = decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(1) || body["new_count"] != float64(1) {
		t.Fatalf("synchronized alias cache = %+v", body)
	}
	calls := fixture.retriever.snapshot()
	if len(calls) != 1 || calls[0].Query.Folder != domain.MessageFolderJunk || calls[0].Query.RetrievalMethod != domain.RetrievalIMAPPassword || calls[0].Query.RecipientAddress != fixture.alias1.NormalizedAddress {
		t.Fatalf("cache sync query = %+v", calls)
	}

	response = retrievalRequest(fixture.router, http.MethodGet, path, retrievalHTTPAdminToken)
	body = decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(1) {
		t.Fatalf("cached alias messages = %+v", body)
	}
	response = retrievalRequest(fixture.router, http.MethodGet, "/api/v1/mailbox-aliases/"+fixture.alias2.ID+"/cached-messages?folder=Junk", retrievalHTTPAdminToken)
	body = decodeRetrievalResponse(t, response, http.StatusOK)
	if body["count"] != float64(0) {
		t.Fatalf("sibling alias cache leaked messages = %+v", body)
	}
}

func newRetrievalHTTPFixture(t *testing.T) retrievalHTTPFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 16, 0, 0, 0, time.UTC)
	store := memory.New()
	retriever := &retrievalHTTPRetriever{receivedAt: now.Add(-time.Minute)}
	provider := retrievalHTTPProvider{}
	registry, err := providers.NewRegistry(ports.ProviderRegistration{Provider: provider, Retriever: retriever})
	if err != nil {
		t.Fatal(err)
	}
	mailboxes, _ := service.NewMailboxService(store, registry)
	accounts, _ := service.NewAccountService(store, store)
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	backups, _ := service.NewBackupService(store, broker)
	pickup, _ := security.NewPickupKeyService(store, []byte("01234567890123456789012345678901"))
	pickup.SetClock(func() time.Time { return now })
	formats, _ := service.NewFormatService(store, registry)
	if err := formats.EnsureBuiltins(ctx); err != nil {
		t.Fatal(err)
	}
	details, _ := service.NewMailboxDetailService(store, store, broker)
	transfers, _ := service.NewImportExportService(store, store, store, registry, broker)
	retrieval, _ := service.NewMessageRetrievalService(store, registry, providers.MessageMatchesRecipient)
	retrieval.SetClock(func() time.Time { return now })
	messageCache, _ := service.NewMessageCacheService(store, store, retrieval)
	messageCache.SetClock(func() time.Time { return now })

	mailbox1 := domain.Mailbox{
		ID: "mbx_microsoft_http_one", Provider: domain.ProviderMicrosoft,
		Address: "one@example.com", NormalizedAddress: "one@example.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	mailbox2 := domain.Mailbox{
		ID: "mbx_microsoft_http_two", Provider: domain.ProviderMicrosoft,
		Address: "two@example.com", NormalizedAddress: "two@example.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	for _, mailbox := range []domain.Mailbox{mailbox1, mailbox2} {
		if err := store.CreateMailbox(ctx, mailbox); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertCredential(ctx, domain.MailboxCredential{
			ID: "cred_" + mailbox.ID, MailboxID: mailbox.ID, Kind: domain.CredentialIMAPPassword,
			EncryptedSecret: []byte("sealed-fixture"), KeyVersion: "v1", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	alias1 := domain.MailboxAlias{
		ID: "alias_http_one", MailboxID: mailbox1.ID, Provider: domain.ProviderCloudflareRoute,
		Address: "alias-one@example.net", NormalizedAddress: "alias-one@example.net",
		Kind: domain.AliasKindForward, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	alias2 := domain.MailboxAlias{
		ID: "alias_http_two", MailboxID: mailbox2.ID, Provider: domain.ProviderCloudflareRoute,
		Address: "alias-two@example.net", NormalizedAddress: "alias-two@example.net",
		Kind: domain.AliasKindForward, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	for _, alias := range []domain.MailboxAlias{alias1, alias2} {
		if err := store.CreateAlias(ctx, alias); err != nil {
			t.Fatal(err)
		}
	}

	router, err := httpapi.NewRouter(httpapi.Dependencies{
		Health: store, Providers: registry, AliasReader: store, Mailboxes: mailboxes,
		PickupKeys: pickup, Accounts: accounts, Backups: backups, Details: details,
		Formats: formats, Transfers: transfers, Retrieval: retrieval,
		MessageCache: messageCache,
		AdminToken:   retrievalHTTPAdminToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	return retrievalHTTPFixture{
		router: router, store: store, pickup: pickup, retriever: retriever,
		mailbox1: mailbox1, mailbox2: mailbox2, alias1: alias1, alias2: alias2, now: now,
	}
}

func retrievalGET(handler http.Handler, path, bearer string) *httptest.ResponseRecorder {
	return retrievalRequest(handler, http.MethodGet, path, bearer)
}

func retrievalRequest(handler http.Handler, method, path, bearer string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeRetrievalResponse(t *testing.T, response *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
	return body
}

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/httpapi"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

type localCloudflareAliasProvider struct {
	providers.CloudflareRouteAdapter
}

func (p localCloudflareAliasProvider) Descriptor(ctx context.Context) domain.ProviderDescriptor {
	descriptor := p.CloudflareRouteAdapter.Descriptor(ctx)
	descriptor.Capabilities.ProvisionMailbox = false
	descriptor.Capabilities.ManageAliases = false
	return descriptor
}

func TestMailboxOverviewAndBackupCompatibilityFlow(t *testing.T) {
	store := memory.New()
	cloudflare := providers.CloudflareRouteAdapter{}
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: providers.GmailAdapter{}, Retriever: providers.GmailAdapter{}},
		ports.ProviderRegistration{Provider: localCloudflareAliasProvider{CloudflareRouteAdapter: cloudflare}, Retriever: cloudflare},
	)
	if err != nil {
		t.Fatal(err)
	}
	mailboxes, _ := service.NewMailboxService(store, registry)
	accounts, _ := service.NewAccountService(store, store)
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	backups, _ := service.NewBackupService(store, broker)
	pickupKeys, _ := security.NewPickupKeyService(store, []byte("01234567890123456789012345678901"))
	formats, _ := service.NewFormatService(store, registry)
	if err := formats.EnsureBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	details, _ := service.NewMailboxDetailService(store, store, broker)
	transfers, _ := service.NewImportExportService(store, store, store, registry, broker)
	retrieval, _ := service.NewMessageRetrievalService(store, registry, providers.MessageMatchesRecipient)
	tokenRefreshSettings, _ := service.NewTokenRefreshSettingsService(store)
	deps := httpapi.Dependencies{
		Health: store, Providers: registry, AliasReader: store, Mailboxes: mailboxes,
		PickupKeys: pickupKeys, Accounts: accounts, Backups: backups,
		Details: details, Formats: formats, Transfers: transfers, Retrieval: retrieval,
		TokenRefreshSettings: tokenRefreshSettings,
	}
	router, err := httpapi.NewRouter(deps)
	if err != nil {
		t.Fatal(err)
	}
	protectedDeps := deps
	protectedDeps.AdminToken = "test-admin-token"
	protectedRouter, err := httpapi.NewRouter(protectedDeps)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	protectedRouter.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("protected API status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer test-admin-token")
	authorized := httptest.NewRecorder()
	protectedRouter.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized API status = %d, want %d", authorized.Code, http.StatusOK)
	}

	mailboxResponse := requestJSON(t, router, http.MethodPost, "/api/v1/mailboxes", `{"provider":"gmail","address":"Owner@Gmail.com"}`, http.StatusCreated)
	mailboxID := nestedString(t, mailboxResponse, "id")
	requestJSON(t, router, http.MethodPost, "/api/v1/mailboxes/"+mailboxID+"/aliases", `{"provider":"cloudflare_route","address":"chat@example.net","kind":"forward"}`, http.StatusCreated)
	keyResponse := requestJSON(t, router, http.MethodPost, "/api/v1/mailboxes/"+mailboxID+"/pickup-keys", `{"label":"reader"}`, http.StatusCreated)
	token := nestedString(t, keyResponse, "token")
	requestJSON(t, router, http.MethodPost, "/api/v1/mailboxes/"+mailboxID+"/pickup-keys", `{"label":"expired-newer","expires_at":"2000-01-01T00:00:00Z"}`, http.StatusCreated)

	overview := requestJSON(t, router, http.MethodGet, "/api/v1/mailboxes/overview", "", http.StatusOK)
	serialized, _ := json.Marshal(overview)
	if strings.Contains(string(serialized), token) {
		t.Fatal("overview leaked the raw pickup token")
	}
	mailboxItems := overview["mailboxes"].([]any)
	mailbox := mailboxItems[0].(map[string]any)
	if mailbox["provider"] != "google" {
		t.Fatalf("overview provider = %v, want google", mailbox["provider"])
	}
	if mailbox["retrieval_key"].(map[string]any)["status"] != "ready" {
		t.Fatalf("overview retrieval key = %+v, want an older valid key to win", mailbox["retrieval_key"])
	}
	if mailbox["auth"].(map[string]any)["auto_refresh"] != true {
		t.Fatalf("managed mailbox auto refresh = %+v, want persisted default enabled", mailbox["auth"])
	}
	children := mailbox["children"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["provider"] != "cloudflare" {
		t.Fatalf("overview children = %+v", children)
	}
	if children[0].(map[string]any)["forwarding"].(map[string]any)["verified"] != false {
		t.Fatalf("forwarding must remain unverified until the provider route confirms it: %+v", children[0])
	}
	if children[0].(map[string]any)["auth"].(map[string]any)["auto_refresh"] != false {
		t.Fatalf("forward-only alias reported token refresh: %+v", children[0])
	}
	currentSettings, err := tokenRefreshSettings.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tokenRefreshSettings.Update(context.Background(), service.UpdateTokenRefreshSettingsInput{
		Enabled: false, LeadTimeMinutes: 5, Version: currentSettings.Version,
	}); err != nil {
		t.Fatal(err)
	}
	disabledOverview := requestJSON(t, router, http.MethodGet, "/api/v1/mailboxes/overview", "", http.StatusOK)
	disabledMailbox := disabledOverview["mailboxes"].([]any)[0].(map[string]any)
	if disabledMailbox["auth"].(map[string]any)["auto_refresh"] != false {
		t.Fatalf("disabled auto refresh was not reflected in overview: %+v", disabledMailbox["auth"])
	}

	unrouted := requestJSON(t, router, http.MethodPost, "/api/v1/platform-accounts", `{"platform":"chatgpt","status":"pending"}`, http.StatusCreated)
	if _, exists := unrouted["mailbox_id"]; exists {
		t.Fatalf("unrouted platform account exposed an empty mailbox_id: %+v", unrouted)
	}
	accountsResponse := requestJSON(t, router, http.MethodGet, "/api/v1/platform-accounts?platform=chatgpt", "", http.StatusOK)
	accountItems := accountsResponse["items"].([]any)
	if len(accountItems) != 1 || accountItems[0].(map[string]any)["id"] != unrouted["id"] {
		t.Fatalf("platform account list = %+v", accountItems)
	}

	targetResponse := requestJSON(t, router, http.MethodPost, "/api/v1/backups/targets", `{"name":"primary-s3","kind":"s3","config":{"bucket":"fixture"}}`, http.StatusCreated)
	targetJSON, _ := json.Marshal(targetResponse)
	if strings.Contains(string(targetJSON), "fixture") {
		t.Fatalf("backup response exposed target config: %s", targetJSON)
	}
	if _, exposed := targetResponse["config"]; exposed {
		t.Fatalf("backup response exposed target config: %s", targetJSON)
	}
	backupOverview := requestJSON(t, router, http.MethodGet, "/api/v1/mailboxes/overview", "", http.StatusOK)
	destinations := backupOverview["backup"].(map[string]any)["destinations"].([]any)
	if len(destinations) != 1 || destinations[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("new backup target status = %+v, want pending", destinations)
	}
	requestJSON(t, router, http.MethodPost, "/api/v1/backups", `{"reason":"manual"}`, http.StatusAccepted)
	if err := store.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, path, body string, status int) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.Code, status, response.Body.String())
	}
	if response.Body.Len() == 0 {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s %s response: %v", method, path, err)
	}
	return value
}

func nestedString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	text, ok := value[key].(string)
	if !ok || text == "" {
		t.Fatalf("field %q = %#v", key, value[key])
	}
	return text
}

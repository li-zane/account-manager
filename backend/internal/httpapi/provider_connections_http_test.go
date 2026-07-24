package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/httpapi"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestProviderConnectionHTTPAdminFlowAndRedaction(t *testing.T) {
	router := newProviderConnectionTestRouter(t, "provider-admin-token")

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/provider-connections", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized provider settings status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	created, raw := providerConnectionRequestJSON(t, router, http.MethodPut, "/api/v1/provider-connections/cloudflare_route", `{
		"account_id":"account-id",
		"zone_id":"zone-id",
		"zone_name":"example.test",
		"api_base_url":"https://api.cloudflare.test/client/v4",
		"api_token":"cloudflare-http-secret",
		"enabled":true,
		"version":0
	}`, http.StatusOK)
	assertProviderConnectionResponseRedacted(t, raw)
	if created["configured"] != true || created["provider"] != "cloudflare_route" {
		t.Fatalf("created provider settings = %+v", created)
	}
	version, ok := created["version"].(float64)
	if !ok || version != 1 {
		t.Fatalf("created provider settings version = %#v", created["version"])
	}

	got, raw := providerConnectionRequestJSON(t, router, http.MethodGet, "/api/v1/provider-connections/cloudflare", "", http.StatusOK)
	assertProviderConnectionResponseRedacted(t, raw)
	if got["account_id"] != "account-id" || got["zone_id"] != "zone-id" || got["configured"] != true {
		t.Fatalf("provider settings GET = %+v", got)
	}

	listed, raw := providerConnectionRequestJSON(t, router, http.MethodGet, "/api/v1/provider-connections", "", http.StatusOK)
	assertProviderConnectionResponseRedacted(t, raw)
	items, ok := listed["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("provider settings list = %+v", listed)
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["provider"] != "cloudflare_route" || item["configured"] != true {
		t.Fatalf("provider settings list item = %+v", items[0])
	}

	updated, raw := providerConnectionRequestJSON(t, router, http.MethodPut, "/api/v1/provider-connections/cloudflare_route", `{
		"account_id":"account-id",
		"zone_id":"zone-id",
		"zone_name":"updated.example.test",
		"api_base_url":"https://api.cloudflare.test/client/v4",
		"enabled":true,
		"version":1
	}`, http.StatusOK)
	assertProviderConnectionResponseRedacted(t, raw)
	if updated["version"] != float64(2) || updated["zone_name"] != "updated.example.test" || updated["configured"] != true {
		t.Fatalf("updated provider settings = %+v", updated)
	}

	_, raw = providerConnectionRequestJSON(t, router, http.MethodPut, "/api/v1/provider-connections/cloudflare_route", `{
		"account_id":"account-id",
		"zone_id":"zone-id",
		"zone_name":"stale.example.test",
		"enabled":true,
		"version":1
	}`, http.StatusConflict)
	assertProviderConnectionResponseRedacted(t, raw)
}

func newProviderConnectionTestRouter(t *testing.T, adminToken string) http.Handler {
	t.Helper()
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
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "provider-http-v1")
	backups, _ := service.NewBackupService(store, broker)
	pickupKeys, _ := security.NewPickupKeyService(store, []byte("01234567890123456789012345678901"))
	formats, _ := service.NewFormatService(store, registry)
	if err := formats.EnsureBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	details, _ := service.NewMailboxDetailService(store, store, broker)
	transfers, _ := service.NewImportExportService(store, store, store, registry, broker)
	retrieval, _ := service.NewMessageRetrievalService(store, registry, providers.MessageMatchesRecipient)
	providerConnections, err := service.NewProviderConnectionService(store, broker)
	if err != nil {
		t.Fatal(err)
	}
	tokenRefreshSettings, err := service.NewTokenRefreshSettingsService(store)
	if err != nil {
		t.Fatal(err)
	}
	router, err := httpapi.NewRouter(httpapi.Dependencies{
		Health: store, Providers: registry, AliasReader: store, Mailboxes: mailboxes,
		PickupKeys: pickupKeys, Accounts: accounts, Backups: backups,
		Details: details, Formats: formats, Transfers: transfers, Retrieval: retrieval,
		ProviderConnections: providerConnections, TokenRefreshSettings: tokenRefreshSettings,
		AdminToken: adminToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func providerConnectionRequestJSON(t *testing.T, handler http.Handler, method, path, body string, status int) (map[string]any, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer provider-admin-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.Code, status, response.Body.String())
	}
	raw := response.Body.String()
	if raw == "" {
		return nil, raw
	}
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s %s response: %v; body=%s", method, path, err, raw)
	}
	return value, raw
}

func assertProviderConnectionResponseRedacted(t *testing.T, raw string) {
	t.Helper()
	for _, protected := range []string{"cloudflare-http-secret", "api_token", "encrypted_config", "key_version", "provider-http-v1"} {
		if strings.Contains(raw, protected) {
			t.Fatalf("provider settings response contains protected value %q: %s", protected, raw)
		}
	}
}

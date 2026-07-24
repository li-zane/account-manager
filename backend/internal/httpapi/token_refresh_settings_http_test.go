package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenRefreshSettingsHTTPAdminAndConflict(t *testing.T) {
	router := newProviderConnectionTestRouter(t, "provider-admin-token")

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		request := httptest.NewRequest(method, "/api/v1/settings/token-refresh", nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized %s status = %d, want %d", method, response.Code, http.StatusUnauthorized)
		}
	}

	current, _ := providerConnectionRequestJSON(t, router, http.MethodGet, "/api/v1/settings/token-refresh", "", http.StatusOK)
	if current["enabled"] != true || current["lead_time_minutes"] != float64(5) || current["version"] != float64(1) || current["updated_at"] == "" {
		t.Fatalf("current token refresh settings = %+v", current)
	}
	updated, _ := providerConnectionRequestJSON(t, router, http.MethodPut, "/api/v1/settings/token-refresh", `{
		"enabled":false,
		"lead_time_minutes":10,
		"version":1
	}`, http.StatusOK)
	if updated["enabled"] != false || updated["lead_time_minutes"] != float64(10) || updated["version"] != float64(2) || updated["updated_at"] == "" {
		t.Fatalf("updated token refresh settings = %+v", updated)
	}
	stale, _ := providerConnectionRequestJSON(t, router, http.MethodPut, "/api/v1/settings/token-refresh", `{
		"enabled":true,
		"lead_time_minutes":5,
		"version":1
	}`, http.StatusConflict)
	if stale["error"] != "conflict" {
		t.Fatalf("stale update response = %+v", stale)
	}
	invalid, _ := providerConnectionRequestJSON(t, router, http.MethodPut, "/api/v1/settings/token-refresh", `{
		"enabled":true,
		"lead_time_minutes":31,
		"version":2
	}`, http.StatusBadRequest)
	if invalid["error"] != "invalid_request" {
		t.Fatalf("invalid update response = %+v", invalid)
	}
}

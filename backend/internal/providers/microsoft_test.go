package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

type microsoftRoundTripFunc func(*http.Request) (*http.Response, error)

func (f microsoftRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestMicrosoftRefreshRotatesTokenAndTracksExpiry(t *testing.T) {
	const (
		oldRefreshToken = "old-refresh-token"
		newRefreshToken = "rotated-refresh-token"
	)
	now := time.Date(2026, 7, 24, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "client-id" || r.Form.Get("refresh_token") != oldRefreshToken || r.Form.Get("scope") != defaultMicrosoftGraphScope {
			t.Fatalf("unexpected token form: %v", r.Form)
		}
		writeJSON(t, w, map[string]any{
			"access_token": "new-graph-access-token", "refresh_token": newRefreshToken,
			"token_type": "Bearer", "scope": "Mail.Read offline_access", "expires_in": 3600,
		})
	}))
	t.Cleanup(server.Close)

	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftGraphOAuth, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion,
		ClientID:      "client-id",
		RefreshToken:  oldRefreshToken,
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL, Now: func() time.Time { return now }}, broker, server.Client())
	refreshed, err := adapter.Refresh(context.Background(), microsoftMailbox(), credential)
	if err != nil {
		t.Fatal(err)
	}
	wantExpiry := now.Add(time.Hour)
	wantRefreshAfter := wantExpiry.Add(-5 * time.Minute)
	if refreshed.ExpiresAt == nil || !refreshed.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expires_at = %v, want %v", refreshed.ExpiresAt, wantExpiry)
	}
	if refreshed.RefreshAfter == nil || !refreshed.RefreshAfter.Equal(wantRefreshAfter) {
		t.Fatalf("refresh_after = %v, want %v", refreshed.RefreshAfter, wantRefreshAfter)
	}
	if refreshed.KeyVersion != "test-key-v2" {
		t.Fatalf("key version = %q", refreshed.KeyVersion)
	}
	secret := openMicrosoftCredential(t, broker, refreshed.EncryptedSecret, refreshed.KeyVersion)
	if secret.GraphAccessToken != "new-graph-access-token" || secret.RefreshToken != newRefreshToken || secret.GraphRefreshToken != "" || secret.IMAPRefreshToken != "" {
		t.Fatalf("rotated credential was not persisted: %+v", redactMicrosoftSecret(secret))
	}
	if secret.GraphTokenExpiresAt == nil || !secret.GraphTokenExpiresAt.Equal(wantExpiry) {
		t.Fatalf("graph token expiry = %v", secret.GraphTokenExpiresAt)
	}
}

func TestMicrosoftEnsureAccessTokenReusesValidMethodToken(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("valid AT must not call token endpoint") }))
	defer server.Close()
	broker := testSecretBroker{}
	expires := now.Add(time.Hour)
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion, ClientID: "client-id", RefreshToken: "rt",
		Password: "mail-password", GraphAccessToken: "graph-at", GraphTokenExpiresAt: &expires,
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL, Now: func() time.Time { return now }}, broker, server.Client())
	_, changed, err := adapter.EnsureAccessToken(context.Background(), microsoftMailbox(), credential, domain.RetrievalMicrosoftGraph, false)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestMicrosoftMethodRefreshPreservesMailboxPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"access_token": "new-at", "refresh_token": "new-rt", "expires_in": 3600})
	}))
	defer server.Close()
	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{SchemaVersion: domain.MicrosoftCredentialSecretVersion, ClientID: "client", RefreshToken: "rt", Password: "mail-password"})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL}, broker, server.Client())
	refreshed, changed, err := adapter.EnsureAccessToken(context.Background(), microsoftMailbox(), credential, domain.RetrievalMicrosoftGraph, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	secret := openMicrosoftCredential(t, broker, refreshed.EncryptedSecret, refreshed.KeyVersion)
	if secret.Password != "mail-password" {
		t.Fatal("method refresh dropped mailbox password")
	}
}

func TestMicrosoftGraphRefreshFallsBackToDefaultGrant(t *testing.T) {
	var scopes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		scope := r.Form.Get("scope")
		scopes = append(scopes, scope)
		if scope == defaultMicrosoftGraphScope {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		if scope != defaultMicrosoftGraphFallback {
			t.Fatalf("fallback scope = %q", scope)
		}
		writeJSON(t, w, map[string]any{"access_token": "graph-at", "refresh_token": "rotated-rt", "scope": "Mail.Read", "expires_in": 3600})
	}))
	t.Cleanup(server.Close)
	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion, ClientID: "client", RefreshToken: "rt",
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL}, broker, server.Client())
	refreshed, changed, err := adapter.EnsureAccessToken(context.Background(), microsoftMailbox(), credential, domain.RetrievalMicrosoftGraph, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if len(scopes) != 2 || scopes[0] != defaultMicrosoftGraphScope || scopes[1] != defaultMicrosoftGraphFallback {
		t.Fatalf("scopes = %v", scopes)
	}
	secret := openMicrosoftCredential(t, broker, refreshed.EncryptedSecret, refreshed.KeyVersion)
	if secret.GraphAccessToken != "graph-at" || secret.RefreshToken != "rotated-rt" {
		t.Fatal("Graph fallback result was not persisted")
	}
}

func TestMicrosoftGraphDeltaProcessesTombstonesAndCursor(t *testing.T) {
	const token = "graph-delta-at"
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Prefer") != `IdType="ImmutableId"` {
			t.Fatalf("Prefer=%q", r.Header.Get("Prefer"))
		}
		if r.URL.Path == "/page2" {
			writeJSON(t, w, map[string]any{"value": []any{}, "@odata.deltaLink": baseURL + "/me/mailFolders/inbox/messages/delta?$deltatoken=done"})
			return
		}
		removed := graphFixture("removed-id", "primary@outlook.com", nil)
		removed["@removed"] = map[string]string{"reason": "deleted"}
		writeJSON(t, w, map[string]any{"value": []any{graphFixture("added-id", "primary@outlook.com", nil), removed}, "@odata.nextLink": baseURL + "/page2"})
	}))
	defer server.Close()
	baseURL = server.URL
	broker := testSecretBroker{}
	expires := time.Now().Add(time.Hour)
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{SchemaVersion: domain.MicrosoftCredentialSecretVersion, GraphAccessToken: token, GraphTokenExpiresAt: &expires})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{GraphBaseURL: server.URL}, broker, server.Client())
	result, err := adapter.SyncIncremental(context.Background(), microsoftMailbox(), credential, domain.MessageSyncRequest{Method: domain.RetrievalMicrosoftGraph, Folder: domain.MessageFolderInbox, Limit: 50, PageSize: 10, MaxPages: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Messages[0].ID != "added-id" || len(result.DeletedProviderMessageIDs) != 1 || result.DeletedProviderMessageIDs[0] != "removed-id" || !strings.Contains(result.Cursor, "$deltatoken=done") {
		t.Fatalf("delta result=%+v", result)
	}
}

func TestMicrosoftGraphRetriesEOFOnce(t *testing.T) {
	var calls int
	client := &http.Client{Transport: microsoftRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, io.EOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"value":[]}`)),
			Request:    request,
		}, nil
	})}
	adapter := NewMicrosoftAdapter(MicrosoftConfig{}, testSecretBroker{}, client)
	target, err := url.Parse("https://graph.microsoft.com/v1.0/me/messages")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.fetchGraphPage(context.Background(), "access-token", target); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestMicrosoftDualCredentialRefreshesGraphAndIMAPInSharedChain(t *testing.T) {
	var mu sync.Mutex
	requests := make([]url.Values, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		requests = append(requests, cloneValues(r.Form))
		mu.Unlock()
		switch r.Form.Get("refresh_token") {
		case "shared-rt":
			writeJSON(t, w, map[string]any{"access_token": "graph-at", "refresh_token": "graph-rt-2", "scope": "graph-scope", "expires_in": 3600})
		case "graph-rt-2":
			writeJSON(t, w, map[string]any{"access_token": "imap-at", "refresh_token": "imap-rt-3", "scope": "imap-scope", "expires_in": 1800})
		default:
			t.Fatalf("unexpected refresh token")
		}
	}))
	t.Cleanup(server.Close)

	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion,
		ClientID:      "client-id",
		RefreshToken:  "shared-rt",
		GraphScope:    "requested-graph-scope",
		IMAPScope:     "requested-imap-scope",
	})
	now := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL, Now: func() time.Time { return now }}, broker, server.Client())
	refreshed, err := adapter.Refresh(context.Background(), microsoftMailbox(), credential)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0].Get("scope") != "requested-graph-scope" || requests[1].Get("scope") != "requested-imap-scope" {
		t.Fatalf("refresh requests = %v", requests)
	}
	secret := openMicrosoftCredential(t, broker, refreshed.EncryptedSecret, refreshed.KeyVersion)
	if secret.RefreshToken != "imap-rt-3" || secret.GraphRefreshToken != "" || secret.IMAPRefreshToken != "" {
		t.Fatalf("dual refresh tokens = %+v", redactMicrosoftSecret(secret))
	}
	if secret.GraphAccessToken != "graph-at" || secret.IMAPAccessToken != "imap-at" {
		t.Fatalf("dual access tokens were not stored")
	}
	wantExpiry := now.Add(30 * time.Minute)
	if refreshed.ExpiresAt == nil || !refreshed.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("dual expiry = %v, want earliest %v", refreshed.ExpiresAt, wantExpiry)
	}
}

func TestMicrosoftDualCredentialReplacesLegacyRESTScopeWithOfficialScopes(t *testing.T) {
	var requestedScopes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		requestedScopes = append(requestedScopes, r.Form.Get("scope"))
		writeJSON(t, w, map[string]any{
			"access_token": "access-token", "refresh_token": "rotated-token", "expires_in": 3600,
		})
	}))
	t.Cleanup(server.Close)
	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion,
		ClientID:      "client-id",
		RefreshToken:  "refresh-token",
		GraphScope:    "https://outlook.office.com/Mail.Read offline_access",
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL}, broker, server.Client())
	if _, err := adapter.Refresh(context.Background(), microsoftMailbox(), credential); err != nil {
		t.Fatal(err)
	}
	if len(requestedScopes) != 2 || requestedScopes[0] != defaultMicrosoftGraphScope || requestedScopes[1] != defaultMicrosoftIMAPScope {
		t.Fatalf("requested scopes = %v", requestedScopes)
	}
}

func TestMicrosoftDualCredentialReturnsGraphCheckpointWhenIMAPRefreshFails(t *testing.T) {
	const (
		oldRefreshToken   = "shared-rt-sensitive"
		graphRefreshToken = "graph-rt-rotated-sensitive"
	)
	var mu sync.Mutex
	requests := make([]url.Values, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		requests = append(requests, cloneValues(r.Form))
		mu.Unlock()
		switch r.Form.Get("refresh_token") {
		case oldRefreshToken:
			writeJSON(t, w, map[string]any{
				"access_token": "new-graph-at", "refresh_token": graphRefreshToken,
				"scope": "graph-scope", "expires_in": 3600,
			})
		case graphRefreshToken:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"temporarily_unavailable","error_description":"rejected %s"}`, graphRefreshToken)
		default:
			t.Fatalf("unexpected refresh token")
		}
	}))
	t.Cleanup(server.Close)

	now := time.Date(2026, 7, 24, 9, 30, 0, 0, time.UTC)
	imapExpiry := now.Add(20 * time.Minute)
	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{
		SchemaVersion:      domain.MicrosoftCredentialSecretVersion,
		ClientID:           "client-id",
		RefreshToken:       oldRefreshToken,
		GraphScope:         "requested-graph-scope",
		IMAPScope:          "requested-imap-scope",
		IMAPAccessToken:    "old-imap-at",
		IMAPTokenExpiresAt: &imapExpiry,
	})
	staleAggregateExpiry := now.Add(-time.Minute)
	credential.ExpiresAt = &staleAggregateExpiry
	adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL, Now: func() time.Time { return now }}, broker, server.Client())
	checkpoint, err := adapter.Refresh(context.Background(), microsoftMailbox(), credential)
	if err == nil || strings.Contains(err.Error(), oldRefreshToken) || strings.Contains(err.Error(), graphRefreshToken) {
		t.Fatalf("refresh error = %v", err)
	}
	if !checkpoint.PersistOnError || len(checkpoint.EncryptedSecret) == 0 || checkpoint.KeyVersion == "" {
		t.Fatalf("partial checkpoint = %+v", checkpoint)
	}
	secret := openMicrosoftCredential(t, broker, checkpoint.EncryptedSecret, checkpoint.KeyVersion)
	if secret.RefreshToken != graphRefreshToken || secret.GraphRefreshToken != "" || secret.IMAPRefreshToken != "" {
		t.Fatalf("checkpoint refresh tokens = %+v", redactMicrosoftSecret(secret))
	}
	if secret.GraphAccessToken != "new-graph-at" || secret.IMAPAccessToken != "old-imap-at" {
		t.Fatalf("checkpoint access tokens were not preserved")
	}
	if checkpoint.ExpiresAt == nil || !checkpoint.ExpiresAt.Equal(imapExpiry) {
		t.Fatalf("checkpoint expiry = %v, want %v", checkpoint.ExpiresAt, imapExpiry)
	}
	wantRefreshAfter := imapExpiry.Add(-5 * time.Minute)
	if checkpoint.RefreshAfter == nil || !checkpoint.RefreshAfter.Equal(wantRefreshAfter) {
		t.Fatalf("checkpoint refresh_after = %v, want %v", checkpoint.RefreshAfter, wantRefreshAfter)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0].Get("refresh_token") != oldRefreshToken || requests[1].Get("refresh_token") != graphRefreshToken {
		t.Fatalf("refresh chain = %v", requests)
	}
}

func TestMicrosoftGraphPaginationAndRecipientIsolation(t *testing.T) {
	const accessToken = "graph-access-token"
	var mu sync.Mutex
	pageRequests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+accessToken {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if !strings.Contains(r.URL.Path, "/v1.0/me/mailFolders/inbox/messages") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		page := r.URL.Query().Get("page")
		mu.Lock()
		pageRequests[page]++
		mu.Unlock()
		if page == "2" {
			writeJSON(t, w, map[string]any{"value": []any{
				graphFixture("alias-a-message", "destination@gmail.com", []map[string]string{
					{"name": "X-Original-To", "value": "unrelated@rainynight.me"},
					{"name": "X-Original-To", "value": "Alias.A@rainynight.me"},
				}),
				graphFixture("missing-evidence", "destination@gmail.com", nil),
			}})
			return
		}
		if r.URL.Query().Get("$top") != "1" || !strings.Contains(r.URL.Query().Get("$select"), "internetMessageHeaders") {
			t.Fatalf("Graph query = %v", r.URL.Query())
		}
		writeJSON(t, w, map[string]any{
			"value": []any{
				graphFixture("primary-message", "primary@outlook.com", nil),
				graphFixture("alias-b-message", "destination@gmail.com", []map[string]string{{"name": "Delivered-To", "value": "alias.b@rainynight.me"}}),
			},
			"@odata.nextLink": serverURLFromRequest(r) + "/v1.0/me/mailFolders/inbox/messages?page=2",
		})
	}))
	t.Cleanup(server.Close)

	broker := testSecretBroker{}
	expiresAt := time.Now().UTC().Add(time.Hour)
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftGraphOAuth, domain.MicrosoftCredentialSecret{
		SchemaVersion:       domain.MicrosoftCredentialSecretVersion,
		ClientID:            "client-id",
		GraphAccessToken:    accessToken,
		GraphTokenExpiresAt: &expiresAt,
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{GraphBaseURL: server.URL + "/v1.0"}, broker, server.Client())

	messages, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{
		RecipientAddress: "alias.a@rainynight.me", Limit: 1, PageSize: 1, MaxPages: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "alias-a-message" {
		t.Fatalf("alias A messages = %+v", messages)
	}
	if values := messages[0].Headers["X-Original-To"]; len(values) != 2 {
		t.Fatalf("repeated headers = %#v", messages[0].Headers)
	}
	if !MessageMatchesRecipient(messages[0], "ALIAS.A@rainynight.me") || MessageMatchesRecipient(messages[0], "alias.b@rainynight.me") {
		t.Fatalf("recipient exact match failed: %v", messages[0].RecipientAddresses)
	}

	messages, err = adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{
		RecipientAddress: "alias.b@rainynight.me", Limit: 1, PageSize: 1, MaxPages: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "alias-b-message" {
		t.Fatalf("alias B messages = %+v", messages)
	}

	messages, err = adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{
		RecipientAddress: "alias.c@rainynight.me", Limit: 5, PageSize: 1, MaxPages: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages without alias C evidence passed filter: %+v", messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if pageRequests["2"] < 2 {
		t.Fatalf("second page requests = %d", pageRequests["2"])
	}
}

func TestMicrosoftGraphJunkFolderAndQueryFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/mailFolders/junkemail/messages") {
			t.Fatalf("path = %s", r.URL.Path)
		}
		filter := r.URL.Query().Get("$filter")
		if !strings.Contains(filter, "receivedDateTime ge 2026-07-24T00:00:00Z") || !strings.Contains(filter, "isRead eq false") {
			t.Fatalf("filter = %q", filter)
		}
		writeJSON(t, w, map[string]any{"value": []any{}})
	}))
	t.Cleanup(server.Close)
	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftGraphOAuth, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion, GraphAccessToken: "token",
	})
	after := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	adapter := NewMicrosoftAdapter(MicrosoftConfig{GraphBaseURL: server.URL}, broker, server.Client())
	if _, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{Folder: domain.MessageFolderJunk, After: &after, Unread: true}); err != nil {
		t.Fatal(err)
	}
}

func TestMicrosoftErrorsDoNotExposeTokens(t *testing.T) {
	t.Run("token endpoint", func(t *testing.T) {
		const refreshToken = "sensitive-refresh-token"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":"bad %s"}`, refreshToken)
		}))
		t.Cleanup(server.Close)
		broker := testSecretBroker{}
		credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftGraphOAuth, domain.MicrosoftCredentialSecret{
			SchemaVersion: domain.MicrosoftCredentialSecretVersion, ClientID: "client-id", RefreshToken: refreshToken,
		})
		adapter := NewMicrosoftAdapter(MicrosoftConfig{TokenEndpoint: server.URL}, broker, server.Client())
		_, err := adapter.Refresh(context.Background(), microsoftMailbox(), credential)
		if err == nil || strings.Contains(err.Error(), refreshToken) || !strings.Contains(err.Error(), "invalid_grant") {
			t.Fatalf("refresh error = %v", err)
		}
	})

	t.Run("Graph endpoint", func(t *testing.T) {
		const accessToken = "sensitive-graph-token"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"error":{"code":"InvalidAuthenticationToken","message":"bad %s"}}`, accessToken)
		}))
		t.Cleanup(server.Close)
		broker := testSecretBroker{}
		credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftGraphOAuth, domain.MicrosoftCredentialSecret{
			SchemaVersion: domain.MicrosoftCredentialSecretVersion, GraphAccessToken: accessToken,
		})
		adapter := NewMicrosoftAdapter(MicrosoftConfig{GraphBaseURL: server.URL + "/graph"}, broker, server.Client())
		_, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{})
		if err == nil || strings.Contains(err.Error(), accessToken) || !strings.Contains(err.Error(), "InvalidAuthenticationToken") {
			t.Fatalf("Graph error = %v", err)
		}
	})
}

func TestMicrosoftRejectsCrossOriginPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"value": []any{}, "@odata.nextLink": "https://example.invalid/messages?page=2"})
	}))
	t.Cleanup(server.Close)
	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftGraphOAuth, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion, GraphAccessToken: "token",
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{GraphBaseURL: server.URL}, broker, server.Client())
	_, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
}

func TestMicrosoftIgnoresUntaggedAccessTokenAndFiltersIMAPRecipients(t *testing.T) {
	broker := testSecretBroker{}
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftGraphOAuth, domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion,
		AccessToken:   "legacy-platform-account-token",
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{}, broker, nil)
	_, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{})
	if !errors.Is(err, domain.ErrUnauthorized) || strings.Contains(err.Error(), "legacy-platform-account-token") {
		t.Fatalf("untagged token error = %v", err)
	}

	expiresAt := time.Now().UTC().Add(time.Hour)
	credential = sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftIMAPOAuth, domain.MicrosoftCredentialSecret{
		SchemaVersion:      domain.MicrosoftCredentialSecretVersion,
		IMAPUsername:       "primary@outlook.com",
		IMAPAccessToken:    "imap-access-token",
		IMAPTokenExpiresAt: &expiresAt,
	})
	called := false
	adapter.imapFetch = func(_ context.Context, secret domain.MicrosoftCredentialSecret, query domain.MessageQuery) ([]domain.Message, error) {
		called = secret.IMAPUsername == "primary@outlook.com" && secret.IMAPAccessToken == "imap-access-token" && query.RetrievalMethod == domain.RetrievalIMAPOAuth
		return []domain.Message{
			{ID: "alias-b", ReceivedAt: time.Now(), Headers: map[string][]string{"X-Original-To": {"alias.b@rainynight.me"}}},
			{ID: "missing-evidence", ReceivedAt: time.Now()},
			{ID: "alias-a", ReceivedAt: time.Now(), Headers: map[string][]string{"X-Original-To": {"other@rainynight.me", "Alias.A@rainynight.me"}}},
		}, nil
	}
	messages, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{
		RetrievalMethod:  domain.RetrievalIMAPOAuth,
		RecipientAddress: "alias.a@rainynight.me",
		Limit:            5,
	})
	if err != nil || !called {
		t.Fatalf("called=%v error=%v", called, err)
	}
	if len(messages) != 1 || messages[0].ID != "alias-a" {
		t.Fatalf("filtered IMAP messages = %+v", messages)
	}
}

func TestMicrosoftDualCredentialSelectsGraphOrIMAP(t *testing.T) {
	const graphAccessToken = "dual-graph-token"
	graphCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		graphCalls++
		if r.Header.Get("Authorization") != "Bearer "+graphAccessToken {
			t.Fatalf("Graph authorization = %q", r.Header.Get("Authorization"))
		}
		writeJSON(t, w, map[string]any{"value": []any{graphFixture("graph-message", "primary@outlook.com", nil)}})
	}))
	t.Cleanup(server.Close)

	broker := testSecretBroker{}
	expiresAt := time.Now().UTC().Add(time.Hour)
	credential := sealMicrosoftCredential(t, broker, domain.CredentialMicrosoftDualToken, domain.MicrosoftCredentialSecret{
		SchemaVersion:       domain.MicrosoftCredentialSecretVersion,
		GraphAccessToken:    graphAccessToken,
		GraphTokenExpiresAt: &expiresAt,
		IMAPAccessToken:     "dual-imap-token",
		IMAPTokenExpiresAt:  &expiresAt,
		IMAPUsername:        "primary@outlook.com",
	})
	adapter := NewMicrosoftAdapter(MicrosoftConfig{GraphBaseURL: server.URL}, broker, server.Client())
	imapCalls := 0
	adapter.imapFetch = func(_ context.Context, secret domain.MicrosoftCredentialSecret, _ domain.MessageQuery) ([]domain.Message, error) {
		imapCalls++
		if secret.IMAPAccessToken != "dual-imap-token" {
			t.Fatalf("IMAP token was not selected")
		}
		return []domain.Message{{ID: "imap-message", ReceivedAt: time.Now(), To: []string{"primary@outlook.com"}}}, nil
	}

	graphMessages, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{RetrievalMethod: domain.RetrievalMicrosoftGraph})
	if err != nil || len(graphMessages) != 1 || graphMessages[0].ID != "graph-message" || graphCalls != 1 || imapCalls != 0 {
		t.Fatalf("Graph selection: messages=%v graph=%d imap=%d err=%v", graphMessages, graphCalls, imapCalls, err)
	}
	imapMessages, err := adapter.Retrieve(context.Background(), microsoftMailbox(), credential, domain.MessageQuery{
		RetrievalMethod:  domain.RetrievalIMAPOAuth,
		RecipientAddress: "primary@outlook.com",
	})
	if err != nil || len(imapMessages) != 1 || imapMessages[0].ID != "imap-message" || graphCalls != 1 || imapCalls != 1 {
		t.Fatalf("IMAP selection: messages=%v graph=%d imap=%d err=%v", imapMessages, graphCalls, imapCalls, err)
	}
}

func TestMicrosoftXOAUTH2Client(t *testing.T) {
	client := &microsoftXOAUTH2Client{username: "user@outlook.com", accessToken: "access-token"}
	mechanism, initial, err := client.Start()
	if err != nil {
		t.Fatal(err)
	}
	if mechanism != "XOAUTH2" || string(initial) != "user=user@outlook.com\x01auth=Bearer access-token\x01\x01" {
		t.Fatalf("mechanism=%q initial=%q", mechanism, initial)
	}
	response, err := client.Next([]byte(`{"status":"401"}`))
	if err != nil || len(response) != 0 {
		t.Fatalf("challenge response=%q err=%v", response, err)
	}
	if _, _, err := (&microsoftXOAUTH2Client{username: "bad\x01user", accessToken: "token"}).Start(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("control-character username error = %v", err)
	}
}

type testSecretBroker struct{}

func (testSecretBroker) Seal(_ context.Context, plaintext []byte) ([]byte, string, error) {
	sealed := append([]byte("sealed:"), plaintext...)
	return sealed, "test-key-v2", nil
}

func (testSecretBroker) Open(_ context.Context, sealed []byte, _ string) ([]byte, error) {
	if !bytes.HasPrefix(sealed, []byte("sealed:")) {
		return nil, errors.New("invalid sealed fixture")
	}
	return append([]byte(nil), sealed[len("sealed:"):]...), nil
}

func (testSecretBroker) CurrentKeyVersion() string { return "test-key-v2" }

func sealMicrosoftCredential(t *testing.T, broker testSecretBroker, kind domain.CredentialKind, secret domain.MicrosoftCredentialSecret) domain.MailboxCredential {
	t.Helper()
	plaintext, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	sealed, keyVersion, err := broker.Seal(context.Background(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return domain.MailboxCredential{
		ID: "credential-test", MailboxID: "mailbox-test", Kind: kind,
		ClientID: secret.ClientID, EncryptedSecret: sealed, KeyVersion: keyVersion,
	}
}

func openMicrosoftCredential(t *testing.T, broker testSecretBroker, sealed []byte, keyVersion string) domain.MicrosoftCredentialSecret {
	t.Helper()
	plaintext, err := broker.Open(context.Background(), sealed, keyVersion)
	if err != nil {
		t.Fatal(err)
	}
	var secret domain.MicrosoftCredentialSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func microsoftMailbox() domain.Mailbox {
	return domain.Mailbox{ID: "mailbox-test", Provider: domain.ProviderMicrosoft, Address: "primary@outlook.com"}
}

func graphFixture(id, to string, headers []map[string]string) map[string]any {
	isRead := false
	return map[string]any{
		"id": id, "internetMessageId": "<" + id + "@example.test>", "subject": id,
		"receivedDateTime": "2026-07-24T08:00:00Z", "body": map[string]any{"contentType": "Text", "content": "body"},
		"from":                   map[string]any{"emailAddress": map[string]any{"address": "sender@example.test"}},
		"toRecipients":           []any{map[string]any{"emailAddress": map[string]any{"address": to}}},
		"internetMessageHeaders": headers, "isRead": isRead,
	}
}

func serverURLFromRequest(r *http.Request) string {
	return "http://" + r.Host
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func cloneValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, items := range values {
		clone[key] = append([]string(nil), items...)
	}
	return clone
}

func redactMicrosoftSecret(secret domain.MicrosoftCredentialSecret) map[string]any {
	return map[string]any{
		"schema_version":          secret.SchemaVersion,
		"has_refresh_token":       secret.RefreshToken != "",
		"has_graph_refresh_token": secret.GraphRefreshToken != "",
		"has_imap_refresh_token":  secret.IMAPRefreshToken != "",
		"has_graph_access_token":  secret.GraphAccessToken != "",
		"has_imap_access_token":   secret.IMAPAccessToken != "",
	}
}

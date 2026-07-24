package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func TestCloudflareCreateRouteAndReuse(t *testing.T) {
	const token = "test-cloudflare-token"
	var mu sync.Mutex
	var created *cloudflareRule
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/account/email/routing/addresses":
			writeCloudflareResult(t, w, []map[string]any{{"tag": "destination", "email": "route@gmail.com", "verified": "2026-07-24T00:00:00Z"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone/email/routing/rules":
			mu.Lock()
			defer mu.Unlock()
			if created == nil {
				writeCloudflareResult(t, w, []cloudflareRule{})
				return
			}
			writeCloudflareResult(t, w, []cloudflareRule{*created})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone/email/routing/rules":
			var rule cloudflareRule
			if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
				t.Fatal(err)
			}
			if !ruleMatchesSource(rule, "split@sub.rainynight.me") || !ruleForwardsTo(rule, "route@gmail.com") {
				t.Fatalf("unexpected rule payload: %+v", rule)
			}
			rule.Tag = "rule-1"
			mu.Lock()
			created = &rule
			mu.Unlock()
			writeCloudflareResult(t, w, rule)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	adapter := NewCloudflareRouteAdapter(CloudflareConfig{
		APIToken: token, AccountID: "account", ZoneID: "zone", ZoneName: "rainynight.me", BaseURL: server.URL,
	}, server.Client())
	request := domain.DomainRouteRequest{Zone: "sub.rainynight.me", LocalPart: "split", DestinationMailboxID: "mbx-1", DestinationAddress: "route@gmail.com"}
	first, err := adapter.CreateRoute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.CreateRoute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExternalReference != "rule-1" || second.ExternalReference != first.ExternalReference {
		t.Fatalf("route references = %q, %q", first.ExternalReference, second.ExternalReference)
	}
}

func TestCloudflareCreateRouteRejectsConflictingDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/addresses"):
			writeCloudflareResult(t, w, []map[string]any{{"email": "new@gmail.com", "verified": "2026-07-24T00:00:00Z"}})
		case strings.Contains(r.URL.Path, "/rules"):
			writeCloudflareResult(t, w, []cloudflareRule{{
				Tag: "existing", Matchers: []cloudflareMatcher{{Type: "literal", Field: "to", Value: "split@rainynight.me"}},
				Actions: []cloudflareAction{{Type: "forward", Value: []string{"old@gmail.com"}}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	adapter := NewCloudflareRouteAdapter(CloudflareConfig{APIToken: "token", AccountID: "account", ZoneID: "zone", BaseURL: server.URL}, server.Client())
	_, err := adapter.CreateRoute(context.Background(), domain.DomainRouteRequest{Zone: "rainynight.me", LocalPart: "split", DestinationAddress: "new@gmail.com"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestCloudflareCreateRouteCreatesMissingDestinationAndStopsPending(t *testing.T) {
	var destinationPosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/addresses") {
			writeCloudflareResult(t, w, []cloudflareDestination{})
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/addresses") {
			destinationPosts++
			writeCloudflareResult(t, w, cloudflareDestination{Tag: "pending", Email: "new@gmail.com"})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	adapter := NewCloudflareRouteAdapter(CloudflareConfig{APIToken: "token", AccountID: "account", ZoneID: "zone", BaseURL: server.URL}, server.Client())
	_, err := adapter.CreateRoute(context.Background(), domain.DomainRouteRequest{Zone: "rainynight.me", LocalPart: "split", DestinationAddress: "new@gmail.com"})
	if !errors.Is(err, ErrDestinationVerificationPending) || destinationPosts != 1 {
		t.Fatalf("error = %v, destination posts = %d", err, destinationPosts)
	}
}

func TestCloudflareErrorDoesNotExposeToken(t *testing.T) {
	const token = "sensitive-test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `{"success":false,"errors":[{"code":9109,"message":"bad token %s"}],"result":null}`, token)
	}))
	t.Cleanup(server.Close)
	adapter := NewCloudflareRouteAdapter(CloudflareConfig{APIToken: token, AccountID: "account", ZoneID: "zone", BaseURL: server.URL}, server.Client())
	err := adapter.VerifyDestination(context.Background(), "route@gmail.com")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("secret leaked in error: %v", err)
	}
}

func TestCloudflareRejectsAddressOutsideConfiguredZone(t *testing.T) {
	adapter := NewCloudflareRouteAdapter(CloudflareConfig{APIToken: "token", AccountID: "account", ZoneID: "zone", ZoneName: "rainynight.me"}, nil)
	_, err := adapter.CreateRoute(context.Background(), domain.DomainRouteRequest{Zone: "example.net", LocalPart: "split", DestinationAddress: "route@gmail.com"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
}

func TestDynamicCloudflareAdapterReloadsConnectionForEachOperation(t *testing.T) {
	config := CloudflareConfig{}
	loads := 0
	adapter := NewDynamicCloudflareRouteAdapter(func(context.Context) (CloudflareConfig, error) {
		loads++
		return config, nil
	}, nil)

	if adapter.Descriptor(context.Background()).Configured {
		t.Fatal("empty dynamic Cloudflare connection was reported as configured")
	}
	config = CloudflareConfig{APIToken: "token", AccountID: "account", ZoneID: "zone"}
	if !adapter.Descriptor(context.Background()).Configured {
		t.Fatal("updated dynamic Cloudflare connection was not reloaded")
	}
	config = CloudflareConfig{}
	if adapter.Descriptor(context.Background()).Configured {
		t.Fatal("disabled dynamic Cloudflare connection remained configured")
	}
	if loads != 3 {
		t.Fatalf("dynamic Cloudflare config loads = %d, want 3", loads)
	}
}

func writeCloudflareResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result}); err != nil {
		t.Fatal(err)
	}
}

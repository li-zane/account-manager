package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestCloudflareProviderConnectionIsEncryptedAndAvailableAtRuntime(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "provider-v1")
	if err != nil {
		t.Fatal(err)
	}
	connections, err := service.NewProviderConnectionService(store, broker)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	settings, err := connections.Save(ctx, domain.ProviderCloudflareRoute, service.SaveProviderConnectionInput{
		AccountID:  "account-secret-value",
		ZoneID:     "zone-secret-value",
		ZoneName:   "example.test",
		APIBaseURL: "https://cloudflare.example.test/client/v4",
		APIToken:   "cloudflare-token-secret",
		Enabled:    &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Configured || settings.Version != 1 {
		t.Fatalf("saved settings = %+v", settings)
	}

	persisted, err := store.GetProviderConnectionByProviderAndName(ctx, domain.ProviderCloudflareRoute, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.EncryptedConfig) == 0 || persisted.KeyVersion != "provider-v1" {
		t.Fatalf("persisted encrypted envelope = %+v", persisted)
	}
	for _, plaintext := range []string{"account-secret-value", "zone-secret-value", "example.test", "cloudflare-token-secret"} {
		if strings.Contains(string(persisted.EncryptedConfig), plaintext) {
			t.Fatalf("encrypted provider config contains plaintext %q", plaintext)
		}
	}

	runtime, configured, err := connections.CloudflareRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !configured {
		t.Fatal("Cloudflare runtime was not reported as configured")
	}
	if runtime.APIToken != "cloudflare-token-secret" || runtime.AccountID != "account-secret-value" || runtime.ZoneID != "zone-secret-value" || runtime.ZoneName != "example.test" || runtime.APIBaseURL != "https://cloudflare.example.test/client/v4" || !runtime.Enabled {
		t.Fatalf("Cloudflare runtime = %+v", runtime)
	}

	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{"cloudflare-token-secret", "api_token", "encrypted_config", "key_version"} {
		if strings.Contains(string(encoded), protected) {
			t.Fatalf("settings JSON contains protected value %q: %s", protected, encoded)
		}
	}
}

func TestDisabledCloudflareConnectionStillOverridesRuntimeFallback(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "provider-v1")
	connections, _ := service.NewProviderConnectionService(store, broker)
	disabled := false
	if _, err := connections.Save(ctx, domain.ProviderCloudflareRoute, service.SaveProviderConnectionInput{
		AccountID: "account", ZoneID: "zone", APIToken: "disabled-token", Enabled: &disabled,
	}); err != nil {
		t.Fatal(err)
	}

	runtime, found, err := connections.CloudflareRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("disabled persisted connection must still suppress environment fallback")
	}
	if runtime.Enabled || runtime.APIToken != "" || runtime.AccountID != "" || runtime.ZoneID != "" {
		t.Fatalf("disabled runtime exposed provider configuration: %+v", runtime)
	}
}

func TestCloudflareRuntimeRejectsTamperedAndMismatchedEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name      string
		forbidden string
		mutate    func(*testing.T, context.Context, *security.AESGCMBroker, *domain.ProviderConnection)
	}{
		{
			name: "tampered ciphertext",
			mutate: func(t *testing.T, _ context.Context, _ *security.AESGCMBroker, connection *domain.ProviderConnection) {
				t.Helper()
				connection.EncryptedConfig[len(connection.EncryptedConfig)-1] ^= 0xff
			},
		},
		{
			name:      "unknown key version",
			forbidden: "retired-secret-key-v99",
			mutate: func(t *testing.T, _ context.Context, _ *security.AESGCMBroker, connection *domain.ProviderConnection) {
				t.Helper()
				connection.KeyVersion = "retired-secret-key-v99"
			},
		},
		{
			name: "envelope from another connection",
			mutate: func(t *testing.T, ctx context.Context, broker *security.AESGCMBroker, connection *domain.ProviderConnection) {
				t.Helper()
				plaintext, err := broker.Open(ctx, connection.EncryptedConfig, connection.KeyVersion)
				if err != nil {
					t.Fatal(err)
				}
				var envelope map[string]any
				if err := json.Unmarshal(plaintext, &envelope); err != nil {
					t.Fatal(err)
				}
				envelope["connection_id"] = "pconn_different_connection"
				plaintext, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				connection.EncryptedConfig, connection.KeyVersion, err = broker.Seal(ctx, plaintext)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := memory.New()
			broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "provider-v1")
			connections, _ := service.NewProviderConnectionService(store, broker)
			enabled := true
			if _, err := connections.Save(ctx, domain.ProviderCloudflareRoute, service.SaveProviderConnectionInput{
				AccountID: "account", ZoneID: "zone", APIToken: "token", Enabled: &enabled,
			}); err != nil {
				t.Fatal(err)
			}
			persisted, err := store.GetProviderConnectionByProviderAndName(ctx, domain.ProviderCloudflareRoute, service.DefaultProviderConnectionName)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, ctx, broker, &persisted)
			if err := store.UpdateProviderConnection(ctx, persisted, persisted.Version); err != nil {
				t.Fatal(err)
			}

			_, found, err := connections.CloudflareRuntime(ctx)
			if !found {
				t.Fatal("persisted invalid connection must still suppress environment fallback")
			}
			if err == nil {
				t.Fatal("invalid provider connection envelope was accepted")
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("provider runtime error leaked key version %q: %v", test.forbidden, err)
			}
		})
	}
}

func TestCloudflareProviderConnectionUpdatePreservesTokenAndRejectsStaleVersion(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "provider-v1")
	if err != nil {
		t.Fatal(err)
	}
	connections, err := service.NewProviderConnectionService(store, broker)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	created, err := connections.Save(ctx, domain.ProviderCloudflareRoute, service.SaveProviderConnectionInput{
		AccountID: "account", ZoneID: "zone", ZoneName: "old.example.test",
		APIToken: "token-to-preserve", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := connections.Save(ctx, domain.ProviderCloudflareRoute, service.SaveProviderConnectionInput{
		AccountID: "account", ZoneID: "zone", ZoneName: "new.example.test",
		Enabled: &enabled, Version: created.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != created.Version+1 || updated.ZoneName != "new.example.test" {
		t.Fatalf("updated settings = %+v", updated)
	}
	runtime, configured, err := connections.CloudflareRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !configured || runtime.APIToken != "token-to-preserve" || runtime.ZoneName != "new.example.test" {
		t.Fatalf("updated Cloudflare runtime = %+v, configured=%v", runtime, configured)
	}

	_, err = connections.Save(ctx, domain.ProviderCloudflareRoute, service.SaveProviderConnectionInput{
		AccountID: "account", ZoneID: "zone", ZoneName: "stale.example.test",
		Enabled: &enabled, Version: created.Version,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale provider settings update error = %v, want conflict", err)
	}
}

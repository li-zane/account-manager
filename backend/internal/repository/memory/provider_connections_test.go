package memory_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
)

func TestProviderConnectionRepositoryCRUDFilterAndCloning(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	now := time.Now().UTC()
	connection := domain.ProviderConnection{
		ID:              "pconn_cloudflare_primary",
		Provider:        domain.ProviderCloudflareRoute,
		Name:            "primary",
		EncryptedConfig: []byte("sealed-config"),
		KeyVersion:      "v1",
		Enabled:         true,
		Capabilities: domain.ProviderCapabilities{
			Forwarding:       true,
			RetrievalMethods: []domain.RetrievalMethod{domain.RetrievalForwarded},
		},
		Metadata:  []byte(`{"region":"global"}`),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateProviderConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}

	// Mutating caller-owned memory after create must not mutate persistence.
	connection.EncryptedConfig[0] = 'X'
	connection.Capabilities.RetrievalMethods[0] = domain.RetrievalIMAPPassword
	connection.Metadata[0] = 'X'

	got, err := store.GetProviderConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 {
		t.Fatalf("created version = %d, want 1", got.Version)
	}
	if string(got.EncryptedConfig) != "sealed-config" || len(got.Capabilities.RetrievalMethods) != 1 || got.Capabilities.RetrievalMethods[0] != domain.RetrievalForwarded || string(got.Metadata) != `{"region":"global"}` {
		t.Fatalf("stored provider connection was mutated through caller buffers: %+v", got)
	}

	byIdentity, err := store.GetProviderConnectionByProviderAndName(ctx, domain.ProviderCloudflareRoute, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if byIdentity.ID != got.ID {
		t.Fatalf("identity lookup id = %q, want %q", byIdentity.ID, got.ID)
	}

	items, err := store.ListProviderConnections(ctx, ports.ProviderConnectionFilter{Provider: domain.ProviderCloudflareRoute}, ports.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != got.ID {
		t.Fatalf("filtered provider connections = %+v", items)
	}
	disabled := false
	items, err = store.ListProviderConnections(ctx, ports.ProviderConnectionFilter{Enabled: &disabled}, ports.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("disabled provider connections = %+v, want none", items)
	}

	// Mutating a returned value must not mutate the value retained by the store.
	got.EncryptedConfig[0] = 'Y'
	got.Capabilities.RetrievalMethods[0] = domain.RetrievalIMAPPassword
	got.Metadata[0] = 'Y'
	again, err := store.GetProviderConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.EncryptedConfig) != "sealed-config" || len(again.Capabilities.RetrievalMethods) != 1 || again.Capabilities.RetrievalMethods[0] != domain.RetrievalForwarded || string(again.Metadata) != `{"region":"global"}` {
		t.Fatalf("provider connection escaped repository cloning: %+v", again)
	}

	again.Name = "renamed"
	again.EncryptedConfig = []byte("sealed-config-v2")
	if err := store.UpdateProviderConnection(ctx, again, 1); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetProviderConnection(ctx, again.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Name != "renamed" || string(updated.EncryptedConfig) != "sealed-config-v2" {
		t.Fatalf("updated provider connection = %+v", updated)
	}
	if _, err := store.GetProviderConnectionByProviderAndName(ctx, domain.ProviderCloudflareRoute, "primary"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old identity lookup error = %v, want not found", err)
	}
	if err := store.UpdateProviderConnection(ctx, updated, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale provider connection update error = %v, want conflict", err)
	}
}

func TestProviderConnectionConcurrentCASAllowsOneWinner(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	now := time.Now().UTC()
	connection := domain.ProviderConnection{
		ID: "pconn_cloudflare_cas", Provider: domain.ProviderCloudflareRoute, Name: "cas",
		EncryptedConfig: []byte("sealed"), KeyVersion: "v1", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateProviderConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetProviderConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}

	const writers = 32
	start := make(chan struct{})
	var group sync.WaitGroup
	var successes atomic.Int32
	errorsFound := make(chan error, writers)
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			candidate := current
			candidate.Metadata = []byte(`{"writer":true}`)
			err := store.UpdateProviderConnection(ctx, candidate, current.Version)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, domain.ErrConflict) {
				errorsFound <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent update: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful concurrent updates = %d, want 1", got)
	}
}

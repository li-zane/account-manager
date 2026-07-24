package security

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type pickupMemoryRepository struct {
	items map[string]domain.MailboxPickupKey
}

func (r *pickupMemoryRepository) CreatePickupKey(_ context.Context, key domain.MailboxPickupKey) error {
	r.items[string(key.Digest)] = key
	return nil
}

func (r *pickupMemoryRepository) FindPickupKeyByDigest(_ context.Context, digest []byte) (domain.MailboxPickupKey, error) {
	key, ok := r.items[string(digest)]
	if !ok {
		return domain.MailboxPickupKey{}, domain.ErrNotFound
	}
	return key, nil
}

func (r *pickupMemoryRepository) RevokePickupKey(_ context.Context, mailboxID, keyID string) error {
	for digest, key := range r.items {
		if key.MailboxID == mailboxID && key.ID == keyID {
			now := time.Now().UTC()
			key.RevokedAt = &now
			r.items[digest] = key
			return nil
		}
	}
	return domain.ErrNotFound
}

func (r *pickupMemoryRepository) ListPickupKeys(_ context.Context, mailboxID string, _ ports.ListOptions) ([]domain.MailboxPickupKey, error) {
	items := make([]domain.MailboxPickupKey, 0, len(r.items))
	for _, item := range r.items {
		if mailboxID == "" || item.MailboxID == mailboxID {
			items = append(items, item)
		}
	}
	return items, nil
}

func TestAESGCMBrokerRoundTripAndVersion(t *testing.T) {
	broker, err := NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	sealed, version, err := broker.Seal(context.Background(), []byte("provider-refresh-token"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := broker.Open(context.Background(), sealed, version)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "provider-refresh-token" {
		t.Fatalf("opened plaintext = %q", opened)
	}
	if _, err := broker.Open(context.Background(), sealed, "v0"); err == nil {
		t.Fatal("expected version mismatch")
	}
}

func TestPickupKeyIsOpaqueAndExpires(t *testing.T) {
	repository := &pickupMemoryRepository{items: make(map[string]domain.MailboxPickupKey)}
	service, err := NewPickupKeyService(repository, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	expires := now.Add(time.Hour)
	key, raw, err := service.Issue(context.Background(), "mbx_microsoft_demo", "verification", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || key.Prefix == raw {
		t.Fatal("raw token should not be stored or fully displayed")
	}
	resolved, err := service.Lookup(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != key.ID {
		t.Fatalf("resolved id = %q, want %q", resolved.ID, key.ID)
	}
	service.SetClock(func() time.Time { return expires.Add(time.Second) })
	if _, err := service.Lookup(context.Background(), raw); err != domain.ErrKeyExpired {
		t.Fatalf("lookup after expiry = %v", err)
	}
}

func TestPrepareImportedPickupKeyKeepsLegacyValueOutOfPersistence(t *testing.T) {
	repository := &pickupMemoryRepository{items: make(map[string]domain.MailboxPickupKey)}
	service, err := NewPickupKeyService(repository, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	raw := "legacy-mail-access-key"
	key, err := service.PrepareImported("mbx_cloudflare_demo", "legacy import", raw)
	if err != nil {
		t.Fatal(err)
	}
	if key.Prefix != "legacy" || string(key.Digest) == raw {
		t.Fatalf("prepared key exposed legacy value: prefix=%q digest=%q", key.Prefix, key.Digest)
	}
	if err := repository.CreatePickupKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	resolved, err := service.Lookup(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != key.ID || resolved.MailboxID != "mbx_cloudflare_demo" {
		t.Fatalf("resolved imported key = %+v", resolved)
	}
}

func TestPickupKeyEnsureIsIdempotentAndEncryptedRevealRemainsUsable(t *testing.T) {
	ctx := context.Background()
	repository := &pickupMemoryRepository{items: make(map[string]domain.MailboxPickupKey)}
	service, err := NewPickupKeyService(repository, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewAESGCMBroker([]byte("abcdefghijklmnopqrstuvwxyz123456"), "pickup-v1")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretBroker(broker)
	first, err := service.Ensure(ctx, "mbx_auto_pickup")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Ensure(ctx, "mbx_auto_pickup")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(repository.items) != 1 {
		t.Fatalf("Ensure issued duplicate keys: first=%q second=%q count=%d", first.ID, second.ID, len(repository.items))
	}
	if len(first.EncryptedToken) == 0 || first.KeyVersion != "pickup-v1" {
		t.Fatalf("automatic key is not exportable: %+v", first)
	}
	raw, err := service.Reveal(ctx, "mbx_auto_pickup")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := service.Lookup(ctx, raw)
	if err != nil || resolved.ID != first.ID {
		t.Fatalf("revealed key lookup = %+v, err=%v", resolved, err)
	}
}

func TestPickupKeyEnsureCoalescesConcurrentIssuance(t *testing.T) {
	ctx := context.Background()
	repository := &pickupMemoryRepository{items: make(map[string]domain.MailboxPickupKey)}
	service, err := NewPickupKeyService(repository, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewAESGCMBroker([]byte("abcdefghijklmnopqrstuvwxyz123456"), "pickup-v1")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretBroker(broker)

	const workers = 32
	var group sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	ids := make(chan string, workers)
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			key, ensureErr := service.Ensure(ctx, "mbx_concurrent_pickup")
			if ensureErr != nil {
				errorsByWorker <- ensureErr
				return
			}
			ids <- key.ID
		}()
	}
	group.Wait()
	close(errorsByWorker)
	close(ids)
	for ensureErr := range errorsByWorker {
		t.Fatal(ensureErr)
	}
	unique := make(map[string]struct{})
	for id := range ids {
		unique[id] = struct{}{}
	}
	if len(unique) != 1 || len(repository.items) != 1 {
		t.Fatalf("concurrent Ensure produced ids=%d keys=%d", len(unique), len(repository.items))
	}
}

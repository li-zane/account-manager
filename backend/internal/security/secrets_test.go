package security

import (
	"context"
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

func (r *pickupMemoryRepository) ListPickupKeys(context.Context, string, ports.ListOptions) ([]domain.MailboxPickupKey, error) {
	return nil, nil
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

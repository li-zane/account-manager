package memory

import (
	"context"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func TestImportMailboxesPersistsPickupKeyAtomically(t *testing.T) {
	store := New()
	now := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	mailbox := domain.Mailbox{
		ID: "mbx_cloudflare_import", Provider: domain.ProviderCloudflareRoute,
		Address: "fixture@example.test", NormalizedAddress: "fixture@example.test",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	key := domain.MailboxPickupKey{
		ID: "pkey_import", MailboxID: mailbox.ID, Digest: []byte("fixture-digest"),
		Prefix: "legacy", Label: "legacy import", CreatedAt: now,
	}

	result, err := store.ImportMailboxes(context.Background(), []domain.MailboxImportItem{{Mailbox: mailbox, PickupKey: &key}}, domain.ConflictSkip)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Fatalf("import result = %+v", result)
	}
	persisted, err := store.FindPickupKeyByDigest(context.Background(), key.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MailboxID != mailbox.ID || persisted.Prefix != "legacy" {
		t.Fatalf("persisted pickup key = %+v", persisted)
	}

	result, err = store.ImportMailboxes(context.Background(), []domain.MailboxImportItem{{Mailbox: mailbox, PickupKey: &key}}, domain.ConflictUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 {
		t.Fatalf("idempotent update result = %+v", result)
	}
}

package security_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
)

func TestOpaquePickupKeyHashesLookupAndRevocation(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	mailbox := domain.Mailbox{
		ID: "mbx_microsoft_test", Provider: domain.ProviderMicrosoft,
		Address: "owner@example.com", NormalizedAddress: "owner@example.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	keys, err := security.NewPickupKeyService(store, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	keys.SetClock(func() time.Time { return now })
	expires := now.Add(time.Hour)
	issued, token, err := keys.Issue(ctx, mailbox.ID, "external reader", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "microsoft") || strings.Contains(token, mailbox.Address) {
		t.Fatalf("pickup token exposes provider information: %q", token)
	}
	serialized, err := json.Marshal(issued)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), token) {
		t.Fatal("persisted/public key metadata contains the raw token")
	}
	resolved, err := keys.Lookup(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MailboxID != mailbox.ID {
		t.Fatalf("resolved mailbox = %q, want %q", resolved.MailboxID, mailbox.ID)
	}
	if err := keys.Revoke(ctx, mailbox.ID, issued.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Lookup(ctx, token); !errors.Is(err, domain.ErrKeyRevoked) {
		t.Fatalf("lookup after revoke = %v", err)
	}
	if _, err := keys.Lookup(ctx, token+"tampered"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("tampered token lookup = %v", err)
	}
}

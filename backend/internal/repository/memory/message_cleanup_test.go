package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

func TestCachedMessageCleanupAndPurge(t *testing.T) {
	ctx := context.Background()
	store := New()
	now := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	mailbox := domain.Mailbox{ID: "mbx_cleanup", Provider: domain.ProviderMicrosoft, Address: "cleanup@example.com", NormalizedAddress: "cleanup@example.com", Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	items := make([]domain.CachedMessage, 0, 4)
	for index := 0; index < 3; index++ {
		received := now.Add(-time.Duration(index+1) * time.Hour)
		items = append(items, domain.CachedMessage{ID: string(rune('a' + index)), MailboxID: mailbox.ID, ExternalID: string(rune('a' + index)), ProviderMessageID: string(rune('a' + index)), Folder: domain.MessageFolderInbox, ReceivedAt: received, DiscoveredAt: now, UpdatedAt: now})
	}
	items = append(items, domain.CachedMessage{ID: "d", MailboxID: mailbox.ID, ExternalID: "d", ProviderMessageID: "d", Folder: domain.MessageFolderJunk, ReceivedAt: now.Add(-48 * time.Hour), DiscoveredAt: now, UpdatedAt: now})
	if _, err := store.UpsertCachedMessages(ctx, items); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.CleanupCachedMessages(ctx, now.Add(-24*time.Hour), 2, 10)
	if err != nil || deleted != 2 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	deleted, err = store.PurgeCachedMessages(ctx, mailbox.ID, domain.MessageFolderInbox, nil, 10)
	if err != nil || deleted != 2 {
		t.Fatalf("purge deleted=%d err=%v", deleted, err)
	}
}

func TestQueryAndDeleteCachedMessageRange(t *testing.T) {
	ctx := context.Background()
	store := New()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	mailbox := domain.Mailbox{ID: "mbx_range", Provider: domain.ProviderMicrosoft, Address: "range@example.test", NormalizedAddress: "range@example.test", Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	for index, receivedAt := range []time.Time{now.Add(-72 * time.Hour), now.Add(-24 * time.Hour), now} {
		_, err := store.UpsertCachedMessages(ctx, []domain.CachedMessage{{ID: fmt.Sprintf("msg_%d", index), MailboxID: mailbox.ID, ExternalID: fmt.Sprintf("ext_%d", index), Folder: domain.MessageFolderInbox, Subject: fmt.Sprintf("code %d", index), ReceivedAt: receivedAt}})
		if err != nil {
			t.Fatal(err)
		}
	}
	after, before := now.Add(-48*time.Hour), now.Add(time.Hour)
	items, count, err := store.QueryCachedMessages(ctx, ports.MessageCacheFilter{MailboxID: mailbox.ID, After: &after, Before: &before, Search: "code"}, ports.ListOptions{Limit: 10})
	if err != nil || count != 2 || len(items) != 2 {
		t.Fatalf("range query count=%d items=%d err=%v", count, len(items), err)
	}
	deleted, err := store.DeleteCachedMessagesRange(ctx, ports.MessageCacheFilter{MailboxID: mailbox.ID, After: &after, Before: &before}, 10)
	if err != nil || deleted != 2 {
		t.Fatalf("range delete=%d err=%v", deleted, err)
	}
}

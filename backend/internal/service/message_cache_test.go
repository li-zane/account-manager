package service_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestMessageCacheIncrementalDeduplicationAndAliasIsolationWithoutRefresh(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 18, 0, 0, 0, time.UTC)
	store := memory.New()
	mailbox := domain.Mailbox{
		ID: "mbx_cache_fixture", Provider: domain.ProviderMicrosoft,
		Address: "owner@outlook.com", NormalizedAddress: "owner@outlook.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	alias := domain.MailboxAlias{
		ID: "alias_cache_fixture", MailboxID: mailbox.ID, Provider: domain.ProviderMicrosoft,
		Address: "split@outlook.com", NormalizedAddress: "split@outlook.com",
		Kind: domain.AliasKindSplit, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAlias(ctx, alias); err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(time.Hour)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftDualToken, "sealed-shared-rt", expiry, now)

	t1, t2, t3 := now.Add(-30*time.Minute), now.Add(-20*time.Minute), now.Add(-10*time.Minute)
	aliasFirst := domain.Message{ID: "provider-1", InternetMessageID: "<one@example.test>", RecipientAddresses: []string{alias.NormalizedAddress}, Subject: "first", ReceivedAt: t1, Unread: true}
	aliasSecond := domain.Message{ID: "provider-2", InternetMessageID: "<two@example.test>", RecipientAddresses: []string{alias.NormalizedAddress}, Subject: "second", ReceivedAt: t2}
	other := domain.Message{ID: "provider-3", InternetMessageID: "<other@example.test>", RecipientAddresses: []string{"other@outlook.com"}, Subject: "other", ReceivedAt: t2}
	junk := domain.Message{ID: "provider-4", InternetMessageID: "<junk@example.test>", RecipientAddresses: []string{alias.NormalizedAddress}, Subject: "junk", ReceivedAt: t3}

	var aliasInboxCalls int
	var refreshCalls atomic.Int32
	var queriesMu sync.Mutex
	var queries []domain.MessageQuery
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth},
		retrieve: func(_ context.Context, _ domain.Mailbox, _ domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
			queriesMu.Lock()
			queries = append(queries, query)
			queriesMu.Unlock()
			if query.Folder == domain.MessageFolderJunk {
				return []domain.Message{junk}, nil
			}
			if query.RecipientAddress == alias.NormalizedAddress {
				aliasInboxCalls++
				if aliasInboxCalls == 1 {
					return []domain.Message{aliasFirst, other}, nil
				}
				return []domain.Message{aliasFirst, aliasSecond, other}, nil
			}
			return []domain.Message{aliasFirst, aliasSecond, other}, nil
		},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			refreshCalls.Add(1)
			return domain.RefreshedCredential{}, domain.ErrUnauthorized
		},
	}
	retrieval := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	retrieval.SetSettingsReader(retrievalRefreshSettingsFunc(func(context.Context) (service.TokenRefreshSettings, error) {
		return service.TokenRefreshSettings{Enabled: false}, nil
	}))
	cache, err := service.NewMessageCacheService(store, store, retrieval)
	if err != nil {
		t.Fatal(err)
	}
	cache.SetClock(func() time.Time { return now })

	input := service.CachedMessagesInput{AliasID: alias.ID, Folder: domain.MessageFolderInbox, RetrievalMethod: domain.RetrievalIMAPOAuth, Limit: 100}
	first, err := cache.Sync(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.NewCount != 1 || len(first.Messages) != 1 || first.Messages[0].Subject != "first" {
		t.Fatalf("first alias sync = %+v", first)
	}
	if err := cache.MarkViewed(ctx, mailbox.ID, first.Messages[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := cache.Sync(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if second.NewCount != 1 || len(second.Messages) != 2 || second.Messages[0].Subject != "second" {
		t.Fatalf("second alias sync = %+v", second)
	}
	for _, message := range second.Messages {
		if message.Subject == "first" && message.ViewedAt == nil {
			t.Fatal("viewed state was lost during provider upsert")
		}
	}
	if refreshCalls.Load() != 0 {
		t.Fatalf("disabled token refresh called provider %d times", refreshCalls.Load())
	}
	queriesMu.Lock()
	if len(queries) < 2 || queries[1].After == nil || !queries[1].After.Equal(t1.Add(-time.Second)) || queries[1].RetrievalMethod != domain.RetrievalIMAPOAuth {
		t.Fatalf("incremental query = %+v", queries)
	}
	queriesMu.Unlock()

	primary, err := cache.Sync(ctx, service.CachedMessagesInput{MailboxID: mailbox.ID, Folder: domain.MessageFolderInbox, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if primary.NewCount != 1 || len(primary.Messages) != 3 {
		t.Fatalf("primary cache = %+v", primary)
	}
	isolated, err := cache.List(ctx, service.CachedMessagesInput{AliasID: alias.ID, Folder: domain.MessageFolderInbox, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(isolated.Messages) != 2 {
		t.Fatalf("alias cache leaked sibling mail: %+v", isolated.Messages)
	}

	if _, err := cache.Sync(ctx, service.CachedMessagesInput{AliasID: alias.ID, Folder: domain.MessageFolderJunk, RetrievalMethod: domain.RetrievalIMAPOAuth}); err != nil {
		t.Fatal(err)
	}
	lastMessageAt, err := cache.LastMessageAt(ctx, mailbox.ID)
	if err != nil || lastMessageAt == nil || !lastMessageAt.Equal(t3) {
		t.Fatalf("latest alias message = %v, err=%v", lastMessageAt, err)
	}
	if !providers.MessageMatchesRecipient(junk, alias.NormalizedAddress) {
		t.Fatal("fixture junk message no longer matches the alias isolation rule")
	}
}

func TestRestoreRangePersistsAutomaticFallbackMethod(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	store := memory.New()
	mailbox := domain.Mailbox{
		ID: "mbx_restore_fixture", Provider: domain.ProviderMicrosoft,
		Address: "restore@outlook.com", NormalizedAddress: "restore@outlook.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftDualToken, "sealed-shared-rt", now.Add(time.Hour), now)

	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth},
		retrieve: func(_ context.Context, _ domain.Mailbox, _ domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
			if query.RetrievalMethod == domain.RetrievalMicrosoftGraph {
				return nil, errors.New("graph temporarily unavailable")
			}
			return []domain.Message{{
				ID: "imap:42", InternetMessageID: "<restore@example.test>", Subject: "restored",
				RecipientAddresses: []string{mailbox.NormalizedAddress}, ReceivedAt: now.Add(-time.Hour),
			}}, nil
		},
	}
	retrieval := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	cache, err := service.NewMessageCacheService(store, store, retrieval)
	if err != nil {
		t.Fatal(err)
	}
	cache.SetClock(func() time.Time { return now })
	after, before := now.Add(-2*time.Hour), now
	count, err := cache.RestoreRange(ctx, service.ManageCachedMessagesInput{MailboxID: mailbox.ID, Folder: domain.MessageFolderInbox, After: &after, Before: &before})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("restored count = %d", count)
	}
	result, err := cache.QueryManaged(ctx, service.ManageCachedMessagesInput{MailboxID: mailbox.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Messages[0].RetrievalMethod != domain.RetrievalIMAPOAuth {
		t.Fatalf("restored messages = %+v", result.Messages)
	}
}

func TestInitialMessageSyncWorkerBackfillsAllFallbackPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	store := memory.New()
	mailbox := domain.Mailbox{
		ID: "mbx_initial_sync_fixture", Provider: domain.ProviderMicrosoft,
		Address: "initial@outlook.com", NormalizedAddress: "initial@outlook.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftDualToken, "sealed-shared-rt", now.Add(time.Hour), now)

	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph},
		retrieve: func(_ context.Context, _ domain.Mailbox, _ domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
			if query.Folder == domain.MessageFolderJunk {
				return nil, nil
			}
			if query.Before != nil {
				return []domain.Message{{
					ID: "older-page", InternetMessageID: "<older-page@example.test>",
					RecipientAddresses: []string{mailbox.NormalizedAddress}, ReceivedAt: now.Add(-500 * time.Minute),
				}}, nil
			}
			messages := make([]domain.Message, 0, 500)
			for index := range 500 {
				messages = append(messages, domain.Message{
					ID: fmt.Sprintf("page-one-%03d", index), InternetMessageID: fmt.Sprintf("<page-one-%03d@example.test>", index),
					RecipientAddresses: []string{mailbox.NormalizedAddress}, ReceivedAt: now.Add(-time.Duration(index) * time.Minute),
				})
			}
			return messages, nil
		},
	}
	retrieval := newRetrievalService(t, store, mailbox.Provider, retriever, now)
	cache, err := service.NewMessageCacheService(store, store, retrieval)
	if err != nil {
		t.Fatal(err)
	}
	cache.SetClock(func() time.Time { return now })
	worker, err := service.NewInitialMessageSyncWorker(cache, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	if err := worker.ScheduleInitialSync(ctx, []string{mailbox.ID}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		result, queryErr := cache.QueryManaged(ctx, service.ManageCachedMessagesInput{MailboxID: mailbox.ID, Limit: 1})
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if result.Count == 501 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial sync cached %d messages, want 501", result.Count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

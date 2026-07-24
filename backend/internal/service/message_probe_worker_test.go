package service_test

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestMessageProbeWorkerHonorsPersistedSwitchAndInterval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 19, 0, 0, 0, time.UTC)
	store := memory.New()
	mailbox := domain.Mailbox{
		ID: "mbx_probe_fixture", Provider: domain.ProviderMicrosoft,
		Address: "probe@outlook.com", NormalizedAddress: "probe@outlook.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	expires := now.Add(time.Hour)
	createRetrievalCredential(t, store, mailbox.ID, domain.CredentialMicrosoftGraphOAuth, "sealed", expires, now)

	var retrievalCalls atomic.Int32
	retriever := &retrievalTestRetriever{
		methods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph},
		retrieve: func(context.Context, domain.Mailbox, domain.MailboxCredential, domain.MessageQuery) ([]domain.Message, error) {
			retrievalCalls.Add(1)
			return []domain.Message{}, nil
		},
		refresh: func(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
			t.Fatal("probe unexpectedly refreshed a credential")
			return domain.RefreshedCredential{}, nil
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
	settings, err := service.NewMessageProbeSettingsService(store)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := service.NewMessageProbeWorker(store, store, cache, settings, slog.New(slog.NewTextHandler(io.Discard, nil)), service.MessageProbeWorkerConfig{
		Enabled: true, Heartbeat: time.Minute, Concurrency: 2, ItemTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.SetClock(func() time.Time { return now })

	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if retrievalCalls.Load() != 0 {
		t.Fatalf("disabled probe made %d retrieval calls", retrievalCalls.Load())
	}
	current, err := settings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Update(ctx, service.UpdateMessageProbeSettingsInput{Enabled: true, IntervalMinutes: 10, Version: current.Version}); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if retrievalCalls.Load() != 2 {
		t.Fatalf("first enabled pass calls = %d, want INBOX and Junk", retrievalCalls.Load())
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if retrievalCalls.Load() != 2 {
		t.Fatalf("interval gate calls = %d", retrievalCalls.Load())
	}
	worker.SetClock(func() time.Time { return now.Add(11 * time.Minute) })
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if retrievalCalls.Load() != 4 {
		t.Fatalf("due pass calls = %d", retrievalCalls.Load())
	}
}

func TestDisabledMessageProbeWorkerStopsOnCancellation(t *testing.T) {
	store := memory.New()
	settings, _ := service.NewMessageProbeSettingsService(store)
	retrieval, _ := service.NewMessageRetrievalService(store, retrievalTestRegistry{}, func(domain.Message, string) bool { return false })
	cache, _ := service.NewMessageCacheService(store, store, retrieval)
	worker, err := service.NewMessageProbeWorker(store, store, cache, settings, nil, service.MessageProbeWorkerConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

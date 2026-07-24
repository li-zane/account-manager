package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type credentialRefreshTestRepository struct {
	mailboxes   []domain.Mailbox
	credentials map[string][]domain.MailboxCredential

	mu      sync.Mutex
	offsets []int
}

func (r *credentialRefreshTestRepository) ListMailboxes(ctx context.Context, options ports.ListOptions) ([]domain.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.offsets = append(r.offsets, options.Offset)
	r.mu.Unlock()
	if options.Offset >= len(r.mailboxes) {
		return nil, nil
	}
	end := options.Offset + options.Limit
	if end > len(r.mailboxes) {
		end = len(r.mailboxes)
	}
	return append([]domain.Mailbox(nil), r.mailboxes[options.Offset:end]...), nil
}

func (r *credentialRefreshTestRepository) ListCredentials(ctx context.Context, mailboxID string) ([]domain.MailboxCredential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]domain.MailboxCredential(nil), r.credentials[mailboxID]...), nil
}

type dueCredentialRefreshFunc func(context.Context, string, domain.CredentialKind) (domain.MailboxCredential, error)

func (f dueCredentialRefreshFunc) RefreshDueCredential(ctx context.Context, mailboxID string, kind domain.CredentialKind) (domain.MailboxCredential, error) {
	return f(ctx, mailboxID, kind)
}

type credentialRefreshSettingsFunc func(context.Context) (TokenRefreshSettings, error)

func (f credentialRefreshSettingsFunc) Get(ctx context.Context) (TokenRefreshSettings, error) {
	return f(ctx)
}

func TestCredentialRefreshWorkerReloadsPersistedSwitchAndLeadTime(t *testing.T) {
	now := time.Date(2026, 7, 24, 11, 0, 0, 0, time.UTC)
	expiresAt := now.Add(20 * time.Minute)
	refreshAfter := now.Add(-time.Minute)
	mailbox := domain.Mailbox{ID: "mbx_dynamic_settings", Status: domain.MailboxStatusActive}
	repository := &credentialRefreshTestRepository{
		mailboxes: []domain.Mailbox{mailbox},
		credentials: map[string][]domain.MailboxCredential{
			mailbox.ID: {{
				MailboxID: mailbox.ID, Kind: domain.CredentialMicrosoftGraphOAuth,
				ExpiresAt: &expiresAt, RefreshAfter: &refreshAfter, RefreshStatus: "active", UpdatedAt: now.Add(-time.Hour),
			}},
		},
	}
	var calls atomic.Int32
	refresher := dueCredentialRefreshFunc(func(context.Context, string, domain.CredentialKind) (domain.MailboxCredential, error) {
		calls.Add(1)
		return domain.MailboxCredential{}, nil
	})
	worker := newCredentialRefreshTestWorker(t, repository, refresher, CredentialRefreshWorkerConfig{Concurrency: 1}, now)
	settings := TokenRefreshSettings{Enabled: false, LeadTimeMinutes: 30}
	worker.SetSettingsReader(credentialRefreshSettingsFunc(func(context.Context) (TokenRefreshSettings, error) {
		return settings, nil
	}))

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	settings.Enabled, settings.LeadTimeMinutes = true, 5
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("refresh calls before configured lead time = %d", calls.Load())
	}

	settings.LeadTimeMinutes = 30
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls inside configured lead time = %d, want 1", calls.Load())
	}
}

func TestCredentialRefreshWorkerLimitsConcurrency(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	repository := &credentialRefreshTestRepository{credentials: make(map[string][]domain.MailboxCredential)}
	for index := 0; index < 12; index++ {
		mailboxID := "mbx_" + string(rune('a'+index))
		repository.mailboxes = append(repository.mailboxes, domain.Mailbox{ID: mailboxID, Status: domain.MailboxStatusActive})
		repository.credentials[mailboxID] = []domain.MailboxCredential{dueWorkerCredential(mailboxID, domain.CredentialMicrosoftGraphOAuth, now)}
	}

	started := make(chan struct{}, len(repository.mailboxes))
	release := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	refresher := dueCredentialRefreshFunc(func(context.Context, string, domain.CredentialKind) (domain.MailboxCredential, error) {
		calls.Add(1)
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return domain.MailboxCredential{}, nil
	})
	worker := newCredentialRefreshTestWorker(t, repository, refresher, CredentialRefreshWorkerConfig{
		Concurrency: 3, ItemTimeout: time.Second,
	}, now)

	done := make(chan error, 1)
	go func() { done <- worker.RunOnce(context.Background()) }()
	for index := 0; index < worker.config.Concurrency; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent refreshes")
		}
	}
	select {
	case <-started:
		t.Fatal("worker exceeded its concurrency limit")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish")
	}
	if calls.Load() != int32(len(repository.mailboxes)) {
		t.Fatalf("refresh calls = %d, want %d", calls.Load(), len(repository.mailboxes))
	}
	if maximum.Load() != 3 {
		t.Fatalf("maximum concurrency = %d, want 3", maximum.Load())
	}
}

func TestCredentialRefreshWorkerSkipsMissingAndBacksOffErrors(t *testing.T) {
	now := time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)
	mailbox := domain.Mailbox{ID: "mbx_statuses", Status: domain.MailboxStatusActive}
	future := now.Add(time.Hour)
	credentials := []domain.MailboxCredential{
		dueWorkerCredential(mailbox.ID, domain.CredentialMicrosoftGraphOAuth, now),
		dueWorkerCredential(mailbox.ID, domain.CredentialMicrosoftIMAPOAuth, now),
		dueWorkerCredential(mailbox.ID, domain.CredentialMicrosoftDualToken, now),
		{
			MailboxID: mailbox.ID, Kind: domain.CredentialGmailOAuth, ExpiresAt: &future,
			RefreshStatus: "active", UpdatedAt: now.Add(-time.Hour),
		},
	}
	credentials[0].RefreshStatus = "missing"
	credentials[1].RefreshStatus = "error"
	credentials[1].LastRefreshError = "recent failure"
	credentials[1].UpdatedAt = now.Add(-5 * time.Minute)
	credentials[2].RefreshStatus = "error"
	credentials[2].LastRefreshError = "old failure"
	credentials[2].UpdatedAt = now.Add(-20 * time.Minute)
	repository := &credentialRefreshTestRepository{
		mailboxes: []domain.Mailbox{mailbox},
		credentials: map[string][]domain.MailboxCredential{
			mailbox.ID: credentials,
		},
	}
	var mu sync.Mutex
	var refreshed []domain.CredentialKind
	refresher := dueCredentialRefreshFunc(func(_ context.Context, _ string, kind domain.CredentialKind) (domain.MailboxCredential, error) {
		mu.Lock()
		refreshed = append(refreshed, kind)
		mu.Unlock()
		return domain.MailboxCredential{}, nil
	})
	worker := newCredentialRefreshTestWorker(t, repository, refresher, CredentialRefreshWorkerConfig{
		Concurrency: 4, ErrorBackoff: 15 * time.Minute,
	}, now)
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(refreshed, []domain.CredentialKind{domain.CredentialMicrosoftDualToken}) {
		t.Fatalf("refreshed kinds = %v", refreshed)
	}
}

func TestCredentialRefreshWorkerAppliesItemTimeout(t *testing.T) {
	now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	mailbox := domain.Mailbox{ID: "mbx_timeout", Status: domain.MailboxStatusActive}
	repository := &credentialRefreshTestRepository{
		mailboxes: []domain.Mailbox{mailbox},
		credentials: map[string][]domain.MailboxCredential{
			mailbox.ID: {dueWorkerCredential(mailbox.ID, domain.CredentialMicrosoftGraphOAuth, now)},
		},
	}
	refresher := dueCredentialRefreshFunc(func(ctx context.Context, _ string, _ domain.CredentialKind) (domain.MailboxCredential, error) {
		<-ctx.Done()
		return domain.MailboxCredential{}, ctx.Err()
	})
	worker := newCredentialRefreshTestWorker(t, repository, refresher, CredentialRefreshWorkerConfig{
		Concurrency: 1, ItemTimeout: 20 * time.Millisecond,
	}, now)
	err := worker.RunOnce(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunOnce error = %v, want deadline exceeded", err)
	}
}

func TestCredentialRefreshWorkerRunStopsOnCancellation(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	mailbox := domain.Mailbox{ID: "mbx_cancel", Status: domain.MailboxStatusActive}
	repository := &credentialRefreshTestRepository{
		mailboxes: []domain.Mailbox{mailbox},
		credentials: map[string][]domain.MailboxCredential{
			mailbox.ID: {dueWorkerCredential(mailbox.ID, domain.CredentialMicrosoftGraphOAuth, now)},
		},
	}
	started := make(chan struct{})
	refresher := dueCredentialRefreshFunc(func(ctx context.Context, _ string, _ domain.CredentialKind) (domain.MailboxCredential, error) {
		close(started)
		<-ctx.Done()
		return domain.MailboxCredential{}, ctx.Err()
	})
	worker := newCredentialRefreshTestWorker(t, repository, refresher, CredentialRefreshWorkerConfig{
		Enabled: true, Interval: time.Hour, Concurrency: 1, ItemTimeout: time.Hour,
	}, now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestCredentialRefreshWorkerPaginatesMailboxScan(t *testing.T) {
	now := time.Date(2026, 7, 24, 16, 0, 0, 0, time.UTC)
	repository := &credentialRefreshTestRepository{credentials: make(map[string][]domain.MailboxCredential)}
	for index := 0; index < 5; index++ {
		mailboxID := "mbx_page_" + string(rune('a'+index))
		repository.mailboxes = append(repository.mailboxes, domain.Mailbox{ID: mailboxID, Status: domain.MailboxStatusActive})
		repository.credentials[mailboxID] = []domain.MailboxCredential{dueWorkerCredential(mailboxID, domain.CredentialGmailOAuth, now)}
	}
	var mu sync.Mutex
	var refreshed []string
	refresher := dueCredentialRefreshFunc(func(_ context.Context, mailboxID string, _ domain.CredentialKind) (domain.MailboxCredential, error) {
		mu.Lock()
		refreshed = append(refreshed, mailboxID)
		mu.Unlock()
		return domain.MailboxCredential{}, nil
	})
	worker := newCredentialRefreshTestWorker(t, repository, refresher, CredentialRefreshWorkerConfig{Concurrency: 2}, now)
	worker.pageSize = 2
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	repository.mu.Lock()
	offsets := append([]int(nil), repository.offsets...)
	repository.mu.Unlock()
	if !reflect.DeepEqual(offsets, []int{0, 2, 4}) {
		t.Fatalf("mailbox offsets = %v", offsets)
	}
	sort.Strings(refreshed)
	want := []string{"mbx_page_a", "mbx_page_b", "mbx_page_c", "mbx_page_d", "mbx_page_e"}
	if !reflect.DeepEqual(refreshed, want) {
		t.Fatalf("refreshed mailboxes = %v", refreshed)
	}
}

func dueWorkerCredential(mailboxID string, kind domain.CredentialKind, now time.Time) domain.MailboxCredential {
	expired := now.Add(-time.Minute)
	return domain.MailboxCredential{
		MailboxID: mailboxID, Kind: kind, ExpiresAt: &expired,
		RefreshStatus: "active", UpdatedAt: now.Add(-time.Hour),
	}
}

func newCredentialRefreshTestWorker(t *testing.T, repository CredentialRefreshRepository, refresher DueCredentialRefresher, config CredentialRefreshWorkerConfig, now time.Time) *CredentialRefreshWorker {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker, err := NewCredentialRefreshWorker(repository, refresher, logger, config)
	if err != nil {
		t.Fatal(err)
	}
	worker.SetClock(func() time.Time { return now })
	return worker
}

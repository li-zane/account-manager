package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const (
	defaultCredentialRefreshInterval     = time.Minute
	defaultCredentialRefreshConcurrency  = 4
	defaultCredentialRefreshItemTimeout  = 45 * time.Second
	defaultCredentialRefreshErrorBackoff = 15 * time.Minute
	defaultCredentialRefreshPageSize     = 100
	maxCredentialRefreshConcurrency      = 64
)

// CredentialRefreshRepository is the read-only repository surface needed to
// discover credentials. Refresh persistence remains owned by the refresher.
type CredentialRefreshRepository interface {
	ListMailboxes(context.Context, ports.ListOptions) ([]domain.Mailbox, error)
	ListCredentials(context.Context, string) ([]domain.MailboxCredential, error)
}

// DueCredentialRefresher refreshes one persisted credential if it is still due.
type DueCredentialRefresher interface {
	RefreshDueCredential(context.Context, string, domain.CredentialKind) (domain.MailboxCredential, error)
}

// CredentialRefreshSettingsReader provides the persisted runtime switch and
// lead time. It is read for every pass so settings changes apply without a
// process restart.
type CredentialRefreshSettingsReader interface {
	Get(context.Context) (TokenRefreshSettings, error)
}

type CredentialRefreshWorkerConfig struct {
	Enabled      bool
	Interval     time.Duration
	Concurrency  int
	ItemTimeout  time.Duration
	ErrorBackoff time.Duration
}

type CredentialRefreshWorker struct {
	repository CredentialRefreshRepository
	refresher  DueCredentialRefresher
	settings   CredentialRefreshSettingsReader
	logger     *slog.Logger
	config     CredentialRefreshWorkerConfig
	clock      func() time.Time
	pageSize   int
}

func NewCredentialRefreshWorker(repository CredentialRefreshRepository, refresher DueCredentialRefresher, logger *slog.Logger, config CredentialRefreshWorkerConfig) (*CredentialRefreshWorker, error) {
	if repository == nil || refresher == nil {
		return nil, fmt.Errorf("%w: credential refresh repository and refresher are required", domain.ErrInvalid)
	}
	if logger == nil {
		logger = slog.Default()
	}
	config = normalizeCredentialRefreshWorkerConfig(config)
	return &CredentialRefreshWorker{
		repository: repository,
		refresher:  refresher,
		logger:     logger,
		config:     config,
		clock:      time.Now,
		pageSize:   defaultCredentialRefreshPageSize,
	}, nil
}

func normalizeCredentialRefreshWorkerConfig(config CredentialRefreshWorkerConfig) CredentialRefreshWorkerConfig {
	if config.Interval <= 0 {
		config.Interval = defaultCredentialRefreshInterval
	}
	if config.Concurrency <= 0 {
		config.Concurrency = defaultCredentialRefreshConcurrency
	}
	if config.Concurrency > maxCredentialRefreshConcurrency {
		config.Concurrency = maxCredentialRefreshConcurrency
	}
	if config.ItemTimeout <= 0 {
		config.ItemTimeout = defaultCredentialRefreshItemTimeout
	}
	if config.ErrorBackoff <= 0 {
		config.ErrorBackoff = defaultCredentialRefreshErrorBackoff
	}
	return config
}

func (w *CredentialRefreshWorker) SetClock(clock func() time.Time) {
	if clock != nil {
		w.clock = clock
	}
}

func (w *CredentialRefreshWorker) SetSettingsReader(settings CredentialRefreshSettingsReader) {
	w.settings = settings
}

// Run executes a pass immediately and then waits Interval between passes. A
// disabled worker still follows the parent context's lifecycle.
func (w *CredentialRefreshWorker) Run(ctx context.Context) error {
	if !w.config.Enabled {
		<-ctx.Done()
		return nil
	}

	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.ErrorContext(ctx, "credential refresh pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce scans every mailbox and refreshes credentials that are due. Errors
// from independent mailboxes and credentials are joined after the pass.
func (w *CredentialRefreshWorker) RunOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	now := w.clock().UTC()
	leadTime := time.Duration(0)
	if w.settings != nil {
		settings, err := w.settings.Get(ctx)
		if err != nil {
			return fmt.Errorf("load credential refresh settings: %w", err)
		}
		if !settings.Enabled {
			return nil
		}
		leadTime = time.Duration(settings.LeadTimeMinutes) * time.Minute
	}
	offset := 0
	var passErrors []error
	for {
		mailboxes, err := w.repository.ListMailboxes(ctx, ports.ListOptions{Limit: w.pageSize, Offset: offset})
		if err != nil {
			passErrors = append(passErrors, fmt.Errorf("list mailboxes at offset %d: %w", offset, err))
			return errors.Join(passErrors...)
		}

		jobs := make([]credentialRefreshJob, 0)
		for _, mailbox := range mailboxes {
			if mailbox.Status == domain.MailboxStatusDisabled {
				continue
			}
			credentials, err := w.repository.ListCredentials(ctx, mailbox.ID)
			if err != nil {
				passErrors = append(passErrors, fmt.Errorf("list credentials for mailbox %q: %w", mailbox.ID, err))
				continue
			}
			for _, credential := range credentials {
				if w.shouldRefresh(credential, now, leadTime) {
					jobs = append(jobs, credentialRefreshJob{mailboxID: mailbox.ID, kind: credential.Kind})
				}
			}
		}

		if err := w.runRefreshJobs(ctx, jobs); err != nil {
			passErrors = append(passErrors, err)
		}
		if err := ctx.Err(); err != nil {
			passErrors = append(passErrors, err)
			return errors.Join(passErrors...)
		}
		if len(mailboxes) < w.pageSize {
			return errors.Join(passErrors...)
		}
		offset += len(mailboxes)
	}
}

func (w *CredentialRefreshWorker) shouldRefresh(credential domain.MailboxCredential, now time.Time, leadTime time.Duration) bool {
	if !refreshableCredential(credential.Kind) {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(credential.RefreshStatus))
	if status == "missing" {
		return false
	}
	if status == "error" || strings.TrimSpace(credential.LastRefreshError) != "" {
		if credential.UpdatedAt.Add(w.config.ErrorBackoff).After(now) {
			return false
		}
	}
	if leadTime > 0 {
		switch status {
		case "expired", "due", "error":
			return true
		}
		if credential.ExpiresAt == nil {
			return true
		}
		return !credential.ExpiresAt.After(now.Add(leadTime))
	}
	return credentialNeedsRefresh(credential, now)
}

type credentialRefreshJob struct {
	mailboxID string
	kind      domain.CredentialKind
}

func (w *CredentialRefreshWorker) runRefreshJobs(ctx context.Context, pending []credentialRefreshJob) error {
	if len(pending) == 0 {
		return nil
	}

	workerCount := w.config.Concurrency
	if workerCount > len(pending) {
		workerCount = len(pending)
	}
	jobs := make(chan credentialRefreshJob)
	var group sync.WaitGroup
	var errorsMu sync.Mutex
	var refreshErrors []error
	group.Add(workerCount)
	for index := 0; index < workerCount; index++ {
		go func() {
			defer group.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					itemCtx, cancel := context.WithTimeout(ctx, w.config.ItemTimeout)
					_, err := w.refresher.RefreshDueCredential(itemCtx, job.mailboxID, job.kind)
					cancel()
					if err != nil {
						errorsMu.Lock()
						refreshErrors = append(refreshErrors, fmt.Errorf("refresh mailbox %q credential %q: %w", job.mailboxID, job.kind, err))
						errorsMu.Unlock()
					}
				}
			}
		}()
	}

sendJobs:
	for _, job := range pending {
		select {
		case <-ctx.Done():
			break sendJobs
		case jobs <- job:
		}
	}
	close(jobs)
	group.Wait()
	return errors.Join(refreshErrors...)
}

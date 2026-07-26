package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type MessageProbeWorkerConfig struct {
	Enabled     bool
	Heartbeat   time.Duration
	Concurrency int
	ItemTimeout time.Duration
}

type MessageProbeWorker struct {
	mailboxes ports.MailboxRepository
	states    ports.MessageCacheRepository
	cache     *MessageCacheService
	settings  *MessageProbeSettingsService
	logger    *slog.Logger
	config    MessageProbeWorkerConfig
	clock     func() time.Time
}

type messageProbeJob struct {
	input CachedMessagesInput
}

func NewMessageProbeWorker(mailboxes ports.MailboxRepository, states ports.MessageCacheRepository, cache *MessageCacheService, settings *MessageProbeSettingsService, logger *slog.Logger, config MessageProbeWorkerConfig) (*MessageProbeWorker, error) {
	if mailboxes == nil || states == nil || cache == nil || settings == nil {
		return nil, fmt.Errorf("%w: message probe worker dependencies are required", domain.ErrInvalid)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if config.Heartbeat <= 0 {
		config.Heartbeat = time.Minute
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 4
	}
	if config.Concurrency > 32 {
		config.Concurrency = 32
	}
	if config.ItemTimeout <= 0 {
		config.ItemTimeout = 45 * time.Second
	}
	return &MessageProbeWorker{mailboxes: mailboxes, states: states, cache: cache, settings: settings, logger: logger, config: config, clock: time.Now}, nil
}

func (w *MessageProbeWorker) SetClock(clock func() time.Time) {
	if clock != nil {
		w.clock = clock
	}
}

func (w *MessageProbeWorker) Run(ctx context.Context) error {
	if !w.config.Enabled {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(w.config.Heartbeat)
	defer ticker.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger.ErrorContext(ctx, "message probe pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *MessageProbeWorker) RunOnce(ctx context.Context) error {
	settings, err := w.settings.Get(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return nil
	}
	dueBefore := w.clock().UTC().Add(-time.Duration(settings.IntervalMinutes) * time.Minute)
	jobs := make([]messageProbeJob, 0)
	for offset := 0; ; offset += 500 {
		mailboxes, err := w.mailboxes.ListMailboxes(ctx, ports.ListOptions{Limit: 500, Offset: offset})
		if err != nil {
			return err
		}
		for _, mailbox := range mailboxes {
			if mailbox.Status == domain.MailboxStatusDisabled {
				continue
			}
			jobs = w.appendDueTarget(ctx, jobs, mailbox.ID, "", dueBefore)
		}
		if len(mailboxes) < 500 {
			break
		}
	}
	return w.runJobs(ctx, jobs)
}

func (w *MessageProbeWorker) appendDueTarget(ctx context.Context, jobs []messageProbeJob, mailboxID, aliasID string, dueBefore time.Time) []messageProbeJob {
	targetID := mailboxID
	if aliasID != "" {
		targetID = aliasID
	}
	for _, folder := range []domain.MessageFolder{domain.MessageFolderInbox, domain.MessageFolderJunk} {
		state, err := w.states.GetMessageSyncState(ctx, targetID, folder)
		if err == nil && state.LastSyncedAt.After(dueBefore) {
			continue
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			continue
		}
		jobs = append(jobs, messageProbeJob{input: CachedMessagesInput{MailboxID: mailboxID, AliasID: aliasID, Folder: folder}})
	}
	return jobs
}

func (w *MessageProbeWorker) runJobs(ctx context.Context, jobs []messageProbeJob) error {
	if len(jobs) == 0 {
		return nil
	}
	workerCount := min(w.config.Concurrency, len(jobs))
	queue := make(chan messageProbeJob)
	var group sync.WaitGroup
	var errorsMu sync.Mutex
	var jobErrors []error
	group.Add(workerCount)
	for range workerCount {
		go func() {
			defer group.Done()
			for job := range queue {
				itemCtx, cancel := context.WithTimeout(ctx, w.config.ItemTimeout)
				_, err := w.cache.Sync(itemCtx, job.input)
				cancel()
				if err != nil {
					errorsMu.Lock()
					jobErrors = append(jobErrors, fmt.Errorf("probe target: %w", err))
					errorsMu.Unlock()
				}
			}
		}()
	}
	for _, job := range jobs {
		select {
		case queue <- job:
		case <-ctx.Done():
			close(queue)
			group.Wait()
			return errors.Join(append(jobErrors, ctx.Err())...)
		}
	}
	close(queue)
	group.Wait()
	return errors.Join(jobErrors...)
}

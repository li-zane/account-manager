package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

const maxInitialSyncBatches = 10000

// InitialMessageSyncWorker drains provider pagination after import. Each batch
// checkpoints its cursor before the next one starts, so process restarts never
// require a completed mailbox to begin again.
type InitialMessageSyncWorker struct {
	cache       *MessageCacheService
	logger      *slog.Logger
	queue       chan string
	concurrency int
}

func NewInitialMessageSyncWorker(cache *MessageCacheService, logger *slog.Logger, concurrency int) (*InitialMessageSyncWorker, error) {
	if cache == nil {
		return nil, fmt.Errorf("%w: initial message sync cache is required", domain.ErrInvalid)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency <= 0 {
		concurrency = 2
	}
	return &InitialMessageSyncWorker{cache: cache, logger: logger, queue: make(chan string, 1024), concurrency: concurrency}, nil
}

func (w *InitialMessageSyncWorker) ScheduleInitialSync(ctx context.Context, mailboxIDs []string) error {
	seen := make(map[string]struct{}, len(mailboxIDs))
	for _, raw := range mailboxIDs {
		mailboxID := strings.TrimSpace(raw)
		if mailboxID == "" {
			continue
		}
		if _, exists := seen[mailboxID]; exists {
			continue
		}
		seen[mailboxID] = struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case w.queue <- mailboxID:
		}
	}
	return nil
}

func (w *InitialMessageSyncWorker) Run(ctx context.Context) error {
	var workers sync.WaitGroup
	workers.Add(w.concurrency)
	for range w.concurrency {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case mailboxID := <-w.queue:
					if err := w.syncMailbox(ctx, mailboxID); err != nil && ctx.Err() == nil {
						w.logger.WarnContext(ctx, "initial mailbox sync failed", "mailbox_id", mailboxID, "error", err)
					}
				}
			}
		}()
	}
	<-ctx.Done()
	workers.Wait()
	return nil
}

func (w *InitialMessageSyncWorker) syncMailbox(ctx context.Context, mailboxID string) error {
	for _, folder := range []domain.MessageFolder{domain.MessageFolderInbox, domain.MessageFolderJunk} {
		complete := false
		for batch := 0; batch < maxInitialSyncBatches; batch++ {
			result, err := w.cache.Sync(ctx, CachedMessagesInput{MailboxID: mailboxID, Folder: folder, Limit: 1})
			if err != nil {
				return fmt.Errorf("%s: %w", folder, err)
			}
			if result.Complete {
				complete = true
				break
			}
		}
		if !complete {
			return fmt.Errorf("%s: initial sync exceeded %d batches", folder, maxInitialSyncBatches)
		}
	}
	return nil
}

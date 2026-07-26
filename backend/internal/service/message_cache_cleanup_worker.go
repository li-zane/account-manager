package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/li-zane/account-manager/backend/internal/ports"
)

type MessageCacheCleanupWorker struct {
	repository   ports.MessageCacheRepository
	logger       *slog.Logger
	interval     time.Duration
	retention    time.Duration
	maxPerFolder int
	batchSize    int
	clock        func() time.Time
}

func NewMessageCacheCleanupWorker(repository ports.MessageCacheRepository, logger *slog.Logger, interval, retention time.Duration, maxPerFolder, batchSize int) *MessageCacheCleanupWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	if maxPerFolder <= 0 {
		maxPerFolder = 5000
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	return &MessageCacheCleanupWorker{repository: repository, logger: logger, interval: interval, retention: retention, maxPerFolder: maxPerFolder, batchSize: batchSize, clock: time.Now}
}

func (w *MessageCacheCleanupWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger.WarnContext(ctx, "message cache cleanup failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *MessageCacheCleanupWorker) RunOnce(ctx context.Context) (int, error) {
	total := 0
	for {
		deleted, err := w.repository.CleanupCachedMessages(ctx, w.clock().UTC().Add(-w.retention), w.maxPerFolder, w.batchSize)
		total += deleted
		if err != nil || deleted < w.batchSize {
			return total, err
		}
	}
}

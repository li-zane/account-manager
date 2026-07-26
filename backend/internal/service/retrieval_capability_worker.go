package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/ports"
)

type RetrievalCapabilityProber interface {
	ProbeMailbox(context.Context, string) error
}

type RetrievalCapabilityWorker struct {
	repository  ports.RetrievalCapabilityRepository
	prober      RetrievalCapabilityProber
	logger      *slog.Logger
	interval    time.Duration
	concurrency int
}

func NewRetrievalCapabilityWorker(repository ports.RetrievalCapabilityRepository, prober RetrievalCapabilityProber, logger *slog.Logger, interval time.Duration, concurrency int) *RetrievalCapabilityWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = time.Minute
	}
	if concurrency <= 0 {
		concurrency = 2
	}
	return &RetrievalCapabilityWorker{repository: repository, prober: prober, logger: logger, interval: interval, concurrency: min(concurrency, 16)}
}

func (w *RetrievalCapabilityWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger.WarnContext(ctx, "retrieval capability probe pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *RetrievalCapabilityWorker) RunOnce(ctx context.Context) error {
	items, err := w.repository.ListPendingRetrievalCapabilities(ctx, 200)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, item := range items {
		if _, ok := seen[item.MailboxID]; !ok {
			seen[item.MailboxID] = struct{}{}
			ids = append(ids, item.MailboxID)
		}
	}
	queue := make(chan string)
	var group sync.WaitGroup
	var mu sync.Mutex
	var probeErrors []error
	for range min(w.concurrency, len(ids)) {
		group.Add(1)
		go func() {
			defer group.Done()
			for id := range queue {
				itemCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				err := w.prober.ProbeMailbox(itemCtx, id)
				cancel()
				if err != nil {
					mu.Lock()
					probeErrors = append(probeErrors, err)
					mu.Unlock()
				}
			}
		}()
	}
	for _, id := range ids {
		select {
		case queue <- id:
		case <-ctx.Done():
			close(queue)
			group.Wait()
			return ctx.Err()
		}
	}
	close(queue)
	group.Wait()
	return errors.Join(probeErrors...)
}

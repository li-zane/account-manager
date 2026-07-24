package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const defaultWorkerPollInterval = 2 * time.Second

type workerRepository interface {
	ports.BackupRepository
	ports.BackupRunClaimer
	ports.BackupOperationLocker
}

type BackupStoreFactory func(ctx context.Context, kind domain.BackupTargetKind, config json.RawMessage) (ports.BackupStore, error)

type Worker struct {
	repository   workerRepository
	secret       ports.SecretBroker
	source       SnapshotSource
	restorer     SnapshotRestorer
	storeFactory BackupStoreFactory
	logger       *slog.Logger
	clock        func() time.Time
	newID        func(string) (string, error)
	pollInterval time.Duration
}

func NewWorker(repository workerRepository, secret ports.SecretBroker, source SnapshotSource, storeFactory BackupStoreFactory, logger *slog.Logger) (*Worker, error) {
	if repository == nil || secret == nil || source == nil || storeFactory == nil {
		return nil, fmt.Errorf("%w: backup worker dependencies are required", domain.ErrInvalid)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	worker := &Worker{
		repository: repository, secret: secret, source: source, storeFactory: storeFactory,
		logger: logger, clock: time.Now, newID: domain.NewRandomID, pollInterval: defaultWorkerPollInterval,
	}
	if restorer, ok := source.(SnapshotRestorer); ok {
		worker.restorer = restorer
	}
	return worker, nil
}

func (w *Worker) SetClock(clock func() time.Time) {
	if clock != nil {
		w.clock = clock
	}
}

func (w *Worker) SetPollInterval(interval time.Duration) {
	if interval > 0 {
		w.pollInterval = interval
	}
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		processed, err := w.ProcessNext(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("execute backup run", "error", err)
		}
		if processed {
			continue
		}
		timer := time.NewTimer(w.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	release, acquired, err := w.repository.TryAcquireBackupOperation(ctx)
	if err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer release()

	run, err := w.repository.ClaimPendingBackupRun(ctx, w.clock().UTC())
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	target, err := w.repository.GetBackupTarget(ctx, run.TargetID)
	if err != nil {
		return true, w.failRun(ctx, run, err)
	}
	if !target.Enabled {
		return true, w.failRun(ctx, run, fmt.Errorf("%w: backup target is disabled", domain.ErrInvalid))
	}
	store, err := w.openStore(ctx, target)
	if err != nil {
		return true, w.failRun(ctx, run, err)
	}

	if err := ctx.Err(); err != nil {
		return true, w.failRun(ctx, run, err)
	}
	engine, err := NewService(w.repository, w.secret, store, w.newID)
	if err != nil {
		return true, w.failRun(ctx, run, err)
	}
	engine.SetClock(w.clock)
	completed, err := engine.Execute(ctx, run, target, w.source)
	if err != nil {
		return true, err
	}
	w.logger.Info("backup run completed", "target_id", target.ID, "run_id", completed.ID, "size_bytes", completed.SizeBytes)
	return true, nil
}

// Restore starts an exclusively leased restore and waits for it to finish.
func (w *Worker) Restore(ctx context.Context, runID string) error {
	result, err := w.StartRestore(ctx, runID)
	if err != nil {
		return err
	}
	return <-result
}

// StartRestore reserves the repository-wide operation lease before returning,
// so callers receive a conflict instead of queueing a destructive restore
// behind another snapshot or restore.
func (w *Worker) StartRestore(ctx context.Context, runID string) (<-chan error, error) {
	if strings.TrimSpace(runID) == "" || w.restorer == nil {
		return nil, fmt.Errorf("%w: backup run and worker restorer are required", domain.ErrInvalid)
	}
	release, acquired, err := w.repository.TryAcquireBackupOperation(ctx)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("%w: another backup or restore operation is active", domain.ErrConflict)
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			release()
		}
	}()

	run, err := w.repository.GetBackupRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	if run.State != domain.BackupRunSucceeded || strings.TrimSpace(run.ObjectKey) == "" || strings.TrimSpace(run.Checksum) == "" {
		return nil, fmt.Errorf("%w: backup run is not restorable", domain.ErrInvalid)
	}
	target, err := w.repository.GetBackupTarget(ctx, run.TargetID)
	if err != nil {
		return nil, err
	}
	store, err := w.openStore(ctx, target)
	if err != nil {
		return nil, err
	}
	engine, err := NewService(w.repository, w.secret, store, w.newID)
	if err != nil {
		return nil, err
	}
	result := make(chan error, 1)
	releaseOnError = false
	go func() {
		defer close(result)
		defer release()
		result <- engine.Restore(ctx, run, w.restorer)
	}()
	return result, nil
}

func (w *Worker) openStore(ctx context.Context, target domain.BackupTarget) (ports.BackupStore, error) {
	config, err := w.secret.Open(ctx, target.EncryptedConfig, target.KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("open backup target configuration: %w", err)
	}
	defer clear(config)
	store, err := w.storeFactory(ctx, target.Kind, json.RawMessage(config))
	if err != nil {
		return nil, fmt.Errorf("open backup target store: %w", err)
	}
	return store, nil
}

func (w *Worker) failRun(ctx context.Context, run domain.BackupRun, cause error) error {
	now := w.clock().UTC()
	run.State = domain.BackupRunFailed
	run.Error = boundedRunError(cause)
	run.FinishedAt = &now
	run.UpdatedAt = now
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.repository.UpdateBackupRun(persistCtx, run); err != nil {
		return fmt.Errorf("backup run failed (%v), persist failure (%w)", cause, err)
	}
	return cause
}

func boundedRunError(err error) string {
	if err == nil {
		return "backup failed"
	}
	value := strings.ToValidUTF8(strings.Join(strings.Fields(err.Error()), " "), "\uFFFD")
	if len(value) > 1000 {
		value = value[:1000]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

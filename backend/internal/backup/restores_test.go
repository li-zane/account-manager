package backup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	memoryrepo "github.com/li-zane/account-manager/backend/internal/repository/memory"
)

type controlledRestoreStarter struct {
	result chan error
	err    error
	calls  int
}

func (s *controlledRestoreStarter) StartRestore(context.Context, string) (<-chan error, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func TestRestoreCoordinatorTracksAsynchronousCompletion(t *testing.T) {
	ctx := context.Background()
	repository := memoryrepo.New()
	run := createRestorableRun(t, ctx, repository)
	starter := &controlledRestoreStarter{result: make(chan error, 1)}
	coordinator, err := NewRestoreCoordinator(ctx, starter, repository)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 8, 30, 0, 0, time.UTC)
	coordinator.SetClock(func() time.Time { return now })
	coordinator.SetIDGenerator(func(prefix string) (string, error) { return prefix + "_fixture", nil })

	operation, err := coordinator.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != domain.BackupRestoreRunning || operation.TargetID != run.TargetID {
		t.Fatalf("started restore = %+v", operation)
	}
	starter.result <- nil
	close(starter.result)
	coordinator.Wait()
	completed, err := coordinator.Get(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != domain.BackupRestoreSucceeded || completed.FinishedAt == nil {
		t.Fatalf("completed restore = %+v", completed)
	}
}

func TestRestoreCoordinatorRejectsNonRestorableRunBeforeStarting(t *testing.T) {
	ctx := context.Background()
	repository := memoryrepo.New()
	now := time.Now().UTC()
	target := domain.BackupTarget{ID: "btarget_invalid_restore", Name: "invalid restore", Kind: domain.BackupTargetS3, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	run := domain.BackupRun{ID: "brun_pending_restore", TargetID: target.ID, State: domain.BackupRunPending, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateBackupRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	starter := &controlledRestoreStarter{result: make(chan error, 1)}
	coordinator, err := NewRestoreCoordinator(ctx, starter, repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(ctx, run.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("pending restore error = %v, want invalid", err)
	}
	if starter.calls != 0 {
		t.Fatalf("restore starter calls = %d, want 0", starter.calls)
	}
}

func TestWorkerStartRestoreConflictsWithActiveBackupOperation(t *testing.T) {
	ctx := context.Background()
	repository := memoryrepo.New()
	broker := testBackupBroker(t)
	target := createWorkerTarget(t, ctx, repository, broker)
	now := time.Now().UTC()
	run := domain.BackupRun{
		ID: "brun_locked_restore", TargetID: target.ID, State: domain.BackupRunSucceeded,
		ObjectKey: "account-manager/locked.snapshot", Checksum: "checksum", CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateBackupRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(
		repository,
		broker,
		snapshotRestoreFixture{bytesSource: bytesSource("snapshot"), bytesRestorer: &bytesRestorer{}},
		func(context.Context, domain.BackupTargetKind, json.RawMessage) (ports.BackupStore, error) {
			return &memoryStore{objects: make(map[string][]byte)}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	release, acquired, err := repository.TryAcquireBackupOperation(ctx)
	if err != nil || !acquired {
		t.Fatalf("acquire operation lease: acquired=%v err=%v", acquired, err)
	}
	defer release()
	if _, err := worker.StartRestore(ctx, run.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("start restore error = %v, want conflict", err)
	}
}

func createRestorableRun(t *testing.T, ctx context.Context, repository *memoryrepo.Store) domain.BackupRun {
	t.Helper()
	now := time.Now().UTC()
	target := domain.BackupTarget{ID: "btarget_restore", Name: "restore", Kind: domain.BackupTargetS3, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	run := domain.BackupRun{
		ID: "brun_restore", TargetID: target.ID, State: domain.BackupRunSucceeded,
		ObjectKey: "account-manager/fixture.snapshot", Checksum: "checksum",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateBackupRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	return run
}

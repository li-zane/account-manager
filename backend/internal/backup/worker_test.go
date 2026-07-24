package backup

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	memoryrepo "github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
)

func TestSchedulerDeduplicatesConcurrentScheduleWindow(t *testing.T) {
	ctx := context.Background()
	repository := memoryrepo.New()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	target := domain.BackupTarget{
		ID: "btarget_scheduled", Name: "scheduled", Kind: domain.BackupTargetS3,
		Enabled: true, Schedule: "@every 1h", RetentionCount: 14,
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}
	if err := repository.CreateBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Int64
	const schedulers = 16
	var group sync.WaitGroup
	errorsFound := make(chan error, schedulers)
	for range schedulers {
		group.Add(1)
		go func() {
			defer group.Done()
			scheduler, err := NewScheduler(repository, nil)
			if err != nil {
				errorsFound <- err
				return
			}
			scheduler.SetClock(func() time.Time { return now })
			scheduler.SetIDGenerator(func(string) (string, error) {
				return "brun_" + time.Unix(0, sequence.Add(1)).Format("150405.000000000"), nil
			})
			if err := scheduler.RunOnce(ctx); err != nil {
				errorsFound <- err
			}
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	runs, err := repository.ListBackupRuns(ctx, target.ID, ports.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].State != domain.BackupRunPending {
		t.Fatalf("scheduled runs = %+v, want one pending run", runs)
	}
}

func TestParseScheduleSupportsFriendlyNamesAndTimezone(t *testing.T) {
	for _, value := range []string{"daily", "weekly", "six-hours", "@every 30m", "CRON_TZ=Asia/Shanghai 15 2 * * *"} {
		if _, err := ParseSchedule(value); err != nil {
			t.Errorf("ParseSchedule(%q): %v", value, err)
		}
	}
}

func TestWorkerExecutesAndRestoresQueuedRun(t *testing.T) {
	ctx := context.Background()
	repository := memoryrepo.New()
	broker := testBackupBroker(t)
	target := createWorkerTarget(t, ctx, repository, broker)
	run := domain.BackupRun{
		ID: "brun_worker", TargetID: target.ID, State: domain.BackupRunPending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repository.CreateBackupRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	objectStore := &memoryStore{objects: make(map[string][]byte)}
	var openedConfig []byte
	restorer := &bytesRestorer{}
	snapshotTools := snapshotRestoreFixture{bytesSource: bytesSource("postgres-custom-format"), bytesRestorer: restorer}
	worker, err := NewWorker(repository, broker, snapshotTools, func(_ context.Context, kind domain.BackupTargetKind, config json.RawMessage) (ports.BackupStore, error) {
		if kind != domain.BackupTargetS3 {
			t.Fatalf("store kind = %q", kind)
		}
		openedConfig = append([]byte(nil), config...)
		return objectStore, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !processed || !json.Valid(openedConfig) {
		t.Fatalf("processed = %v, config = %q", processed, openedConfig)
	}
	completed, err := repository.GetBackupRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != domain.BackupRunSucceeded || completed.ObjectKey == "" {
		t.Fatalf("completed run = %+v", completed)
	}
	if err := worker.Restore(ctx, completed.ID); err != nil {
		t.Fatal(err)
	}
	if string(restorer.value) != "postgres-custom-format" {
		t.Fatalf("restored snapshot = %q", restorer.value)
	}
}

func TestWorkerCancellationPersistsFailedRun(t *testing.T) {
	repository := memoryrepo.New()
	broker := testBackupBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	target := createWorkerTarget(t, ctx, repository, broker)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	run := domain.BackupRun{ID: "brun_cancel", TargetID: target.ID, State: domain.BackupRunPending, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateBackupRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	source := &waitingSnapshotSource{started: make(chan struct{})}
	worker, err := NewWorker(repository, broker, source, func(context.Context, domain.BackupTargetKind, json.RawMessage) (ports.BackupStore, error) {
		return &memoryStore{objects: make(map[string][]byte)}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker.SetClock(func() time.Time { return now })
	worker.SetPollInterval(time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start the snapshot")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	persisted, err := repository.GetBackupRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != domain.BackupRunFailed || persisted.FinishedAt == nil || persisted.Error != context.Canceled.Error() {
		t.Fatalf("persisted canceled run = %+v", persisted)
	}
}

type snapshotRestoreFixture struct {
	bytesSource
	*bytesRestorer
}

type waitingSnapshotSource struct {
	started chan struct{}
	once    sync.Once
}

func (s *waitingSnapshotSource) Snapshot(ctx context.Context) (io.ReadCloser, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func testBackupBroker(t *testing.T) ports.SecretBroker {
	t.Helper()
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func createWorkerTarget(t *testing.T, ctx context.Context, repository *memoryrepo.Store, broker ports.SecretBroker) domain.BackupTarget {
	t.Helper()
	sealed, keyVersion, err := broker.Seal(ctx, []byte(`{"bucket":"fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := domain.BackupTarget{
		ID: "btarget_worker", Name: "worker", Kind: domain.BackupTargetS3,
		EncryptedConfig: sealed, KeyVersion: keyVersion, Enabled: true, RetentionCount: 14,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	return target
}

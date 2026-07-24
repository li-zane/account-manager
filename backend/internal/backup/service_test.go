package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/security"
)

type memoryBackupRepository struct {
	runs map[string]domain.BackupRun
}

func (r *memoryBackupRepository) CreateBackupTarget(context.Context, domain.BackupTarget) error {
	return nil
}
func (r *memoryBackupRepository) GetBackupTarget(context.Context, string) (domain.BackupTarget, error) {
	return domain.BackupTarget{}, domain.ErrNotFound
}
func (r *memoryBackupRepository) ListBackupTargets(context.Context, ports.ListOptions) ([]domain.BackupTarget, error) {
	return nil, nil
}
func (r *memoryBackupRepository) UpdateBackupTarget(context.Context, domain.BackupTarget, int64) error {
	return nil
}
func (r *memoryBackupRepository) CreateBackupRun(_ context.Context, run domain.BackupRun) error {
	r.runs[run.ID] = run
	return nil
}
func (r *memoryBackupRepository) GetBackupRun(_ context.Context, id string) (domain.BackupRun, error) {
	run, ok := r.runs[id]
	if !ok {
		return domain.BackupRun{}, domain.ErrNotFound
	}
	return run, nil
}
func (r *memoryBackupRepository) ListBackupRuns(context.Context, string, ports.ListOptions) ([]domain.BackupRun, error) {
	return nil, nil
}
func (r *memoryBackupRepository) UpdateBackupRun(ctx context.Context, run domain.BackupRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.runs[run.ID] = run
	return nil
}

type memoryStore struct {
	objects map[string][]byte
}

func (s *memoryStore) Put(_ context.Context, key string, body io.Reader) (ports.BackupObject, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return ports.BackupObject{}, err
	}
	s.objects[key] = data
	return ports.BackupObject{ObjectKey: key, SizeBytes: int64(len(data))}, nil
}
func (s *memoryStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *memoryStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

type bytesSource []byte

func (s bytesSource) Snapshot(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s)), nil
}

type cancelingSource struct{ cancel context.CancelFunc }

func (s cancelingSource) Snapshot(ctx context.Context) (io.ReadCloser, error) {
	s.cancel()
	return nil, ctx.Err()
}

type failingSource struct{ err error }

func (s failingSource) Snapshot(context.Context) (io.ReadCloser, error) {
	return nil, s.err
}

type bytesRestorer struct{ value []byte }

func (r *bytesRestorer) Restore(_ context.Context, snapshot io.Reader) error {
	value, err := io.ReadAll(snapshot)
	r.value = value
	return err
}

func TestRunEncryptsAndRestoreVerifiesSnapshot(t *testing.T) {
	repository := &memoryBackupRepository{runs: make(map[string]domain.BackupRun)}
	store := &memoryStore{objects: make(map[string][]byte)}
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, broker, store, func(string) (string, error) { return "brun_test", nil })
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 7, 24, 2, 15, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return clock })
	raw := []byte("postgres-custom-format-snapshot")
	run, err := service.Run(context.Background(), domain.BackupTarget{ID: "target_1", Kind: domain.BackupTargetS3}, bytesSource(raw))
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.BackupRunSucceeded || run.Checksum == "" || run.SizeBytes == 0 {
		t.Fatalf("unexpected run: %+v", run)
	}
	if bytes.Contains(store.objects[run.ObjectKey], raw) {
		t.Fatal("backup object contains plaintext")
	}
	restorer := &bytesRestorer{}
	if err := service.Restore(context.Background(), run, restorer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restorer.value, raw) {
		t.Fatalf("restored = %q", restorer.value)
	}
}

func TestRestoreRejectsTamperedObject(t *testing.T) {
	repository := &memoryBackupRepository{runs: make(map[string]domain.BackupRun)}
	store := &memoryStore{objects: make(map[string][]byte)}
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	service, _ := NewService(repository, broker, store, func(string) (string, error) { return "brun_test", nil })
	run, err := service.Run(context.Background(), domain.BackupTarget{ID: "target_1"}, bytesSource("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	store.objects[run.ObjectKey][0] ^= 0xff
	if err := service.Restore(context.Background(), run, &bytesRestorer{}); err == nil {
		t.Fatal("expected checksum error")
	}
}

func TestExecuteUsesQueuedRunAndPersistsCancellation(t *testing.T) {
	repository := &memoryBackupRepository{runs: make(map[string]domain.BackupRun)}
	store := &memoryStore{objects: make(map[string][]byte)}
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	service, _ := NewService(repository, broker, store, func(string) (string, error) { return "unused", nil })
	now := time.Date(2026, 7, 24, 2, 15, 0, 0, time.UTC)
	queued := domain.BackupRun{ID: "brun_queued", TargetID: "target_1", State: domain.BackupRunPending, CreatedAt: now, UpdatedAt: now}
	repository.runs[queued.ID] = queued
	ctx, cancel := context.WithCancel(context.Background())
	run, err := service.Execute(ctx, queued, domain.BackupTarget{ID: "target_1"}, cancelingSource{cancel: cancel})
	if err == nil {
		t.Fatal("expected canceled snapshot error")
	}
	if len(repository.runs) != 1 || run.ID != queued.ID || run.State != domain.BackupRunFailed {
		t.Fatalf("executed run = %+v; repository = %+v", run, repository.runs)
	}
	persisted := repository.runs[queued.ID]
	if persisted.State != domain.BackupRunFailed || persisted.FinishedAt == nil {
		t.Fatalf("persisted run = %+v", persisted)
	}
}

func TestRunBoundsPersistedFailureDetails(t *testing.T) {
	repository := &memoryBackupRepository{runs: make(map[string]domain.BackupRun)}
	store := &memoryStore{objects: make(map[string][]byte)}
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	service, _ := NewService(repository, broker, store, func(string) (string, error) { return "brun_failed", nil })
	longMessage := "provider failure\n" + strings.Repeat("detail ", 300)
	run, err := service.Run(context.Background(), domain.BackupTarget{ID: "target_1"}, failingSource{err: errors.New(longMessage)})
	if err == nil {
		t.Fatal("expected snapshot failure")
	}
	if run.State != domain.BackupRunFailed || len(run.Error) != 1000 {
		t.Fatalf("failed run state = %q, error length = %d", run.State, len(run.Error))
	}
	if strings.ContainsAny(run.Error, "\r\n\t") {
		t.Fatalf("persisted error is not normalized: %q", run.Error)
	}
	persisted := repository.runs[run.ID]
	if persisted.Error != run.Error {
		t.Fatalf("persisted error length = %d, returned error length = %d", len(persisted.Error), len(run.Error))
	}
}

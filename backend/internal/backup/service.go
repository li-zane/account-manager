package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

// SnapshotSource supplies a consistent database/file snapshot. The backup
// service never knows whether it came from pg_dump, a filesystem copy, or a
// future tenant export.
type SnapshotSource interface {
	Snapshot(ctx context.Context) (io.ReadCloser, error)
}

// SnapshotRestorer owns database-specific locking and transactional restore
// behavior. The service validates and decrypts the object before invoking it.
type SnapshotRestorer interface {
	Restore(ctx context.Context, snapshot io.Reader) error
}

type Service struct {
	repository ports.BackupRepository
	secret     ports.SecretBroker
	store      ports.BackupStore
	clock      func() time.Time
	newID      func(prefix string) (string, error)
}

func NewService(repository ports.BackupRepository, secret ports.SecretBroker, store ports.BackupStore, newID func(string) (string, error)) (*Service, error) {
	if repository == nil || secret == nil || store == nil || newID == nil {
		return nil, fmt.Errorf("%w: backup dependencies are required", domain.ErrInvalid)
	}
	return &Service{repository: repository, secret: secret, store: store, clock: time.Now, newID: newID}, nil
}

func (s *Service) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

// Run creates an encrypted, checksummed snapshot and uploads it through the
// selected storage port. No provider configuration or raw database bytes are
// returned to the HTTP layer.
func (s *Service) Run(ctx context.Context, target domain.BackupTarget, source SnapshotSource) (domain.BackupRun, error) {
	if target.ID == "" || source == nil {
		return domain.BackupRun{}, fmt.Errorf("%w: backup target and source are required", domain.ErrInvalid)
	}
	runID, err := s.newID("brun")
	if err != nil {
		return domain.BackupRun{}, err
	}
	now := s.clock().UTC()
	run := domain.BackupRun{ID: runID, TargetID: target.ID, State: domain.BackupRunPending, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.CreateBackupRun(ctx, run); err != nil {
		return domain.BackupRun{}, err
	}
	return s.Execute(ctx, run, target, source)
}

// Execute processes an existing queued run so workers do not create a second
// run and leave the originally claimed pending row behind.
func (s *Service) Execute(ctx context.Context, run domain.BackupRun, target domain.BackupTarget, source SnapshotSource) (domain.BackupRun, error) {
	if run.ID == "" || target.ID == "" || source == nil {
		return domain.BackupRun{}, fmt.Errorf("%w: backup run, target, and source are required", domain.ErrInvalid)
	}
	if run.TargetID != target.ID || (run.State != domain.BackupRunPending && run.State != domain.BackupRunRunning) {
		return domain.BackupRun{}, fmt.Errorf("%w: backup run is not executable for this target", domain.ErrInvalid)
	}
	now := s.clock().UTC()
	if run.State == domain.BackupRunPending || run.StartedAt == nil {
		run.State = domain.BackupRunRunning
		run.StartedAt = &now
		run.UpdatedAt = now
		if err := s.repository.UpdateBackupRun(ctx, run); err != nil {
			return domain.BackupRun{}, err
		}
	}
	reader, err := source.Snapshot(ctx)
	if err != nil {
		return s.fail(ctx, run, err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		return s.fail(ctx, run, err)
	}
	sealed, keyVersion, err := s.secret.Seal(ctx, raw)
	if err != nil {
		return s.fail(ctx, run, err)
	}
	envelope, err := json.Marshal(snapshotEnvelope{Format: "account-manager.snapshot.v1", KeyVersion: keyVersion, Payload: sealed})
	if err != nil {
		return s.fail(ctx, run, err)
	}
	checksum := sha256.Sum256(envelope)
	objectKey := fmt.Sprintf("account-manager/%s/%s.snapshot", now.Format("2006/01/02"), run.ID)
	object, err := s.store.Put(ctx, objectKey, bytes.NewReader(envelope))
	if err != nil {
		return s.fail(ctx, run, err)
	}
	finished := s.clock().UTC()
	run.State = domain.BackupRunSucceeded
	run.ObjectKey = object.ObjectKey
	run.Checksum = hex.EncodeToString(checksum[:])
	run.SizeBytes = object.SizeBytes
	run.FinishedAt = &finished
	run.UpdatedAt = finished
	if err := s.repository.UpdateBackupRun(ctx, run); err != nil {
		return domain.BackupRun{}, err
	}
	return run, nil
}

func (s *Service) Restore(ctx context.Context, run domain.BackupRun, restorer SnapshotRestorer) error {
	if run.State != domain.BackupRunSucceeded || run.ObjectKey == "" || run.Checksum == "" {
		return fmt.Errorf("%w: backup run is not restorable", domain.ErrInvalid)
	}
	if restorer == nil {
		return fmt.Errorf("%w: snapshot restorer is required", domain.ErrInvalid)
	}
	reader, err := s.store.Get(ctx, run.ObjectKey)
	if err != nil {
		return err
	}
	defer reader.Close()
	envelopeBytes, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read backup object: %w", err)
	}
	checksum := sha256.Sum256(envelopeBytes)
	if !bytes.Equal([]byte(hex.EncodeToString(checksum[:])), []byte(run.Checksum)) {
		return fmt.Errorf("%w: backup checksum mismatch", domain.ErrInvalid)
	}
	var envelope snapshotEnvelope
	if err := json.Unmarshal(envelopeBytes, &envelope); err != nil {
		return fmt.Errorf("decode backup envelope: %w", err)
	}
	if envelope.Format != "account-manager.snapshot.v1" {
		return fmt.Errorf("%w: unsupported backup format %q", domain.ErrInvalid, envelope.Format)
	}
	plaintext, err := s.secret.Open(ctx, envelope.Payload, envelope.KeyVersion)
	if err != nil {
		return err
	}
	if err := restorer.Restore(ctx, bytes.NewReader(plaintext)); err != nil {
		return fmt.Errorf("restore snapshot: %w", err)
	}
	return nil
}

func (s *Service) fail(ctx context.Context, run domain.BackupRun, cause error) (domain.BackupRun, error) {
	now := s.clock().UTC()
	run.State = domain.BackupRunFailed
	run.Error = boundedRunError(cause)
	run.FinishedAt = &now
	run.UpdatedAt = now
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.repository.UpdateBackupRun(persistCtx, run); err != nil {
		return domain.BackupRun{}, fmt.Errorf("backup failed (%v), persist failure (%w)", cause, err)
	}
	return run, cause
}

type snapshotEnvelope struct {
	Format     string `json:"format"`
	KeyVersion string `json:"key_version"`
	Payload    []byte `json:"payload"`
}

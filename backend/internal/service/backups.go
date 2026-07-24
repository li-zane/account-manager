package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	backupengine "github.com/li-zane/account-manager/backend/internal/backup"
	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const maxBackupRetentionCount = 365

type BackupService struct {
	repository ports.BackupRepository
	secrets    ports.SecretBroker
	validate   func(domain.BackupTargetKind, json.RawMessage) error
	redact     func(domain.BackupTargetKind, json.RawMessage) (json.RawMessage, error)
	clock      func() time.Time
}

func NewBackupService(repository ports.BackupRepository, secrets ports.SecretBroker) (*BackupService, error) {
	if repository == nil || secrets == nil {
		return nil, fmt.Errorf("%w: backup repository and secret broker are required", domain.ErrInvalid)
	}
	return &BackupService{repository: repository, secrets: secrets, clock: time.Now}, nil
}

func (s *BackupService) SetConfigValidator(validator func(domain.BackupTargetKind, json.RawMessage) error) {
	s.validate = validator
}

func (s *BackupService) SetConfigRedactor(redactor func(domain.BackupTargetKind, json.RawMessage) (json.RawMessage, error)) {
	s.redact = redactor
}

func (s *BackupService) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

type CreateBackupTargetInput struct {
	Name           string
	Kind           domain.BackupTargetKind
	Config         json.RawMessage
	Enabled        *bool
	Schedule       string
	RetentionCount int
	Metadata       json.RawMessage
}

type UpdateBackupTargetInput struct {
	Name           *string
	Kind           *domain.BackupTargetKind
	Config         json.RawMessage
	Enabled        *bool
	Schedule       *string
	RetentionCount *int
	Metadata       *json.RawMessage
	Version        int64
}

// BackupTargetSettings is the API-facing target representation. Config is a
// provider-generated summary which has no credential-bearing fields.
type BackupTargetSettings struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Kind           domain.BackupTargetKind `json:"kind"`
	Config         json.RawMessage         `json:"config,omitempty"`
	Configured     bool                    `json:"configured"`
	Enabled        bool                    `json:"enabled"`
	Schedule       string                  `json:"schedule,omitempty"`
	RetentionCount int                     `json:"retention_count"`
	Metadata       json.RawMessage         `json:"metadata,omitempty"`
	Version        int64                   `json:"version"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
}

func (s *BackupService) CreateTarget(ctx context.Context, input CreateBackupTargetInput) (domain.BackupTarget, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.BackupTarget{}, fmt.Errorf("%w: backup target name is required", domain.ErrInvalid)
	}
	if input.Kind != domain.BackupTargetS3 && input.Kind != domain.BackupTargetWebDAV {
		return domain.BackupTarget{}, fmt.Errorf("%w: unsupported backup target kind %q", domain.ErrInvalid, input.Kind)
	}
	if len(input.Config) == 0 || !json.Valid(input.Config) {
		return domain.BackupTarget{}, fmt.Errorf("%w: backup target config must be valid JSON", domain.ErrInvalid)
	}
	if s.validate != nil {
		if err := s.validate(input.Kind, input.Config); err != nil {
			return domain.BackupTarget{}, err
		}
	}
	schedule := strings.TrimSpace(input.Schedule)
	if schedule != "" {
		if _, err := backupengine.ParseSchedule(schedule); err != nil {
			return domain.BackupTarget{}, err
		}
	}
	sealed, keyVersion, err := s.secrets.Seal(ctx, input.Config)
	if err != nil {
		return domain.BackupTarget{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	retention := input.RetentionCount
	if retention == 0 {
		retention = 14
	}
	if retention < 1 || retention > maxBackupRetentionCount {
		return domain.BackupTarget{}, fmt.Errorf("%w: backup retention count must be between 1 and %d", domain.ErrInvalid, maxBackupRetentionCount)
	}
	id, err := domain.NewRandomID("btarget")
	if err != nil {
		return domain.BackupTarget{}, err
	}
	now := s.clock().UTC()
	target := domain.BackupTarget{
		ID:              id,
		Name:            name,
		Kind:            input.Kind,
		EncryptedConfig: sealed,
		KeyVersion:      keyVersion,
		Enabled:         enabled,
		Schedule:        schedule,
		RetentionCount:  retention,
		Metadata:        normalizedJSON(input.Metadata),
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.repository.CreateBackupTarget(ctx, target); err != nil {
		return domain.BackupTarget{}, err
	}
	return target, nil
}

func (s *BackupService) UpdateTarget(ctx context.Context, id string, input UpdateBackupTargetInput) (domain.BackupTarget, error) {
	id = strings.TrimSpace(id)
	if id == "" || input.Version <= 0 {
		return domain.BackupTarget{}, fmt.Errorf("%w: backup target id and version are required", domain.ErrInvalid)
	}
	target, err := s.repository.GetBackupTarget(ctx, id)
	if err != nil {
		return domain.BackupTarget{}, err
	}
	if target.Version != input.Version {
		return domain.BackupTarget{}, fmt.Errorf("%w: backup target version changed", domain.ErrConflict)
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return domain.BackupTarget{}, fmt.Errorf("%w: backup target name is required", domain.ErrInvalid)
		}
		target.Name = name
	}
	if input.Kind != nil {
		if *input.Kind != domain.BackupTargetS3 && *input.Kind != domain.BackupTargetWebDAV {
			return domain.BackupTarget{}, fmt.Errorf("%w: unsupported backup target kind %q", domain.ErrInvalid, *input.Kind)
		}
		if *input.Kind != target.Kind && len(input.Config) == 0 {
			return domain.BackupTarget{}, fmt.Errorf("%w: changing backup target kind requires config", domain.ErrInvalid)
		}
		target.Kind = *input.Kind
	}
	if len(input.Config) > 0 {
		if !json.Valid(input.Config) {
			return domain.BackupTarget{}, fmt.Errorf("%w: backup target config must be valid JSON", domain.ErrInvalid)
		}
		if s.validate != nil {
			if err := s.validate(target.Kind, input.Config); err != nil {
				return domain.BackupTarget{}, err
			}
		}
		sealed, keyVersion, err := s.secrets.Seal(ctx, input.Config)
		if err != nil {
			return domain.BackupTarget{}, err
		}
		target.EncryptedConfig = sealed
		target.KeyVersion = keyVersion
	}
	if input.Enabled != nil {
		target.Enabled = *input.Enabled
	}
	if input.Schedule != nil {
		schedule := strings.TrimSpace(*input.Schedule)
		if schedule != "" {
			if _, err := backupengine.ParseSchedule(schedule); err != nil {
				return domain.BackupTarget{}, err
			}
		}
		target.Schedule = schedule
	}
	if input.RetentionCount != nil {
		if *input.RetentionCount < 1 || *input.RetentionCount > maxBackupRetentionCount {
			return domain.BackupTarget{}, fmt.Errorf("%w: backup retention count must be between 1 and %d", domain.ErrInvalid, maxBackupRetentionCount)
		}
		target.RetentionCount = *input.RetentionCount
	}
	if input.Metadata != nil {
		if !json.Valid(*input.Metadata) {
			return domain.BackupTarget{}, fmt.Errorf("%w: backup target metadata must be valid JSON", domain.ErrInvalid)
		}
		target.Metadata = append(json.RawMessage(nil), (*input.Metadata)...)
	}
	target.Version = input.Version + 1
	target.UpdatedAt = s.clock().UTC()
	if err := s.repository.UpdateBackupTarget(ctx, target, input.Version); err != nil {
		return domain.BackupTarget{}, err
	}
	return target, nil
}

func (s *BackupService) ListTargets(ctx context.Context, options ports.ListOptions) ([]domain.BackupTarget, error) {
	return s.repository.ListBackupTargets(ctx, options)
}

func (s *BackupService) GetTarget(ctx context.Context, id string) (domain.BackupTarget, error) {
	return s.repository.GetBackupTarget(ctx, id)
}

func (s *BackupService) DescribeTarget(ctx context.Context, target domain.BackupTarget) (BackupTargetSettings, error) {
	settings := BackupTargetSettings{
		ID: target.ID, Name: target.Name, Kind: target.Kind,
		Configured: len(target.EncryptedConfig) > 0, Enabled: target.Enabled,
		Schedule: target.Schedule, RetentionCount: target.RetentionCount,
		Metadata: append(json.RawMessage(nil), target.Metadata...), Version: target.Version,
		CreatedAt: target.CreatedAt, UpdatedAt: target.UpdatedAt,
	}
	if !settings.Configured || s.redact == nil {
		return settings, nil
	}
	plaintext, err := s.secrets.Open(ctx, target.EncryptedConfig, target.KeyVersion)
	if err != nil {
		return BackupTargetSettings{}, fmt.Errorf("%w: backup target %s configuration is unavailable", domain.ErrInvalid, target.ID)
	}
	defer clear(plaintext)
	config, err := s.redact(target.Kind, json.RawMessage(plaintext))
	if err != nil {
		return BackupTargetSettings{}, err
	}
	settings.Config = append(json.RawMessage(nil), config...)
	return settings, nil
}

func (s *BackupService) GetTargetSettings(ctx context.Context, id string) (BackupTargetSettings, error) {
	target, err := s.GetTarget(ctx, strings.TrimSpace(id))
	if err != nil {
		return BackupTargetSettings{}, err
	}
	return s.DescribeTarget(ctx, target)
}

func (s *BackupService) ListTargetSettings(ctx context.Context, options ports.ListOptions) ([]BackupTargetSettings, error) {
	targets, err := s.ListTargets(ctx, options)
	if err != nil {
		return nil, err
	}
	items := make([]BackupTargetSettings, 0, len(targets))
	for _, target := range targets {
		item, err := s.DescribeTarget(ctx, target)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *BackupService) QueueRun(ctx context.Context, targetID string) (domain.BackupRun, error) {
	if locker, ok := s.repository.(ports.BackupOperationLocker); ok {
		release, acquired, err := locker.TryAcquireBackupOperation(ctx)
		if err != nil {
			return domain.BackupRun{}, err
		}
		if !acquired {
			return domain.BackupRun{}, fmt.Errorf("%w: another backup or restore operation is active", domain.ErrConflict)
		}
		defer release()
	}
	target, err := s.repository.GetBackupTarget(ctx, targetID)
	if err != nil {
		return domain.BackupRun{}, err
	}
	if !target.Enabled {
		return domain.BackupRun{}, fmt.Errorf("%w: backup target is disabled", domain.ErrInvalid)
	}
	id, err := domain.NewRandomID("brun")
	if err != nil {
		return domain.BackupRun{}, err
	}
	now := s.clock().UTC()
	run := domain.BackupRun{ID: id, TargetID: targetID, State: domain.BackupRunPending, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.CreateBackupRun(ctx, run); err != nil {
		return domain.BackupRun{}, err
	}
	return run, nil
}

func (s *BackupService) GetRun(ctx context.Context, id string) (domain.BackupRun, error) {
	return s.repository.GetBackupRun(ctx, id)
}

func (s *BackupService) ListRuns(ctx context.Context, targetID string, options ports.ListOptions) ([]domain.BackupRun, error) {
	return s.repository.ListBackupRuns(ctx, targetID, options)
}

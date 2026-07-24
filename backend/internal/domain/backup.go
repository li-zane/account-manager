package domain

import (
	"encoding/json"
	"time"
)

type BackupTargetKind string

const (
	BackupTargetS3     BackupTargetKind = "s3"
	BackupTargetWebDAV BackupTargetKind = "webdav"
)

type BackupTarget struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Kind            BackupTargetKind `json:"kind"`
	EncryptedConfig []byte           `json:"-"`
	KeyVersion      string           `json:"-"`
	Enabled         bool             `json:"enabled"`
	Schedule        string           `json:"schedule,omitempty"`
	RetentionCount  int              `json:"retention_count"`
	Metadata        json.RawMessage  `json:"metadata,omitempty"`
	Version         int64            `json:"version"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type BackupRunState string

const (
	BackupRunPending   BackupRunState = "pending"
	BackupRunRunning   BackupRunState = "running"
	BackupRunSucceeded BackupRunState = "succeeded"
	BackupRunFailed    BackupRunState = "failed"
)

type BackupRun struct {
	ID         string         `json:"id"`
	TargetID   string         `json:"target_id"`
	State      BackupRunState `json:"state"`
	ObjectKey  string         `json:"object_key,omitempty"`
	Checksum   string         `json:"checksum,omitempty"`
	SizeBytes  int64          `json:"size_bytes,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type BackupRestoreState string

const (
	BackupRestoreRunning   BackupRestoreState = "running"
	BackupRestoreSucceeded BackupRestoreState = "succeeded"
	BackupRestoreFailed    BackupRestoreState = "failed"
)

// BackupRestoreOperation is process-local operational state. It deliberately
// contains no target configuration, object payload, or encryption metadata.
type BackupRestoreOperation struct {
	ID         string             `json:"id"`
	RunID      string             `json:"run_id"`
	TargetID   string             `json:"target_id"`
	State      BackupRestoreState `json:"state"`
	Error      string             `json:"error,omitempty"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
	UpdatedAt  time.Time          `json:"updated_at"`
}

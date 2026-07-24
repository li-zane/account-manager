package ports

import (
	"context"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

type ListOptions struct {
	Limit  int
	Offset int
}

type ProviderConnectionFilter struct {
	Provider domain.ProviderKey
	Enabled  *bool
}

type ProviderConnectionRepository interface {
	CreateProviderConnection(ctx context.Context, connection domain.ProviderConnection) error
	GetProviderConnection(ctx context.Context, id string) (domain.ProviderConnection, error)
	GetProviderConnectionByProviderAndName(ctx context.Context, provider domain.ProviderKey, name string) (domain.ProviderConnection, error)
	ListProviderConnections(ctx context.Context, filter ProviderConnectionFilter, options ListOptions) ([]domain.ProviderConnection, error)
	UpdateProviderConnection(ctx context.Context, connection domain.ProviderConnection, expectedVersion int64) error
}

type AppSettingRepository interface {
	GetAppSetting(ctx context.Context, key string) (domain.AppSetting, error)
	SaveAppSetting(ctx context.Context, setting domain.AppSetting, expectedVersion int64) error
}

func (o ListOptions) Normalize(defaultLimit, maxLimit int) ListOptions {
	if o.Limit <= 0 {
		o.Limit = defaultLimit
	}
	if o.Limit > maxLimit {
		o.Limit = maxLimit
	}
	if o.Offset < 0 {
		o.Offset = 0
	}
	return o
}

type MailboxRepository interface {
	CreateMailbox(ctx context.Context, mailbox domain.Mailbox) error
	GetMailbox(ctx context.Context, id string) (domain.Mailbox, error)
	GetMailboxByIdentity(ctx context.Context, provider domain.ProviderKey, normalizedAddress string) (domain.Mailbox, error)
	ListMailboxes(ctx context.Context, options ListOptions) ([]domain.Mailbox, error)
	CountMailboxes(ctx context.Context) (int64, error)
	CreateAlias(ctx context.Context, alias domain.MailboxAlias) error
	GetAlias(ctx context.Context, id string) (domain.MailboxAlias, error)
	ListAliases(ctx context.Context, mailboxID string, options ListOptions) ([]domain.MailboxAlias, error)
	CountAliases(ctx context.Context, mailboxID string) (int64, error)
	UpsertCredential(ctx context.Context, credential domain.MailboxCredential) error
	GetCredential(ctx context.Context, mailboxID string, kind domain.CredentialKind) (domain.MailboxCredential, error)
	ListCredentials(ctx context.Context, mailboxID string) ([]domain.MailboxCredential, error)
	ImportMailboxes(ctx context.Context, items []domain.MailboxImportItem, strategy domain.ConflictStrategy) (domain.MailboxImportResult, error)
}

type MailboxFormatRepository interface {
	CreateMailboxFormat(ctx context.Context, format domain.MailboxFormat) error
	GetMailboxFormat(ctx context.Context, id string) (domain.MailboxFormat, error)
	ListMailboxFormats(ctx context.Context, options ListOptions) ([]domain.MailboxFormat, error)
	UpdateMailboxFormat(ctx context.Context, format domain.MailboxFormat, expectedVersion int64) error
}

type PickupKeyRepository interface {
	CreatePickupKey(ctx context.Context, key domain.MailboxPickupKey) error
	FindPickupKeyByDigest(ctx context.Context, digest []byte) (domain.MailboxPickupKey, error)
	RevokePickupKey(ctx context.Context, mailboxID, keyID string) error
	ListPickupKeys(ctx context.Context, mailboxID string, options ListOptions) ([]domain.MailboxPickupKey, error)
}

type PlatformAccountRepository interface {
	CreatePlatformAccount(ctx context.Context, account domain.PlatformAccount) error
	GetPlatformAccount(ctx context.Context, id string) (domain.PlatformAccount, error)
	ListPlatformAccounts(ctx context.Context, platform string, options ListOptions) ([]domain.PlatformAccount, error)
	ListPlatformAccountsByMailbox(ctx context.Context, mailboxID string, options ListOptions) ([]domain.PlatformAccount, error)
	UpsertPlatformAccountCredential(ctx context.Context, credential domain.PlatformAccountCredential) error
	GetPlatformAccountCredential(ctx context.Context, platformAccountID, kind string) (domain.PlatformAccountCredential, error)
}

type BackupRepository interface {
	CreateBackupTarget(ctx context.Context, target domain.BackupTarget) error
	GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error)
	ListBackupTargets(ctx context.Context, options ListOptions) ([]domain.BackupTarget, error)
	UpdateBackupTarget(ctx context.Context, target domain.BackupTarget, expectedVersion int64) error
	CreateBackupRun(ctx context.Context, run domain.BackupRun) error
	GetBackupRun(ctx context.Context, id string) (domain.BackupRun, error)
	ListBackupRuns(ctx context.Context, targetID string, options ListOptions) ([]domain.BackupRun, error)
	UpdateBackupRun(ctx context.Context, run domain.BackupRun) error
}

// BackupOperationLocker serializes database snapshots and restores across all
// server instances sharing a repository. The release function is idempotent.
type BackupOperationLocker interface {
	TryAcquireBackupOperation(ctx context.Context) (release func(), acquired bool, err error)
}

// BackupRunClaimer atomically moves the oldest pending run to running. It is
// separate from BackupRepository so the encryption/upload core remains easy
// to exercise with small repository fakes.
type BackupRunClaimer interface {
	ClaimPendingBackupRun(ctx context.Context, startedAt time.Time) (domain.BackupRun, error)
}

// BackupSchedulerRepository serializes schedule-window inserts. The boolean
// is false when another scheduler already created a run for the same window.
type BackupSchedulerRepository interface {
	CreateScheduledBackupRun(ctx context.Context, run domain.BackupRun, dueAt time.Time) (bool, error)
}

type HealthRepository interface {
	Ping(ctx context.Context) error
}

// Store is a convenient composition used by the HTTP server. Individual
// services still depend on the narrow interfaces above.
type Store interface {
	ProviderConnectionRepository
	AppSettingRepository
	MailboxRepository
	MailboxFormatRepository
	PickupKeyRepository
	PlatformAccountRepository
	BackupRepository
	BackupRunClaimer
	BackupSchedulerRepository
	BackupOperationLocker
	HealthRepository
}

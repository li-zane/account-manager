package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/migrations"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	store := New(pool)
	if err := store.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return store, nil
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error { return migrations.Apply(ctx, s.pool) }

func (s *Store) CreateMailbox(ctx context.Context, mailbox domain.Mailbox) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mailboxes
			(id, provider, address, normalized_address, display_name, external_reference, status, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		mailbox.ID, mailbox.Provider, mailbox.Address, mailbox.NormalizedAddress,
		mailbox.DisplayName, mailbox.ExternalReference, mailbox.Status, validJSON(mailbox.Metadata),
		mailbox.CreatedAt, mailbox.UpdatedAt,
	)
	return mapError(err)
}

func (s *Store) GetMailbox(ctx context.Context, id string) (domain.Mailbox, error) {
	return scanMailbox(s.pool.QueryRow(ctx, `
		SELECT id, provider, address, normalized_address, display_name, external_reference, status, metadata, created_at, updated_at
		FROM mailboxes WHERE id=$1`, id))
}

func (s *Store) GetMailboxByIdentity(ctx context.Context, provider domain.ProviderKey, normalizedAddress string) (domain.Mailbox, error) {
	return scanMailbox(s.pool.QueryRow(ctx, `
		SELECT id, provider, address, normalized_address, display_name, external_reference, status, metadata, created_at, updated_at
		FROM mailboxes WHERE provider=$1 AND normalized_address=$2`, provider, normalizedAddress))
}

func (s *Store) ListMailboxes(ctx context.Context, options ports.ListOptions) ([]domain.Mailbox, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT id, provider, address, normalized_address, display_name, external_reference, status, metadata, created_at, updated_at
		FROM mailboxes ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.Mailbox, 0)
	for rows.Next() {
		item, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) CountMailboxes(ctx context.Context) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mailboxes`).Scan(&count)
	return count, mapError(err)
}

func (s *Store) CreateAlias(ctx context.Context, alias domain.MailboxAlias) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mailbox_aliases
			(id, mailbox_id, provider, address, normalized_address, kind, enabled, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		alias.ID, alias.MailboxID, alias.Provider, alias.Address, alias.NormalizedAddress,
		alias.Kind, alias.Enabled, validJSON(alias.Metadata), alias.CreatedAt, alias.UpdatedAt,
	)
	return mapError(err)
}

func (s *Store) GetAlias(ctx context.Context, id string) (domain.MailboxAlias, error) {
	return scanAlias(s.pool.QueryRow(ctx, `
		SELECT id, mailbox_id, provider, address, normalized_address, kind, enabled, metadata, created_at, updated_at
		FROM mailbox_aliases WHERE id=$1`, id))
}

func (s *Store) ListAliases(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.MailboxAlias, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT id, mailbox_id, provider, address, normalized_address, kind, enabled, metadata, created_at, updated_at
		FROM mailbox_aliases WHERE mailbox_id=$1 ORDER BY created_at, id LIMIT $2 OFFSET $3`,
		mailboxID, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.MailboxAlias, 0)
	for rows.Next() {
		item, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) CountAliases(ctx context.Context, mailboxID string) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mailbox_aliases WHERE ($1='' OR mailbox_id=$1)`, mailboxID).Scan(&count)
	return count, mapError(err)
}

func (s *Store) UpsertCredential(ctx context.Context, credential domain.MailboxCredential) error {
	result, err := s.pool.Exec(ctx, `
		INSERT INTO mailbox_credentials
			(id, mailbox_id, kind, client_id, encrypted_secret, key_version, expires_at, refresh_after,
			 refresh_status, last_refreshed_at, last_refresh_error, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (mailbox_id, kind) DO UPDATE SET
			client_id=EXCLUDED.client_id,
			encrypted_secret=EXCLUDED.encrypted_secret,
			key_version=EXCLUDED.key_version,
			expires_at=EXCLUDED.expires_at,
			refresh_after=EXCLUDED.refresh_after,
			refresh_status=EXCLUDED.refresh_status,
			last_refreshed_at=EXCLUDED.last_refreshed_at,
			last_refresh_error=EXCLUDED.last_refresh_error,
			metadata=EXCLUDED.metadata,
			version=mailbox_credentials.version+1,
			updated_at=EXCLUDED.updated_at
		WHERE mailbox_credentials.version=$15`,
		credential.ID, credential.MailboxID, credential.Kind, credential.ClientID, credential.EncryptedSecret,
		credential.KeyVersion, credential.ExpiresAt, credential.RefreshAfter, credential.RefreshStatus,
		credential.LastRefreshedAt, credential.LastRefreshError, validJSON(credential.Metadata),
		credential.CreatedAt, credential.UpdatedAt, credential.Version,
	)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("%w: credential version changed", domain.ErrConflict)
	}
	return nil
}

func (s *Store) GetCredential(ctx context.Context, mailboxID string, kind domain.CredentialKind) (domain.MailboxCredential, error) {
	return scanCredential(s.pool.QueryRow(ctx, `
		SELECT id, mailbox_id, kind, client_id, encrypted_secret, key_version, expires_at, refresh_after,
			refresh_status, last_refreshed_at, last_refresh_error, metadata, version, created_at, updated_at
		FROM mailbox_credentials WHERE mailbox_id=$1 AND kind=$2`, mailboxID, kind))
}

func (s *Store) ListCredentials(ctx context.Context, mailboxID string) ([]domain.MailboxCredential, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, mailbox_id, kind, client_id, encrypted_secret, key_version, expires_at, refresh_after,
			refresh_status, last_refreshed_at, last_refresh_error, metadata, version, created_at, updated_at
		FROM mailbox_credentials WHERE mailbox_id=$1 ORDER BY kind`, mailboxID)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.MailboxCredential, 0)
	for rows.Next() {
		item, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) CreatePickupKey(ctx context.Context, key domain.MailboxPickupKey) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mailbox_pickup_keys
			(id, mailbox_id, digest, encrypted_token, key_version, prefix, label, expires_at, revoked_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		key.ID, key.MailboxID, key.Digest, nullableBytes(key.EncryptedToken), nullableText(key.KeyVersion),
		key.Prefix, key.Label, key.ExpiresAt, key.RevokedAt, key.CreatedAt)
	return mapError(err)
}

func (s *Store) FindPickupKeyByDigest(ctx context.Context, digest []byte) (domain.MailboxPickupKey, error) {
	return scanPickupKey(s.pool.QueryRow(ctx, `
		SELECT id, mailbox_id, digest, COALESCE(encrypted_token, ''::bytea), COALESCE(key_version, ''), prefix, label, expires_at, revoked_at, created_at
		FROM mailbox_pickup_keys WHERE digest=$1`, digest))
}

func (s *Store) RevokePickupKey(ctx context.Context, mailboxID, keyID string) error {
	command, err := s.pool.Exec(ctx, `
		UPDATE mailbox_pickup_keys SET revoked_at=COALESCE(revoked_at, now())
		WHERE id=$1 AND mailbox_id=$2`, keyID, mailboxID)
	if err != nil {
		return mapError(err)
	}
	if command.RowsAffected() == 0 {
		return fmt.Errorf("%w: pickup key %q", domain.ErrNotFound, keyID)
	}
	return nil
}

func (s *Store) ListPickupKeys(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.MailboxPickupKey, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT id, mailbox_id, digest, COALESCE(encrypted_token, ''::bytea), COALESCE(key_version, ''), prefix, label, expires_at, revoked_at, created_at
		FROM mailbox_pickup_keys WHERE mailbox_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		mailboxID, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.MailboxPickupKey, 0)
	for rows.Next() {
		item, err := scanPickupKey(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) CreatePlatformAccount(ctx context.Context, account domain.PlatformAccount) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO platform_accounts
			(id, platform, external_reference, mailbox_id, mailbox_alias_id, login_address, status, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		account.ID, account.Platform, account.ExternalReference, nullableText(account.MailboxID),
		account.MailboxAliasID, account.LoginAddress, account.Status,
		validJSON(account.Metadata), account.CreatedAt, account.UpdatedAt)
	if err != nil {
		return mapError(err)
	}
	if account.MailboxID == "" && account.MailboxAliasID == nil {
		return mapError(tx.Commit(ctx))
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO platform_account_mailbox_routes
			(id, platform_account_id, mailbox_id, mailbox_alias_id, route_role, address, metadata, created_at)
		VALUES ($1,$2,$3,$4,'login',$5,$6,$7)`,
		"route_"+account.ID, account.ID, nullableText(account.MailboxID), account.MailboxAliasID,
		account.LoginAddress, validJSON(account.Metadata), account.CreatedAt)
	if err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}

func (s *Store) GetPlatformAccount(ctx context.Context, id string) (domain.PlatformAccount, error) {
	return scanAccount(s.pool.QueryRow(ctx, `
		SELECT a.id, a.platform, a.external_reference,
			COALESCE(a.mailbox_id, route.mailbox_id),
			COALESCE(a.mailbox_alias_id, route.mailbox_alias_id),
			a.login_address, a.status, a.metadata, a.created_at, a.updated_at
		FROM platform_accounts a
		LEFT JOIN LATERAL (
			SELECT mailbox_id, mailbox_alias_id
			FROM platform_account_mailbox_routes
			WHERE platform_account_id=a.id AND route_role='login'
			ORDER BY created_at LIMIT 1
		) route ON TRUE
		WHERE a.id=$1`, id))
}

func (s *Store) ListPlatformAccounts(ctx context.Context, platform string, options ports.ListOptions) ([]domain.PlatformAccount, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.platform, a.external_reference,
			COALESCE(a.mailbox_id, route.mailbox_id),
			COALESCE(a.mailbox_alias_id, route.mailbox_alias_id),
			a.login_address, a.status, a.metadata, a.created_at, a.updated_at
		FROM platform_accounts a
		LEFT JOIN LATERAL (
			SELECT mailbox_id, mailbox_alias_id
			FROM platform_account_mailbox_routes
			WHERE platform_account_id=a.id AND route_role='login'
			ORDER BY created_at LIMIT 1
		) route ON TRUE
		WHERE ($1='' OR a.platform=$1)
		ORDER BY a.created_at DESC, a.id LIMIT $2 OFFSET $3`, platform, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.PlatformAccount, 0)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) ListPlatformAccountsByMailbox(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.PlatformAccount, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT a.id, a.platform, a.external_reference,
			COALESCE(a.mailbox_id, route.mailbox_id),
			COALESCE(a.mailbox_alias_id, route.mailbox_alias_id),
			a.login_address, a.status, a.metadata, a.created_at, a.updated_at
		FROM platform_accounts a
		LEFT JOIN LATERAL (
			SELECT mailbox_id, mailbox_alias_id
			FROM platform_account_mailbox_routes
			WHERE platform_account_id=a.id AND route_role='login'
			ORDER BY created_at LIMIT 1
		) route ON TRUE
		WHERE a.mailbox_id=$1 OR EXISTS (
			SELECT 1 FROM platform_account_mailbox_routes r
			WHERE r.platform_account_id=a.id AND r.mailbox_id=$1
		)
		ORDER BY a.created_at DESC, a.id LIMIT $2 OFFSET $3`, mailboxID, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.PlatformAccount, 0)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) CreateBackupTarget(ctx context.Context, target domain.BackupTarget) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO backup_targets
			(id, name, kind, encrypted_config, key_version, enabled, schedule, retention_count, metadata, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10,$11)`,
		target.ID, target.Name, target.Kind, target.EncryptedConfig, target.KeyVersion,
		target.Enabled, target.Schedule, target.RetentionCount, validJSON(target.Metadata),
		target.CreatedAt, target.UpdatedAt)
	return mapError(err)
}

func (s *Store) GetBackupTarget(ctx context.Context, id string) (domain.BackupTarget, error) {
	return scanBackupTarget(s.pool.QueryRow(ctx, `
		SELECT id, name, kind, encrypted_config, key_version, enabled, schedule, retention_count, metadata, version, created_at, updated_at
		FROM backup_targets WHERE id=$1`, id))
}

func (s *Store) ListBackupTargets(ctx context.Context, options ports.ListOptions) ([]domain.BackupTarget, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, kind, encrypted_config, key_version, enabled, schedule, retention_count, metadata, version, created_at, updated_at
		FROM backup_targets ORDER BY name, id LIMIT $1 OFFSET $2`, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.BackupTarget, 0)
	for rows.Next() {
		item, err := scanBackupTarget(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) UpdateBackupTarget(ctx context.Context, target domain.BackupTarget, expectedVersion int64) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE backup_targets SET name=$2, kind=$3, encrypted_config=$4, key_version=$5,
			enabled=$6, schedule=$7, retention_count=$8, metadata=$9,
			version=version+1, updated_at=$10
		WHERE id=$1 AND version=$11`,
		target.ID, target.Name, target.Kind, target.EncryptedConfig, target.KeyVersion,
		target.Enabled, target.Schedule, target.RetentionCount, validJSON(target.Metadata),
		target.UpdatedAt, expectedVersion)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 0 {
		return nil
	}
	var currentVersion int64
	if err := s.pool.QueryRow(ctx, `SELECT version FROM backup_targets WHERE id=$1`, target.ID).Scan(&currentVersion); err != nil {
		return mapError(err)
	}
	return fmt.Errorf("%w: backup target version changed", domain.ErrConflict)
}

func (s *Store) CreateBackupRun(ctx context.Context, run domain.BackupRun) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	var targetID string
	if err := tx.QueryRow(ctx, `SELECT id FROM backup_targets WHERE id=$1 FOR UPDATE`, run.TargetID).Scan(&targetID); err != nil {
		return mapError(err)
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM backup_runs WHERE target_id=$1 AND state IN ($2,$3))`,
		run.TargetID, domain.BackupRunPending, domain.BackupRunRunning).Scan(&active); err != nil {
		return mapError(err)
	}
	if active {
		return fmt.Errorf("%w: backup target %q already has an active run", domain.ErrConflict, run.TargetID)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO backup_runs
			(id, target_id, state, object_key, checksum, size_bytes, error, started_at, finished_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		run.ID, run.TargetID, run.State, run.ObjectKey, run.Checksum, run.SizeBytes,
		run.Error, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}

func (s *Store) CreateScheduledBackupRun(ctx context.Context, run domain.BackupRun, dueAt time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, mapError(err)
	}
	defer tx.Rollback(ctx)

	var targetID string
	if err := tx.QueryRow(ctx, `SELECT id FROM backup_targets WHERE id=$1 FOR UPDATE`, run.TargetID).Scan(&targetID); err != nil {
		return false, mapError(err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM backup_runs
			WHERE target_id=$1 AND (created_at >= $2 OR state IN ($3,$4))
		)`, run.TargetID, dueAt, domain.BackupRunPending, domain.BackupRunRunning).Scan(&exists); err != nil {
		return false, mapError(err)
	}
	if exists {
		if err := tx.Commit(ctx); err != nil {
			return false, mapError(err)
		}
		return false, nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO backup_runs
			(id, target_id, state, object_key, checksum, size_bytes, error, started_at, finished_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		run.ID, run.TargetID, run.State, run.ObjectKey, run.Checksum, run.SizeBytes,
		run.Error, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return false, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, mapError(err)
	}
	return true, nil
}

func (s *Store) ClaimPendingBackupRun(ctx context.Context, startedAt time.Time) (domain.BackupRun, error) {
	return scanBackupRun(s.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM backup_runs
			WHERE state=$1
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE backup_runs AS run
		SET state=$2, started_at=COALESCE(run.started_at, $3), updated_at=$3
		FROM candidate
		WHERE run.id=candidate.id
		RETURNING run.id, run.target_id, run.state, run.object_key, run.checksum,
			run.size_bytes, run.error, run.started_at, run.finished_at, run.created_at, run.updated_at`,
		domain.BackupRunPending, domain.BackupRunRunning, startedAt.UTC()))
}

func (s *Store) GetBackupRun(ctx context.Context, id string) (domain.BackupRun, error) {
	return scanBackupRun(s.pool.QueryRow(ctx, `
		SELECT id, target_id, state, object_key, checksum, size_bytes, error, started_at, finished_at, created_at, updated_at
		FROM backup_runs WHERE id=$1`, id))
}

func (s *Store) ListBackupRuns(ctx context.Context, targetID string, options ports.ListOptions) ([]domain.BackupRun, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT id, target_id, state, object_key, checksum, size_bytes, error, started_at, finished_at, created_at, updated_at
		FROM backup_runs WHERE ($1='' OR target_id=$1)
		ORDER BY created_at DESC LIMIT $2 OFFSET $3`, targetID, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.BackupRun, 0)
	for rows.Next() {
		item, err := scanBackupRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) UpdateBackupRun(ctx context.Context, run domain.BackupRun) error {
	command, err := s.pool.Exec(ctx, `
		UPDATE backup_runs SET state=$2, object_key=$3, checksum=$4, size_bytes=$5,
			error=$6, started_at=$7, finished_at=$8, updated_at=$9
		WHERE id=$1 AND target_id=$10`, run.ID, run.State, run.ObjectKey, run.Checksum,
		run.SizeBytes, run.Error, run.StartedAt, run.FinishedAt, run.UpdatedAt, run.TargetID)
	if err != nil {
		return mapError(err)
	}
	if command.RowsAffected() == 0 {
		return fmt.Errorf("%w: backup run %q", domain.ErrNotFound, run.ID)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMailbox(row scanner) (domain.Mailbox, error) {
	var item domain.Mailbox
	var provider, status string
	err := row.Scan(&item.ID, &provider, &item.Address, &item.NormalizedAddress, &item.DisplayName,
		&item.ExternalReference, &status, &item.Metadata, &item.CreatedAt, &item.UpdatedAt)
	item.Provider, item.Status = domain.ProviderKey(provider), domain.MailboxStatus(status)
	return item, mapError(err)
}

func scanAlias(row scanner) (domain.MailboxAlias, error) {
	var item domain.MailboxAlias
	var provider, kind string
	err := row.Scan(&item.ID, &item.MailboxID, &provider, &item.Address, &item.NormalizedAddress,
		&kind, &item.Enabled, &item.Metadata, &item.CreatedAt, &item.UpdatedAt)
	item.Provider, item.Kind = domain.ProviderKey(provider), domain.AliasKind(kind)
	return item, mapError(err)
}

func scanCredential(row scanner) (domain.MailboxCredential, error) {
	var item domain.MailboxCredential
	var kind string
	err := row.Scan(&item.ID, &item.MailboxID, &kind, &item.ClientID, &item.EncryptedSecret, &item.KeyVersion,
		&item.ExpiresAt, &item.RefreshAfter, &item.RefreshStatus, &item.LastRefreshedAt,
		&item.LastRefreshError, &item.Metadata, &item.Version, &item.CreatedAt, &item.UpdatedAt)
	item.Kind = domain.CredentialKind(kind)
	return item, mapError(err)
}

func scanPickupKey(row scanner) (domain.MailboxPickupKey, error) {
	var item domain.MailboxPickupKey
	err := row.Scan(&item.ID, &item.MailboxID, &item.Digest, &item.EncryptedToken, &item.KeyVersion, &item.Prefix, &item.Label,
		&item.ExpiresAt, &item.RevokedAt, &item.CreatedAt)
	return item, mapError(err)
}

func scanAccount(row scanner) (domain.PlatformAccount, error) {
	var item domain.PlatformAccount
	var mailboxID *string
	err := row.Scan(&item.ID, &item.Platform, &item.ExternalReference, &mailboxID, &item.MailboxAliasID,
		&item.LoginAddress, &item.Status, &item.Metadata, &item.CreatedAt, &item.UpdatedAt)
	if mailboxID != nil {
		item.MailboxID = *mailboxID
	}
	return item, mapError(err)
}

func scanBackupTarget(row scanner) (domain.BackupTarget, error) {
	var item domain.BackupTarget
	var kind string
	err := row.Scan(&item.ID, &item.Name, &kind, &item.EncryptedConfig, &item.KeyVersion,
		&item.Enabled, &item.Schedule, &item.RetentionCount, &item.Metadata, &item.Version, &item.CreatedAt, &item.UpdatedAt)
	item.Kind = domain.BackupTargetKind(kind)
	return item, mapError(err)
}

func scanBackupRun(row scanner) (domain.BackupRun, error) {
	var item domain.BackupRun
	var state string
	err := row.Scan(&item.ID, &item.TargetID, &state, &item.ObjectKey, &item.Checksum,
		&item.SizeBytes, &item.Error, &item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.UpdatedAt)
	item.State = domain.BackupRunState(state)
	return item, mapError(err)
}

func validJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage(`{}`)
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: database row", domain.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %s", domain.ErrConflict, pgErr.ConstraintName)
		case "23503":
			return fmt.Errorf("%w: related record does not exist", domain.ErrInvalid)
		case "23514", "23502", "22P02":
			return fmt.Errorf("%w: database value rejected (code %s, column %s, constraint %s)", domain.ErrInvalid, pgErr.Code, pgErr.ColumnName, pgErr.ConstraintName)
		}
	}
	return err
}

var _ ports.Store = (*Store)(nil)

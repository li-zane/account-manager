package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

func (s *Store) ImportMailboxes(ctx context.Context, items []domain.MailboxImportItem, strategy domain.ConflictStrategy) (domain.MailboxImportResult, error) {
	if strategy != domain.ConflictSkip && strategy != domain.ConflictUpdate && strategy != domain.ConflictError {
		return domain.MailboxImportResult{}, fmt.Errorf("%w: conflict strategy %q", domain.ErrInvalid, strategy)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.MailboxImportResult{}, mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result := domain.MailboxImportResult{MailboxIDs: make([]string, 0, len(items))}
	for _, item := range items {
		existing, lookupErr := scanMailbox(tx.QueryRow(ctx, `
			SELECT id, provider, address, normalized_address, display_name, external_reference, status, metadata, created_at, updated_at
			FROM mailboxes WHERE provider=$1 AND normalized_address=$2 FOR UPDATE`,
			item.Mailbox.Provider, item.Mailbox.NormalizedAddress))
		switch {
		case lookupErr == nil:
			switch strategy {
			case domain.ConflictSkip:
				result.Skipped++
				continue
			case domain.ConflictError:
				return domain.MailboxImportResult{}, fmt.Errorf("%w: mailbox %s/%s", domain.ErrConflict, item.Mailbox.Provider, item.Mailbox.NormalizedAddress)
			case domain.ConflictUpdate:
				_, err = tx.Exec(ctx, `
					UPDATE mailboxes SET address=$2, display_name=$3, external_reference=$4,
						status=$5, metadata=$6, version=version+1, updated_at=$7
					WHERE id=$1`, existing.ID, item.Mailbox.Address, item.Mailbox.DisplayName,
					item.Mailbox.ExternalReference, item.Mailbox.Status, validJSON(item.Mailbox.Metadata), item.Mailbox.UpdatedAt)
				if err != nil {
					return domain.MailboxImportResult{}, mapError(err)
				}
				if item.Credential != nil {
					credential := *item.Credential
					credential.MailboxID = existing.ID
					if err := upsertImportedCredential(ctx, tx, credential); err != nil {
						return domain.MailboxImportResult{}, err
					}
				}
				if err := upsertImportedPlatform(ctx, tx, item, existing.ID); err != nil {
					return domain.MailboxImportResult{}, err
				}
				if err := insertImportedPickupKey(ctx, tx, item, existing.ID); err != nil {
					return domain.MailboxImportResult{}, err
				}
				result.Updated++
				result.MailboxIDs = append(result.MailboxIDs, existing.ID)
			}
		case errors.Is(lookupErr, domain.ErrNotFound):
			mailbox := item.Mailbox
			_, err = tx.Exec(ctx, `
				INSERT INTO mailboxes
					(id, provider, address, normalized_address, display_name, external_reference, status, metadata, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
				mailbox.ID, mailbox.Provider, mailbox.Address, mailbox.NormalizedAddress,
				mailbox.DisplayName, mailbox.ExternalReference, mailbox.Status, validJSON(mailbox.Metadata),
				mailbox.CreatedAt, mailbox.UpdatedAt)
			if err != nil {
				return domain.MailboxImportResult{}, mapError(err)
			}
			if item.Credential != nil {
				credential := *item.Credential
				credential.MailboxID = mailbox.ID
				if err := upsertImportedCredential(ctx, tx, credential); err != nil {
					return domain.MailboxImportResult{}, err
				}
			}
			if err := upsertImportedPlatform(ctx, tx, item, mailbox.ID); err != nil {
				return domain.MailboxImportResult{}, err
			}
			if err := insertImportedPickupKey(ctx, tx, item, mailbox.ID); err != nil {
				return domain.MailboxImportResult{}, err
			}
			result.Created++
			result.MailboxIDs = append(result.MailboxIDs, mailbox.ID)
		default:
			return domain.MailboxImportResult{}, lookupErr
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.MailboxImportResult{}, mapError(err)
	}
	return result, nil
}

func insertImportedPickupKey(ctx context.Context, tx pgx.Tx, item domain.MailboxImportItem, mailboxID string) error {
	if item.PickupKey == nil {
		return nil
	}
	key := *item.PickupKey
	key.MailboxID = mailboxID
	var existingMailboxID string
	err := tx.QueryRow(ctx, `SELECT mailbox_id FROM mailbox_pickup_keys WHERE digest=$1 FOR UPDATE`, key.Digest).Scan(&existingMailboxID)
	switch {
	case err == nil && existingMailboxID == mailboxID:
		return nil
	case err == nil:
		return fmt.Errorf("%w: imported pickup key belongs to another mailbox", domain.ErrConflict)
	case !errors.Is(err, pgx.ErrNoRows):
		return mapError(err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO mailbox_pickup_keys
			(id, mailbox_id, digest, prefix, label, expires_at, revoked_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		key.ID, key.MailboxID, key.Digest, key.Prefix, key.Label, key.ExpiresAt, key.RevokedAt, key.CreatedAt)
	return mapError(err)
}

func upsertImportedPlatform(ctx context.Context, tx pgx.Tx, item domain.MailboxImportItem, mailboxID string) error {
	if item.PlatformAccount == nil {
		return nil
	}
	account := *item.PlatformAccount
	account.MailboxID = mailboxID
	_, err := tx.Exec(ctx, `
		INSERT INTO platform_accounts
			(id, platform, external_reference, mailbox_id, mailbox_alias_id, login_address, status, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE SET
			mailbox_id=EXCLUDED.mailbox_id,
			mailbox_alias_id=EXCLUDED.mailbox_alias_id,
			login_address=EXCLUDED.login_address,
			status=EXCLUDED.status,
			metadata=EXCLUDED.metadata,
			version=platform_accounts.version+1,
			updated_at=EXCLUDED.updated_at`,
		account.ID, account.Platform, account.ExternalReference, account.MailboxID,
		account.MailboxAliasID, account.LoginAddress, account.Status, validJSON(account.Metadata),
		account.CreatedAt, account.UpdatedAt)
	if err != nil {
		return mapError(err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO platform_account_mailbox_routes
			(id, platform_account_id, mailbox_id, mailbox_alias_id, route_role, address, metadata, created_at)
		VALUES ($1,$2,$3,$4,'login',$5,$6,$7)
		ON CONFLICT (id) DO UPDATE SET mailbox_id=EXCLUDED.mailbox_id,
			mailbox_alias_id=EXCLUDED.mailbox_alias_id, address=EXCLUDED.address, metadata=EXCLUDED.metadata`,
		"route_"+account.ID, account.ID, account.MailboxID, account.MailboxAliasID,
		account.LoginAddress, validJSON(account.Metadata), account.CreatedAt)
	if err != nil {
		return mapError(err)
	}
	if item.PlatformCredential == nil {
		return nil
	}
	credential := *item.PlatformCredential
	credential.PlatformAccountID = account.ID
	_, err = tx.Exec(ctx, `
		INSERT INTO platform_account_credentials
			(id, platform_account_id, kind, encrypted_secret, key_version, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (platform_account_id, kind) DO UPDATE SET
			encrypted_secret=EXCLUDED.encrypted_secret,
			key_version=EXCLUDED.key_version,
			metadata=EXCLUDED.metadata,
			updated_at=EXCLUDED.updated_at`,
		credential.ID, credential.PlatformAccountID, credential.Kind, credential.EncryptedSecret,
		credential.KeyVersion, validJSON(credential.Metadata), credential.CreatedAt, credential.UpdatedAt)
	return mapError(err)
}

func upsertImportedCredential(ctx context.Context, tx pgx.Tx, credential domain.MailboxCredential) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO mailbox_credentials
			(id, mailbox_id, kind, client_id, encrypted_secret, key_version, expires_at, refresh_after,
			 refresh_status, last_refreshed_at, last_refresh_error, metadata, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,1,$13,$14)
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
			updated_at=EXCLUDED.updated_at`,
		credential.ID, credential.MailboxID, credential.Kind, credential.ClientID,
		credential.EncryptedSecret, credential.KeyVersion, credential.ExpiresAt, credential.RefreshAfter,
		credential.RefreshStatus, credential.LastRefreshedAt, credential.LastRefreshError,
		validJSON(credential.Metadata), credential.CreatedAt, credential.UpdatedAt)
	return mapError(err)
}

func (s *Store) CreateMailboxFormat(ctx context.Context, format domain.MailboxFormat) error {
	fields, err := json.Marshal(format.Fields)
	if err != nil {
		return fmt.Errorf("marshal mailbox format fields: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO mailbox_formats
			(id, name, kind, direction, delimiter, fields, provider, enabled, has_header,
			 template, parser_config, builtin, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,1,$13,$14)`,
		format.ID, format.Name, format.Kind, format.Direction, format.Delimiter, fields,
		nullableProvider(format.Provider), format.Enabled, format.HasHeader, format.Template,
		validJSON(format.ParserConfig), format.Builtin, format.CreatedAt, format.UpdatedAt)
	return mapError(err)
}

func (s *Store) GetMailboxFormat(ctx context.Context, id string) (domain.MailboxFormat, error) {
	return scanMailboxFormat(s.pool.QueryRow(ctx, `
		SELECT id, name, kind, direction, delimiter, fields, provider, has_header, template,
			parser_config, builtin, enabled, version, created_at, updated_at
		FROM mailbox_formats WHERE id=$1`, id))
}

func (s *Store) ListMailboxFormats(ctx context.Context, options ports.ListOptions) ([]domain.MailboxFormat, error) {
	options = options.Normalize(100, 500)
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, kind, direction, delimiter, fields, provider, has_header, template,
			parser_config, builtin, enabled, version, created_at, updated_at
		FROM mailbox_formats ORDER BY name, id LIMIT $1 OFFSET $2`, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.MailboxFormat, 0)
	for rows.Next() {
		item, err := scanMailboxFormat(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) UpdateMailboxFormat(ctx context.Context, format domain.MailboxFormat, expectedVersion int64) error {
	fields, err := json.Marshal(format.Fields)
	if err != nil {
		return fmt.Errorf("marshal mailbox format fields: %w", err)
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE mailbox_formats SET name=$2, kind=$3, direction=$4, delimiter=$5, fields=$6,
			provider=$7, has_header=$8, template=$9, parser_config=$10, builtin=$11,
			enabled=$12, version=version+1, updated_at=$13
		WHERE id=$1 AND version=$14`, format.ID, format.Name, format.Kind, format.Direction,
		format.Delimiter, fields, nullableProvider(format.Provider), format.HasHeader,
		format.Template, validJSON(format.ParserConfig), format.Builtin, format.Enabled,
		format.UpdatedAt, expectedVersion)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 0 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mailbox_formats WHERE id=$1)`, format.ID).Scan(&exists); err != nil {
		return mapError(err)
	}
	if !exists {
		return fmt.Errorf("%w: mailbox format %q", domain.ErrNotFound, format.ID)
	}
	return fmt.Errorf("%w: mailbox format version changed", domain.ErrConflict)
}

func scanMailboxFormat(row scanner) (domain.MailboxFormat, error) {
	var item domain.MailboxFormat
	var kind, direction string
	var fields []byte
	var provider *string
	err := row.Scan(&item.ID, &item.Name, &kind, &direction, &item.Delimiter, &fields, &provider,
		&item.HasHeader, &item.Template, &item.ParserConfig, &item.Builtin, &item.Enabled,
		&item.Version, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return domain.MailboxFormat{}, mapError(err)
	}
	if err := json.Unmarshal(fields, &item.Fields); err != nil {
		return domain.MailboxFormat{}, fmt.Errorf("decode mailbox format fields: %w", err)
	}
	item.Direction = domain.MailboxFormatDirection(direction)
	item.Kind = domain.MailboxFormatKind(kind)
	if provider != nil {
		value := domain.ProviderKey(*provider)
		item.Provider = &value
	}
	return item, nil
}

func nullableProvider(provider *domain.ProviderKey) any {
	if provider == nil || *provider == "" {
		return nil
	}
	return string(*provider)
}

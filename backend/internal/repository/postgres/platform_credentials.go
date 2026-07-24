package postgres

import (
	"context"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func (s *Store) UpsertPlatformAccountCredential(ctx context.Context, credential domain.PlatformAccountCredential) error {
	_, err := s.pool.Exec(ctx, `
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

func (s *Store) GetPlatformAccountCredential(ctx context.Context, platformAccountID, kind string) (domain.PlatformAccountCredential, error) {
	var credential domain.PlatformAccountCredential
	err := s.pool.QueryRow(ctx, `
		SELECT id, platform_account_id, kind, encrypted_secret, key_version, metadata, created_at, updated_at
		FROM platform_account_credentials WHERE platform_account_id=$1 AND kind=$2`, platformAccountID, kind).Scan(
		&credential.ID, &credential.PlatformAccountID, &credential.Kind, &credential.EncryptedSecret,
		&credential.KeyVersion, &credential.Metadata, &credential.CreatedAt, &credential.UpdatedAt)
	return credential, mapError(err)
}

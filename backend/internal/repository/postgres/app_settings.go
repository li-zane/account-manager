package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func (s *Store) GetAppSetting(ctx context.Context, key string) (domain.AppSetting, error) {
	var setting domain.AppSetting
	err := s.pool.QueryRow(ctx, `
		SELECT key, value, version, updated_at
		FROM app_settings WHERE key=$1`, strings.TrimSpace(key)).Scan(
		&setting.Key, &setting.Value, &setting.Version, &setting.UpdatedAt,
	)
	if err != nil {
		return domain.AppSetting{}, mapError(err)
	}
	setting.Value = append(json.RawMessage(nil), setting.Value...)
	return setting, nil
}

func (s *Store) SaveAppSetting(ctx context.Context, setting domain.AppSetting, expectedVersion int64) error {
	setting.Key = strings.TrimSpace(setting.Key)
	if setting.Key == "" || !json.Valid(setting.Value) || expectedVersion < 0 {
		return fmt.Errorf("%w: valid app setting key, value, and version are required", domain.ErrInvalid)
	}
	if expectedVersion == 0 {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO app_settings (key, value, version, updated_at)
			VALUES ($1,$2,1,$3)`, setting.Key, setting.Value, setting.UpdatedAt)
		return mapError(err)
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE app_settings SET value=$2, version=version+1, updated_at=$3
		WHERE key=$1 AND version=$4`, setting.Key, setting.Value, setting.UpdatedAt, expectedVersion)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 0 {
		return nil
	}
	var currentVersion int64
	if err := s.pool.QueryRow(ctx, `SELECT version FROM app_settings WHERE key=$1`, setting.Key).Scan(&currentVersion); err != nil {
		return mapError(err)
	}
	return fmt.Errorf("%w: app setting version changed", domain.ErrConflict)
}

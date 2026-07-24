package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const providerConnectionColumns = `id, provider, name, encrypted_config, key_version,
	enabled, capabilities, metadata, version, created_at, updated_at`

func (s *Store) CreateProviderConnection(ctx context.Context, connection domain.ProviderConnection) error {
	connection.Version = 1
	capabilities, metadata, err := encodeProviderConnectionJSON(connection)
	if err != nil {
		return err
	}
	if err := validateProviderConnection(connection); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO provider_connections
			(id, provider, name, encrypted_config, key_version, enabled, capabilities,
			 metadata, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,$9,$10)`,
		connection.ID, connection.Provider, connection.Name, connection.EncryptedConfig,
		connection.KeyVersion, connection.Enabled, capabilities, metadata,
		connection.CreatedAt, connection.UpdatedAt)
	return mapError(err)
}

func (s *Store) GetProviderConnection(ctx context.Context, id string) (domain.ProviderConnection, error) {
	return scanProviderConnection(s.pool.QueryRow(ctx, `SELECT `+providerConnectionColumns+`
		FROM provider_connections WHERE id=$1`, id))
}

func (s *Store) GetProviderConnectionByProviderAndName(ctx context.Context, provider domain.ProviderKey, name string) (domain.ProviderConnection, error) {
	return scanProviderConnection(s.pool.QueryRow(ctx, `SELECT `+providerConnectionColumns+`
		FROM provider_connections WHERE provider=$1 AND name=$2`, provider, name))
}

func (s *Store) ListProviderConnections(ctx context.Context, filter ports.ProviderConnectionFilter, options ports.ListOptions) ([]domain.ProviderConnection, error) {
	options = options.Normalize(100, 500)
	var enabled any
	if filter.Enabled != nil {
		enabled = *filter.Enabled
	}
	rows, err := s.pool.Query(ctx, `SELECT `+providerConnectionColumns+`
		FROM provider_connections
		WHERE ($1 = '' OR provider=$1) AND ($2::boolean IS NULL OR enabled=$2)
		ORDER BY provider, name, id LIMIT $3 OFFSET $4`, filter.Provider, enabled, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.ProviderConnection, 0)
	for rows.Next() {
		item, err := scanProviderConnection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) UpdateProviderConnection(ctx context.Context, connection domain.ProviderConnection, expectedVersion int64) error {
	connection.Version = expectedVersion + 1
	capabilities, metadata, err := encodeProviderConnectionJSON(connection)
	if err != nil {
		return err
	}
	if err := validateProviderConnection(connection); err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE provider_connections SET name=$3, encrypted_config=$4, key_version=$5,
			enabled=$6, capabilities=$7, metadata=$8, version=version+1, updated_at=$9
		WHERE id=$1 AND provider=$2 AND version=$10`,
		connection.ID, connection.Provider, connection.Name, connection.EncryptedConfig,
		connection.KeyVersion, connection.Enabled, capabilities, metadata,
		connection.UpdatedAt, expectedVersion)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 0 {
		return nil
	}
	var currentProvider string
	var currentVersion int64
	if err := s.pool.QueryRow(ctx, `SELECT provider, version FROM provider_connections WHERE id=$1`, connection.ID).Scan(&currentProvider, &currentVersion); err != nil {
		return mapError(err)
	}
	if domain.ProviderKey(currentProvider) != connection.Provider {
		return fmt.Errorf("%w: provider connection provider is immutable", domain.ErrInvalid)
	}
	return fmt.Errorf("%w: provider connection version changed", domain.ErrConflict)
}

func scanProviderConnection(row scanner) (domain.ProviderConnection, error) {
	var item domain.ProviderConnection
	var provider string
	var capabilities, metadata []byte
	err := row.Scan(&item.ID, &provider, &item.Name, &item.EncryptedConfig, &item.KeyVersion,
		&item.Enabled, &capabilities, &metadata, &item.Version, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return domain.ProviderConnection{}, mapError(err)
	}
	if err := json.Unmarshal(capabilities, &item.Capabilities); err != nil {
		return domain.ProviderConnection{}, fmt.Errorf("decode provider connection capabilities: %w", err)
	}
	if _, err := requireJSONObject(metadata, "metadata"); err != nil {
		return domain.ProviderConnection{}, err
	}
	item.Provider = domain.ProviderKey(provider)
	item.Metadata = append(json.RawMessage(nil), metadata...)
	return item, nil
}

func encodeProviderConnectionJSON(connection domain.ProviderConnection) ([]byte, []byte, error) {
	capabilities, err := json.Marshal(connection.Capabilities)
	if err != nil {
		return nil, nil, fmt.Errorf("encode provider connection capabilities: %w", err)
	}
	if _, err := requireJSONObject(capabilities, "capabilities"); err != nil {
		return nil, nil, err
	}
	metadata := connection.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if _, err := requireJSONObject(metadata, "metadata"); err != nil {
		return nil, nil, err
	}
	return capabilities, metadata, nil
}

func requireJSONObject(value []byte, field string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: provider connection %s must be a JSON object", domain.ErrInvalid, field)
	}
	return object, nil
}

func validateProviderConnection(connection domain.ProviderConnection) error {
	if strings.TrimSpace(connection.ID) == "" || strings.TrimSpace(string(connection.Provider)) == "" || strings.TrimSpace(connection.Name) == "" {
		return fmt.Errorf("%w: provider connection id, provider, and name are required", domain.ErrInvalid)
	}
	if len(connection.EncryptedConfig) == 0 || strings.TrimSpace(connection.KeyVersion) == "" {
		return fmt.Errorf("%w: encrypted provider config and key version are required", domain.ErrInvalid)
	}
	if connection.Version <= 0 {
		return fmt.Errorf("%w: provider connection version must be positive", domain.ErrInvalid)
	}
	return nil
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const (
	DefaultProviderConnectionName = "default"
	providerEnvelopeVersion       = 1
)

type ProviderConnectionService struct {
	repository ports.ProviderConnectionRepository
	secrets    ports.SecretBroker
	clock      func() time.Time
}

func NewProviderConnectionService(repository ports.ProviderConnectionRepository, secrets ports.SecretBroker) (*ProviderConnectionService, error) {
	if repository == nil || secrets == nil {
		return nil, fmt.Errorf("%w: provider connection repository and secret broker are required", domain.ErrInvalid)
	}
	return &ProviderConnectionService{repository: repository, secrets: secrets, clock: time.Now}, nil
}

func (s *ProviderConnectionService) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

type SaveProviderConnectionInput struct {
	AccountID  string
	ZoneID     string
	ZoneName   string
	APIBaseURL string
	APIToken   string
	Enabled    *bool
	Version    int64
}

// ProviderConnectionSettings is the settings-facing representation. It
// intentionally has no field capable of carrying a provider token, ciphertext,
// or encryption key version.
type ProviderConnectionSettings struct {
	ID           string                      `json:"id"`
	Provider     domain.ProviderKey          `json:"provider"`
	Name         string                      `json:"name"`
	AccountID    string                      `json:"account_id,omitempty"`
	ZoneID       string                      `json:"zone_id,omitempty"`
	ZoneName     string                      `json:"zone_name,omitempty"`
	APIBaseURL   string                      `json:"api_base_url,omitempty"`
	Configured   bool                        `json:"configured"`
	Enabled      bool                        `json:"enabled"`
	Capabilities domain.ProviderCapabilities `json:"capabilities"`
	Metadata     json.RawMessage             `json:"metadata,omitempty"`
	Version      int64                       `json:"version"`
	CreatedAt    time.Time                   `json:"created_at"`
	UpdatedAt    time.Time                   `json:"updated_at"`
}

// CloudflareRuntimeConfig crosses the service-to-composition boundary. The API
// token remains excluded if this value is accidentally JSON encoded.
type CloudflareRuntimeConfig struct {
	APIToken   string `json:"-"`
	AccountID  string `json:"account_id"`
	ZoneID     string `json:"zone_id"`
	ZoneName   string `json:"zone_name,omitempty"`
	APIBaseURL string `json:"api_base_url,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type providerConnectionEnvelope struct {
	SchemaVersion int                        `json:"schema_version"`
	Provider      domain.ProviderKey         `json:"provider"`
	ConnectionID  string                     `json:"connection_id"`
	Config        cloudflareConnectionSecret `json:"config"`
}

type cloudflareConnectionSecret struct {
	APIToken  string `json:"api_token"`
	AccountID string `json:"account_id"`
	ZoneID    string `json:"zone_id"`
	ZoneName  string `json:"zone_name,omitempty"`
	BaseURL   string `json:"api_base_url,omitempty"`
}

func (s *ProviderConnectionService) Save(ctx context.Context, provider domain.ProviderKey, input SaveProviderConnectionInput) (ProviderConnectionSettings, error) {
	provider, err := normalizeConnectionProvider(provider)
	if err != nil {
		return ProviderConnectionSettings{}, err
	}
	if provider != domain.ProviderCloudflareRoute {
		return ProviderConnectionSettings{}, fmt.Errorf("%w: provider connection %q is not supported", domain.ErrInvalid, provider)
	}

	current, err := s.repository.GetProviderConnectionByProviderAndName(ctx, provider, DefaultProviderConnectionName)
	creating := errors.Is(err, domain.ErrNotFound)
	if err != nil && !creating {
		return ProviderConnectionSettings{}, err
	}

	var config cloudflareConnectionSecret
	if creating {
		if input.Version != 0 {
			return ProviderConnectionSettings{}, fmt.Errorf("%w: a new provider connection must use version 0", domain.ErrInvalid)
		}
		id, err := domain.NewRandomID("pconn")
		if err != nil {
			return ProviderConnectionSettings{}, err
		}
		now := s.clock().UTC()
		current = domain.ProviderConnection{
			ID: id, Provider: provider, Name: DefaultProviderConnectionName,
			Enabled: true, Capabilities: cloudflareCapabilities(), Metadata: json.RawMessage(`{}`),
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	} else {
		if input.Version <= 0 {
			return ProviderConnectionSettings{}, fmt.Errorf("%w: provider connection version is required", domain.ErrInvalid)
		}
		config, err = s.openCloudflareConfig(ctx, current)
		if err != nil {
			return ProviderConnectionSettings{}, err
		}
	}

	config.AccountID = strings.TrimSpace(input.AccountID)
	config.ZoneID = strings.TrimSpace(input.ZoneID)
	config.ZoneName = strings.ToLower(strings.TrimSpace(input.ZoneName))
	config.BaseURL, err = normalizeProviderAPIBase(input.APIBaseURL)
	if err != nil {
		return ProviderConnectionSettings{}, err
	}
	if token := strings.TrimSpace(input.APIToken); token != "" {
		config.APIToken = token
	}
	if config.AccountID == "" || config.ZoneID == "" || config.APIToken == "" {
		return ProviderConnectionSettings{}, fmt.Errorf("%w: Cloudflare account id, zone id, and API token are required", domain.ErrInvalid)
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}

	sealed, keyVersion, err := s.sealCloudflareConfig(ctx, current.ID, provider, config)
	if err != nil {
		return ProviderConnectionSettings{}, err
	}
	current.EncryptedConfig = sealed
	current.KeyVersion = keyVersion
	current.Capabilities = cloudflareCapabilities()
	current.UpdatedAt = s.clock().UTC()

	if creating {
		if err := s.repository.CreateProviderConnection(ctx, current); err != nil {
			return ProviderConnectionSettings{}, err
		}
	} else {
		expectedVersion := input.Version
		current.Version = expectedVersion + 1
		if err := s.repository.UpdateProviderConnection(ctx, current, expectedVersion); err != nil {
			return ProviderConnectionSettings{}, err
		}
	}
	return providerConnectionSettings(current, config), nil
}

func (s *ProviderConnectionService) Get(ctx context.Context, provider domain.ProviderKey) (ProviderConnectionSettings, error) {
	provider, err := normalizeConnectionProvider(provider)
	if err != nil {
		return ProviderConnectionSettings{}, err
	}
	connection, err := s.repository.GetProviderConnectionByProviderAndName(ctx, provider, DefaultProviderConnectionName)
	if err != nil {
		return ProviderConnectionSettings{}, err
	}
	return s.settings(ctx, connection)
}

func (s *ProviderConnectionService) List(ctx context.Context, options ports.ListOptions) ([]ProviderConnectionSettings, error) {
	connections, err := s.repository.ListProviderConnections(ctx, ports.ProviderConnectionFilter{}, options)
	if err != nil {
		return nil, err
	}
	items := make([]ProviderConnectionSettings, 0, len(connections))
	for _, connection := range connections {
		item, err := s.settings(ctx, connection)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// CloudflareRuntime returns found=true whenever a persisted default record
// exists, including when it is disabled. This lets an explicit database setting
// take precedence over deployment environment fallbacks.
func (s *ProviderConnectionService) CloudflareRuntime(ctx context.Context) (CloudflareRuntimeConfig, bool, error) {
	connection, err := s.repository.GetProviderConnectionByProviderAndName(ctx, domain.ProviderCloudflareRoute, DefaultProviderConnectionName)
	if errors.Is(err, domain.ErrNotFound) {
		return CloudflareRuntimeConfig{}, false, nil
	}
	if err != nil {
		return CloudflareRuntimeConfig{}, false, err
	}
	if !connection.Enabled {
		return CloudflareRuntimeConfig{Enabled: false}, true, nil
	}
	config, err := s.openCloudflareConfig(ctx, connection)
	if err != nil {
		return CloudflareRuntimeConfig{}, true, err
	}
	if !cloudflareConfigured(config) {
		return CloudflareRuntimeConfig{}, true, fmt.Errorf("%w: persisted Cloudflare connection is incomplete", domain.ErrNotConfigured)
	}
	return CloudflareRuntimeConfig{
		APIToken: config.APIToken, AccountID: config.AccountID, ZoneID: config.ZoneID,
		ZoneName: config.ZoneName, APIBaseURL: config.BaseURL, Enabled: true,
	}, true, nil
}

func (s *ProviderConnectionService) settings(ctx context.Context, connection domain.ProviderConnection) (ProviderConnectionSettings, error) {
	if connection.Provider != domain.ProviderCloudflareRoute {
		return ProviderConnectionSettings{
			ID: connection.ID, Provider: connection.Provider, Name: connection.Name,
			Configured: len(connection.EncryptedConfig) > 0, Enabled: connection.Enabled,
			Capabilities: connection.Capabilities, Metadata: cloneRawJSON(connection.Metadata),
			Version: connection.Version, CreatedAt: connection.CreatedAt, UpdatedAt: connection.UpdatedAt,
		}, nil
	}
	config, err := s.openCloudflareConfig(ctx, connection)
	if err != nil {
		return ProviderConnectionSettings{}, err
	}
	return providerConnectionSettings(connection, config), nil
}

func (s *ProviderConnectionService) sealCloudflareConfig(ctx context.Context, connectionID string, provider domain.ProviderKey, config cloudflareConnectionSecret) ([]byte, string, error) {
	payload, err := json.Marshal(providerConnectionEnvelope{
		SchemaVersion: providerEnvelopeVersion,
		Provider:      provider,
		ConnectionID:  connectionID,
		Config:        config,
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode provider connection secret: %w", err)
	}
	defer clear(payload)
	sealed, keyVersion, err := s.secrets.Seal(ctx, payload)
	if err != nil {
		return nil, "", fmt.Errorf("%w: provider connection secret could not be sealed", domain.ErrInvalid)
	}
	return sealed, keyVersion, nil
}

func (s *ProviderConnectionService) openCloudflareConfig(ctx context.Context, connection domain.ProviderConnection) (cloudflareConnectionSecret, error) {
	plaintext, err := s.secrets.Open(ctx, connection.EncryptedConfig, connection.KeyVersion)
	if err != nil {
		return cloudflareConnectionSecret{}, fmt.Errorf("%w: provider connection %s secret is unavailable", domain.ErrInvalid, connection.ID)
	}
	defer clear(plaintext)
	var envelope providerConnectionEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		return cloudflareConnectionSecret{}, fmt.Errorf("decode provider connection %s: %w", connection.ID, err)
	}
	if envelope.SchemaVersion != providerEnvelopeVersion || envelope.Provider != connection.Provider || envelope.ConnectionID != connection.ID {
		return cloudflareConnectionSecret{}, fmt.Errorf("%w: provider connection secret envelope does not match %s", domain.ErrInvalid, connection.ID)
	}
	return envelope.Config, nil
}

func providerConnectionSettings(connection domain.ProviderConnection, config cloudflareConnectionSecret) ProviderConnectionSettings {
	return ProviderConnectionSettings{
		ID: connection.ID, Provider: connection.Provider, Name: connection.Name,
		AccountID: config.AccountID, ZoneID: config.ZoneID, ZoneName: config.ZoneName,
		APIBaseURL: config.BaseURL, Configured: cloudflareConfigured(config), Enabled: connection.Enabled,
		Capabilities: connection.Capabilities, Metadata: cloneRawJSON(connection.Metadata),
		Version: connection.Version, CreatedAt: connection.CreatedAt, UpdatedAt: connection.UpdatedAt,
	}
}

func cloudflareCapabilities() domain.ProviderCapabilities {
	return domain.ProviderCapabilities{
		ProvisionMailbox: true,
		ManageAliases:    true,
		Forwarding:       true,
		RetrievalMethods: []domain.RetrievalMethod{domain.RetrievalForwarded},
	}
}

func cloudflareConfigured(config cloudflareConnectionSecret) bool {
	return strings.TrimSpace(config.APIToken) != "" && strings.TrimSpace(config.AccountID) != "" && strings.TrimSpace(config.ZoneID) != ""
}

func normalizeConnectionProvider(provider domain.ProviderKey) (domain.ProviderKey, error) {
	value := strings.ToLower(strings.TrimSpace(string(provider)))
	if value == "cloudflare" {
		value = string(domain.ProviderCloudflareRoute)
	}
	if value == "" {
		return "", fmt.Errorf("%w: provider is required", domain.ErrInvalid)
	}
	if value != string(domain.ProviderCloudflareRoute) && value != string(domain.ProviderMicrosoft) && value != string(domain.ProviderGmail) {
		return "", fmt.Errorf("%w: unknown provider %q", domain.ErrInvalid, value)
	}
	return domain.ProviderKey(value), nil
}

func normalizeProviderAPIBase(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: provider API base URL must be an absolute HTTP(S) URL", domain.ErrInvalid)
	}
	return value, nil
}

func cloneRawJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

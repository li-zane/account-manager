package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const (
	DefaultTokenRefreshEnabled         = true
	DefaultTokenRefreshLeadTimeMinutes = 5
	MinTokenRefreshLeadTimeMinutes     = 1
	MaxTokenRefreshLeadTimeMinutes     = 30
)

type TokenRefreshSettings struct {
	Enabled         bool      `json:"enabled"`
	LeadTimeMinutes int       `json:"lead_time_minutes"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type UpdateTokenRefreshSettingsInput struct {
	Enabled         bool
	LeadTimeMinutes int
	Version         int64
}

type TokenRefreshSettingsService struct {
	repository ports.AppSettingRepository
	clock      func() time.Time
}

func NewTokenRefreshSettingsService(repository ports.AppSettingRepository) (*TokenRefreshSettingsService, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: app setting repository is required", domain.ErrInvalid)
	}
	return &TokenRefreshSettingsService{repository: repository, clock: time.Now}, nil
}

func (s *TokenRefreshSettingsService) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

func (s *TokenRefreshSettingsService) Get(ctx context.Context) (TokenRefreshSettings, error) {
	setting, err := s.repository.GetAppSetting(ctx, domain.AppSettingKeyTokenRefresh)
	if errors.Is(err, domain.ErrNotFound) {
		return defaultTokenRefreshSettings(), nil
	}
	if err != nil {
		return TokenRefreshSettings{}, err
	}
	settings := defaultTokenRefreshSettings()
	if !strings.HasPrefix(strings.TrimSpace(string(setting.Value)), "{") {
		return TokenRefreshSettings{}, fmt.Errorf("%w: token refresh setting must be a JSON object", domain.ErrInvalid)
	}
	if err := json.Unmarshal(setting.Value, &settings); err != nil {
		return TokenRefreshSettings{}, fmt.Errorf("%w: decode token refresh setting: %v", domain.ErrInvalid, err)
	}
	if err := validateTokenRefreshLeadTime(settings.LeadTimeMinutes); err != nil {
		return TokenRefreshSettings{}, err
	}
	settings.Version = setting.Version
	settings.UpdatedAt = setting.UpdatedAt
	return settings, nil
}

func (s *TokenRefreshSettingsService) Update(ctx context.Context, input UpdateTokenRefreshSettingsInput) (TokenRefreshSettings, error) {
	if input.Version < 0 {
		return TokenRefreshSettings{}, fmt.Errorf("%w: token refresh setting version must be non-negative", domain.ErrInvalid)
	}
	if err := validateTokenRefreshLeadTime(input.LeadTimeMinutes); err != nil {
		return TokenRefreshSettings{}, err
	}
	value, err := json.Marshal(struct {
		Enabled         bool `json:"enabled"`
		LeadTimeMinutes int  `json:"lead_time_minutes"`
	}{Enabled: input.Enabled, LeadTimeMinutes: input.LeadTimeMinutes})
	if err != nil {
		return TokenRefreshSettings{}, fmt.Errorf("encode token refresh setting: %w", err)
	}
	now := s.clock().UTC()
	setting := domain.AppSetting{
		Key: domain.AppSettingKeyTokenRefresh, Value: value,
		Version: input.Version + 1, UpdatedAt: now,
	}
	if err := s.repository.SaveAppSetting(ctx, setting, input.Version); err != nil {
		return TokenRefreshSettings{}, err
	}
	return TokenRefreshSettings{
		Enabled: input.Enabled, LeadTimeMinutes: input.LeadTimeMinutes,
		Version: input.Version + 1, UpdatedAt: now,
	}, nil
}

func defaultTokenRefreshSettings() TokenRefreshSettings {
	return TokenRefreshSettings{Enabled: DefaultTokenRefreshEnabled, LeadTimeMinutes: DefaultTokenRefreshLeadTimeMinutes}
}

func validateTokenRefreshLeadTime(value int) error {
	if value < MinTokenRefreshLeadTimeMinutes || value > MaxTokenRefreshLeadTimeMinutes {
		return fmt.Errorf("%w: lead_time_minutes must be between %d and %d", domain.ErrInvalid, MinTokenRefreshLeadTimeMinutes, MaxTokenRefreshLeadTimeMinutes)
	}
	return nil
}

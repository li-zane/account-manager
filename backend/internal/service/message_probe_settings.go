package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const (
	DefaultMessageProbeIntervalMinutes = 10
	MinMessageProbeIntervalMinutes     = 1
	MaxMessageProbeIntervalMinutes     = 1440
)

type MessageProbeSettings struct {
	Enabled         bool      `json:"enabled"`
	IntervalMinutes int       `json:"interval_minutes"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type UpdateMessageProbeSettingsInput struct {
	Enabled         bool
	IntervalMinutes int
	Version         int64
}

type MessageProbeSettingsService struct {
	repository ports.AppSettingRepository
	clock      func() time.Time
}

func NewMessageProbeSettingsService(repository ports.AppSettingRepository) (*MessageProbeSettingsService, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: app setting repository is required", domain.ErrInvalid)
	}
	return &MessageProbeSettingsService{repository: repository, clock: time.Now}, nil
}

func (s *MessageProbeSettingsService) Get(ctx context.Context) (MessageProbeSettings, error) {
	setting, err := s.repository.GetAppSetting(ctx, domain.AppSettingKeyMessageProbe)
	if errors.Is(err, domain.ErrNotFound) {
		return defaultMessageProbeSettings(), nil
	}
	if err != nil {
		return MessageProbeSettings{}, err
	}
	settings := defaultMessageProbeSettings()
	if err := json.Unmarshal(setting.Value, &settings); err != nil {
		return MessageProbeSettings{}, fmt.Errorf("%w: decode message probe setting", domain.ErrInvalid)
	}
	if err := validateMessageProbeInterval(settings.IntervalMinutes); err != nil {
		return MessageProbeSettings{}, err
	}
	settings.Version, settings.UpdatedAt = setting.Version, setting.UpdatedAt
	return settings, nil
}

func (s *MessageProbeSettingsService) Update(ctx context.Context, input UpdateMessageProbeSettingsInput) (MessageProbeSettings, error) {
	if input.Version < 0 {
		return MessageProbeSettings{}, fmt.Errorf("%w: message probe setting version must be non-negative", domain.ErrInvalid)
	}
	if err := validateMessageProbeInterval(input.IntervalMinutes); err != nil {
		return MessageProbeSettings{}, err
	}
	value, err := json.Marshal(map[string]any{"enabled": input.Enabled, "interval_minutes": input.IntervalMinutes})
	if err != nil {
		return MessageProbeSettings{}, err
	}
	now := s.clock().UTC()
	setting := domain.AppSetting{Key: domain.AppSettingKeyMessageProbe, Value: value, Version: input.Version + 1, UpdatedAt: now}
	if err := s.repository.SaveAppSetting(ctx, setting, input.Version); err != nil {
		return MessageProbeSettings{}, err
	}
	return MessageProbeSettings{Enabled: input.Enabled, IntervalMinutes: input.IntervalMinutes, Version: input.Version + 1, UpdatedAt: now}, nil
}

func defaultMessageProbeSettings() MessageProbeSettings {
	return MessageProbeSettings{Enabled: false, IntervalMinutes: DefaultMessageProbeIntervalMinutes}
}

func validateMessageProbeInterval(value int) error {
	if value < MinMessageProbeIntervalMinutes || value > MaxMessageProbeIntervalMinutes {
		return fmt.Errorf("%w: interval_minutes must be between %d and %d", domain.ErrInvalid, MinMessageProbeIntervalMinutes, MaxMessageProbeIntervalMinutes)
	}
	return nil
}

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

type missingAppSettingRepository struct{}

func (missingAppSettingRepository) GetAppSetting(context.Context, string) (domain.AppSetting, error) {
	return domain.AppSetting{}, domain.ErrNotFound
}

func (missingAppSettingRepository) SaveAppSetting(context.Context, domain.AppSetting, int64) error {
	return nil
}

func TestTokenRefreshSettingsUseDefaultsWhenSeedIsMissing(t *testing.T) {
	settingsService, err := service.NewTokenRefreshSettingsService(missingAppSettingRepository{})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := settingsService.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || settings.LeadTimeMinutes != 5 || settings.Version != 0 {
		t.Fatalf("missing-seed defaults = %+v", settings)
	}
}

func TestTokenRefreshSettingsDefaultsAndCASUpdate(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	settingsService, err := service.NewTokenRefreshSettingsService(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 15, 30, 0, 0, time.UTC)
	settingsService.SetClock(func() time.Time { return now })

	current, err := settingsService.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !current.Enabled || current.LeadTimeMinutes != 5 || current.Version != 1 || current.UpdatedAt.IsZero() {
		t.Fatalf("default settings = %+v", current)
	}
	updated, err := settingsService.Update(ctx, service.UpdateTokenRefreshSettingsInput{
		Enabled: false, LeadTimeMinutes: 10, Version: current.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.LeadTimeMinutes != 10 || updated.Version != 2 || !updated.UpdatedAt.Equal(now) {
		t.Fatalf("updated settings = %+v", updated)
	}
	_, err = settingsService.Update(ctx, service.UpdateTokenRefreshSettingsInput{
		Enabled: true, LeadTimeMinutes: 5, Version: current.Version,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}
}

func TestTokenRefreshSettingsValidateLeadTime(t *testing.T) {
	settingsService, err := service.NewTokenRefreshSettingsService(memory.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []int{-1, 0, 31, 60} {
		_, err := settingsService.Update(context.Background(), service.UpdateTokenRefreshSettingsInput{
			Enabled: true, LeadTimeMinutes: value, Version: 1,
		})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("lead time %d error = %v, want invalid", value, err)
		}
	}
	for _, value := range []int{1, 30} {
		store := memory.New()
		settingsService, _ := service.NewTokenRefreshSettingsService(store)
		if _, err := settingsService.Update(context.Background(), service.UpdateTokenRefreshSettingsInput{
			Enabled: true, LeadTimeMinutes: value, Version: 1,
		}); err != nil {
			t.Errorf("lead time %d: %v", value, err)
		}
	}
}

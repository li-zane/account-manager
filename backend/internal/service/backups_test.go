package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestCreateBackupTargetRejectsInvalidSchedule(t *testing.T) {
	repository := memory.New()
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	backups, err := service.NewBackupService(repository, broker)
	if err != nil {
		t.Fatal(err)
	}
	_, err = backups.CreateTarget(context.Background(), service.CreateBackupTargetInput{
		Name: "invalid schedule", Kind: domain.BackupTargetS3,
		Config: json.RawMessage(`{"bucket":"fixture"}`), Schedule: "sometimes",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("create target error = %v, want invalid", err)
	}
	targets, listErr := backups.ListTargets(context.Background(), ports.ListOptions{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(targets) != 0 {
		t.Fatalf("created targets = %+v, want none", targets)
	}
}

func TestBackupTargetRetentionRangeIsEnforced(t *testing.T) {
	for _, retention := range []int{-1, 366} {
		t.Run(fmt.Sprintf("create_%d", retention), func(t *testing.T) {
			repository := memory.New()
			broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
			backups, _ := service.NewBackupService(repository, broker)
			_, err := backups.CreateTarget(context.Background(), service.CreateBackupTargetInput{
				Name: "invalid retention", Kind: domain.BackupTargetS3,
				Config: json.RawMessage(`{"bucket":"fixture"}`), RetentionCount: retention,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("retention %d error = %v, want invalid", retention, err)
			}
		})
	}
	repository := memory.New()
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	backups, _ := service.NewBackupService(repository, broker)
	target, err := backups.CreateTarget(context.Background(), service.CreateBackupTargetInput{
		Name: "default retention", Kind: domain.BackupTargetS3, Config: json.RawMessage(`{"bucket":"fixture"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.RetentionCount != 14 {
		t.Fatalf("default retention = %d, want 14", target.RetentionCount)
	}
	tooLarge := 366
	if _, err := backups.UpdateTarget(context.Background(), target.ID, service.UpdateBackupTargetInput{
		RetentionCount: &tooLarge, Version: target.Version,
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("update retention error = %v, want invalid", err)
	}
}

func TestUpdateBackupTargetPreservesSecretAndRejectsStaleVersion(t *testing.T) {
	repository := memory.New()
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	backups, err := service.NewBackupService(repository, broker)
	if err != nil {
		t.Fatal(err)
	}
	backups.SetConfigValidator(providers.ValidateBackupStoreConfig)
	backups.SetConfigRedactor(providers.RedactBackupStoreConfig)
	originalConfig := json.RawMessage(`{"endpoint":"https://s3.example.test","bucket":"fixture","access_key_id":"access-id","secret_access_key":"secret-value"}`)
	target, err := backups.CreateTarget(context.Background(), service.CreateBackupTargetInput{
		Name: "primary", Kind: domain.BackupTargetS3, Config: originalConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	schedule := "daily"
	updated, err := backups.UpdateTarget(context.Background(), target.ID, service.UpdateBackupTargetInput{
		Name: &name, Schedule: &schedule, Version: target.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Name != name || updated.Schedule != schedule {
		t.Fatalf("updated target = %+v", updated)
	}
	opened, err := broker.Open(context.Background(), updated.EncryptedConfig, updated.KeyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != string(originalConfig) {
		t.Fatalf("preserved config = %s, want %s", opened, originalConfig)
	}
	settings, err := backups.GetTargetSettings(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(settings)
	for _, secret := range []string{"access-id", "secret-value", "access_key_id", "secret_access_key", "key_version"} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("target settings exposed %q: %s", secret, serialized)
		}
	}
	if _, err := backups.UpdateTarget(context.Background(), target.ID, service.UpdateBackupTargetInput{
		Name: &name, Version: target.Version,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}
}

func TestQueueBackupRunConflictsWithActiveRestoreLease(t *testing.T) {
	repository := memory.New()
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	backups, err := service.NewBackupService(repository, broker)
	if err != nil {
		t.Fatal(err)
	}
	target, err := backups.CreateTarget(context.Background(), service.CreateBackupTargetInput{
		Name: "locked", Kind: domain.BackupTargetS3, Config: json.RawMessage(`{"bucket":"fixture"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	release, acquired, err := repository.TryAcquireBackupOperation(context.Background())
	if err != nil || !acquired {
		t.Fatalf("acquire backup operation: acquired=%v err=%v", acquired, err)
	}
	defer release()
	if _, err := backups.QueueRun(context.Background(), target.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("queue while restore lease is active error = %v, want conflict", err)
	}
}

func TestCreateBackupTargetRunsProviderConfigValidationBeforeSealing(t *testing.T) {
	repository := memory.New()
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	backups, err := service.NewBackupService(repository, broker)
	if err != nil {
		t.Fatal(err)
	}
	backups.SetConfigValidator(func(kind domain.BackupTargetKind, config json.RawMessage) error {
		if kind != domain.BackupTargetS3 || string(config) != `{"bucket":""}` {
			t.Fatalf("validator input = %q, %s", kind, config)
		}
		return domain.ErrInvalid
	})
	_, err = backups.CreateTarget(context.Background(), service.CreateBackupTargetInput{
		Name: "invalid provider config", Kind: domain.BackupTargetS3, Config: json.RawMessage(`{"bucket":""}`),
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("create target error = %v, want invalid", err)
	}
	targets, listErr := backups.ListTargets(context.Background(), ports.ListOptions{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(targets) != 0 {
		t.Fatalf("created targets = %+v, want none", targets)
	}
}

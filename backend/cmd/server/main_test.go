package main

import (
	"context"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestStartBackupRuntimeReturnsNilInterfaceInMemoryMode(t *testing.T) {
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	restores, stop, err := startBackupRuntime(context.Background(), "", memory.New(), broker, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if restores != nil {
		t.Fatalf("memory restore dependency = %#v, want nil interface", restores)
	}
}

func TestSelectCloudflareConfigPrecedence(t *testing.T) {
	environment := providers.CloudflareConfig{
		APIToken: "env-token", AccountID: "env-account", ZoneID: "env-zone",
	}
	persisted := service.CloudflareRuntimeConfig{
		APIToken: "db-token", AccountID: "db-account", ZoneID: "db-zone", Enabled: true,
	}

	if got := selectCloudflareConfig(environment, persisted, false); got.APIToken != "env-token" {
		t.Fatalf("missing persisted connection selected %+v", got)
	}
	if got := selectCloudflareConfig(environment, persisted, true); got.APIToken != "db-token" || got.AccountID != "db-account" || got.ZoneID != "db-zone" {
		t.Fatalf("persisted connection did not override environment: %+v", got)
	}
	persisted.Enabled = false
	if got := selectCloudflareConfig(environment, persisted, true); got.APIToken != "" || got.AccountID != "" || got.ZoneID != "" {
		t.Fatalf("disabled persisted connection fell back to environment: %+v", got)
	}
}

func TestEnvPositiveInt(t *testing.T) {
	t.Setenv("TEST_POSITIVE_INT", "")
	if got, err := envPositiveInt("TEST_POSITIVE_INT", 7); err != nil || got != 7 {
		t.Fatalf("default envPositiveInt() = %d, %v", got, err)
	}
	t.Setenv("TEST_POSITIVE_INT", "12")
	if got, err := envPositiveInt("TEST_POSITIVE_INT", 7); err != nil || got != 12 {
		t.Fatalf("configured envPositiveInt() = %d, %v", got, err)
	}
	for _, value := range []string{"0", "-1", "many"} {
		t.Setenv("TEST_POSITIVE_INT", value)
		if _, err := envPositiveInt("TEST_POSITIVE_INT", 7); err == nil {
			t.Fatalf("envPositiveInt(%q) succeeded", value)
		}
	}
}

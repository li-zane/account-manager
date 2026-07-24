package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/httpapi"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

type fakeBackupRestoreCoordinator struct {
	operation domain.BackupRestoreOperation
	startErr  error
	starts    int
}

func (f *fakeBackupRestoreCoordinator) Start(context.Context, string) (domain.BackupRestoreOperation, error) {
	f.starts++
	if f.startErr != nil {
		return domain.BackupRestoreOperation{}, f.startErr
	}
	return f.operation, nil
}

func (f *fakeBackupRestoreCoordinator) Get(context.Context, string) (domain.BackupRestoreOperation, error) {
	return f.operation, nil
}

func TestBackupRestoreHTTPRequiresConfirmationAndRuntime(t *testing.T) {
	disabled := newBackupHTTPTestRouter(t, nil)
	requestJSON(t, disabled, http.MethodPost, "/api/v1/backups/runs/brun_fixture/restore", `{"confirm":"RESTORE"}`, http.StatusNotImplemented)
	var typedNil *fakeBackupRestoreCoordinator
	typedNilRouter := newBackupHTTPTestRouter(t, typedNil)
	requestJSON(t, typedNilRouter, http.MethodPost, "/api/v1/backups/runs/brun_fixture/restore", `{"confirm":"RESTORE"}`, http.StatusNotImplemented)

	now := time.Now().UTC()
	coordinator := &fakeBackupRestoreCoordinator{operation: domain.BackupRestoreOperation{
		ID: "brestore_fixture", RunID: "brun_fixture", TargetID: "btarget_fixture",
		State: domain.BackupRestoreRunning, StartedAt: now, UpdatedAt: now,
	}}
	router := newBackupHTTPTestRouter(t, coordinator)
	requestJSON(t, router, http.MethodPost, "/api/v1/backups/runs/brun_fixture/restore", `{"confirm":"yes"}`, http.StatusBadRequest)
	if coordinator.starts != 0 {
		t.Fatalf("restore starts after invalid confirmation = %d, want 0", coordinator.starts)
	}
	started := requestJSON(t, router, http.MethodPost, "/api/v1/backups/runs/brun_fixture/restore", `{"confirm":"RESTORE"}`, http.StatusAccepted)
	if started["id"] != coordinator.operation.ID || started["state"] != string(domain.BackupRestoreRunning) {
		t.Fatalf("started restore response = %+v", started)
	}
	got := requestJSON(t, router, http.MethodGet, "/api/v1/backups/restores/"+coordinator.operation.ID, "", http.StatusOK)
	if got["run_id"] != coordinator.operation.RunID {
		t.Fatalf("restore status response = %+v", got)
	}
	coordinator.startErr = fmt.Errorf("%w: restore is busy", domain.ErrConflict)
	requestJSON(t, router, http.MethodPost, "/api/v1/backups/runs/brun_fixture/restore", `{"confirm":"RESTORE"}`, http.StatusConflict)
}

func TestBackupTargetHTTPUpdateCASAndRedaction(t *testing.T) {
	router := newBackupHTTPTestRouter(t, nil)
	created := requestJSON(t, router, http.MethodPost, "/api/v1/backups/targets", `{
		"name":"primary-s3",
		"kind":"s3",
		"config":{
			"endpoint":"https://s3.example.test",
			"bucket":"mail-backups",
			"access_key_id":"http-access-id",
			"secret_access_key":"http-secret-value"
		}
	}`, http.StatusCreated)
	assertBackupTargetRedacted(t, created)
	config, ok := created["config"].(map[string]any)
	if !ok || config["bucket"] != "mail-backups" || config["credentials_configured"] != true {
		t.Fatalf("redacted target config = %+v", created["config"])
	}
	id := created["id"].(string)
	updated := requestJSON(t, router, http.MethodPut, "/api/v1/backups/targets/"+id, `{
		"name":"renamed-s3",
		"enabled":false,
		"schedule":"daily",
		"version":1
	}`, http.StatusOK)
	assertBackupTargetRedacted(t, updated)
	if updated["version"] != float64(2) || updated["name"] != "renamed-s3" || updated["enabled"] != false {
		t.Fatalf("updated target = %+v", updated)
	}
	requestJSON(t, router, http.MethodPut, "/api/v1/backups/targets/"+id, `{"name":"stale","version":1}`, http.StatusConflict)
	got := requestJSON(t, router, http.MethodGet, "/api/v1/backups/targets/"+id, "", http.StatusOK)
	assertBackupTargetRedacted(t, got)
}

func assertBackupTargetRedacted(t *testing.T, value map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"http-access-id", "http-secret-value", "access_key_id", "secret_access_key", "key_version", "encrypted_config"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("backup target response exposed %q: %s", forbidden, encoded)
		}
	}
}

func newBackupHTTPTestRouter(t *testing.T, restores httpapi.BackupRestoreCoordinator) http.Handler {
	t.Helper()
	store := memory.New()
	cloudflare := providers.CloudflareRouteAdapter{}
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: providers.GmailAdapter{}, Retriever: providers.GmailAdapter{}},
		ports.ProviderRegistration{Provider: localCloudflareAliasProvider{CloudflareRouteAdapter: cloudflare}, Retriever: cloudflare},
	)
	if err != nil {
		t.Fatal(err)
	}
	mailboxes, _ := service.NewMailboxService(store, registry)
	accounts, _ := service.NewAccountService(store, store)
	broker, _ := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "backup-http-v1")
	backups, _ := service.NewBackupService(store, broker)
	backups.SetConfigValidator(providers.ValidateBackupStoreConfig)
	backups.SetConfigRedactor(providers.RedactBackupStoreConfig)
	pickupKeys, _ := security.NewPickupKeyService(store, []byte("01234567890123456789012345678901"))
	formats, _ := service.NewFormatService(store, registry)
	if err := formats.EnsureBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	details, _ := service.NewMailboxDetailService(store, store, broker)
	transfers, _ := service.NewImportExportService(store, store, store, registry, broker)
	retrieval, _ := service.NewMessageRetrievalService(store, registry, providers.MessageMatchesRecipient)
	router, err := httpapi.NewRouter(httpapi.Dependencies{
		Health: store, Providers: registry, AliasReader: store, Mailboxes: mailboxes,
		PickupKeys: pickupKeys, Accounts: accounts, Backups: backups, BackupRestores: restores,
		Details: details, Formats: formats, Transfers: transfers, Retrieval: retrieval,
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	backupengine "github.com/li-zane/account-manager/backend/internal/backup"
	"github.com/li-zane/account-manager/backend/internal/httpapi"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	postgresrepo "github.com/li-zane/account-manager/backend/internal/repository/postgres"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	secretKey, ephemeral, err := loadSecretKey()
	if err != nil {
		return err
	}
	if databaseURL != "" && ephemeral {
		return fmt.Errorf("APP_ENCRYPTION_KEY_BASE64 is required when DATABASE_URL is configured")
	}
	store, closeStore, persistent, err := openStore(rootCtx)
	if err != nil {
		return err
	}
	defer closeStore()
	adminToken := strings.TrimSpace(os.Getenv("ADMIN_API_TOKEN"))
	if persistent && adminToken == "" {
		return fmt.Errorf("ADMIN_API_TOKEN is required when DATABASE_URL is configured")
	}
	pickupPepper, derivedPepper, err := loadPickupPepper(secretKey)
	if err != nil {
		return err
	}
	if persistent && derivedPepper {
		return fmt.Errorf("PICKUP_KEY_PEPPER_BASE64 is required when DATABASE_URL is configured")
	}

	if ephemeral {
		logger.Warn("using an ephemeral encryption key; configured secrets last only for this in-memory process")
	}
	broker, err := security.NewAESGCMBroker(secretKey, envOrDefault("APP_ENCRYPTION_KEY_VERSION", "v1"))
	if err != nil {
		return fmt.Errorf("create secret broker: %w", err)
	}
	providerConnections, err := service.NewProviderConnectionService(store, broker)
	if err != nil {
		return fmt.Errorf("create provider connection service: %w", err)
	}
	providerHTTPClient := &http.Client{Timeout: 30 * time.Second}
	microsoft := providers.NewMicrosoftAdapter(providers.MicrosoftConfig{
		TokenEndpoint: strings.TrimSpace(os.Getenv("MICROSOFT_TOKEN_ENDPOINT")),
		GraphBaseURL:  strings.TrimSpace(os.Getenv("MICROSOFT_GRAPH_BASE_URL")),
	}, broker, providerHTTPClient)
	gmail := providers.NewGmailAdapter(broker, providerHTTPClient)
	gmail.APIBase = strings.TrimSpace(os.Getenv("GMAIL_API_BASE_URL"))
	gmail.TokenEndpoint = strings.TrimSpace(os.Getenv("GOOGLE_TOKEN_ENDPOINT"))
	environmentCloudflareConfig := providers.CloudflareConfig{
		APIToken:  strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")),
		AccountID: strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")),
		ZoneID:    strings.TrimSpace(os.Getenv("CLOUDFLARE_ZONE_ID")),
		ZoneName:  strings.TrimSpace(os.Getenv("CLOUDFLARE_ZONE_NAME")),
		BaseURL:   strings.TrimSpace(os.Getenv("CLOUDFLARE_API_BASE_URL")),
	}
	loadCloudflareConfig := func(ctx context.Context) (providers.CloudflareConfig, error) {
		persistedCloudflare, found, err := providerConnections.CloudflareRuntime(ctx)
		if err != nil {
			return providers.CloudflareConfig{}, err
		}
		return selectCloudflareConfig(environmentCloudflareConfig, persistedCloudflare, found), nil
	}
	if _, err := loadCloudflareConfig(rootCtx); err != nil {
		return fmt.Errorf("load persisted Cloudflare connection: %w", err)
	}
	cloudflare := providers.NewDynamicCloudflareRouteAdapter(loadCloudflareConfig, providerHTTPClient)
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: microsoft, Retriever: microsoft},
		ports.ProviderRegistration{Provider: gmail, Retriever: gmail},
		ports.ProviderRegistration{Provider: cloudflare, Retriever: cloudflare},
	)
	if err != nil {
		return fmt.Errorf("create provider registry: %w", err)
	}
	pickupKeys, err := security.NewPickupKeyService(store, pickupPepper)
	if err != nil {
		return fmt.Errorf("create pickup key service: %w", err)
	}
	mailboxes, err := service.NewMailboxService(store, registry)
	if err != nil {
		return err
	}
	retrieval, err := service.NewMessageRetrievalService(store, registry, providers.MessageMatchesRecipient)
	if err != nil {
		return err
	}
	tokenRefreshSettings, err := service.NewTokenRefreshSettingsService(store)
	if err != nil {
		return fmt.Errorf("create token refresh settings service: %w", err)
	}
	stopCredentialRefreshRuntime, err := startCredentialRefreshRuntime(rootCtx, store, retrieval, tokenRefreshSettings, logger)
	if err != nil {
		return err
	}
	defer stopCredentialRefreshRuntime()
	accounts, err := service.NewAccountService(store, store)
	if err != nil {
		return err
	}
	formats, err := service.NewFormatService(store, registry)
	if err != nil {
		return err
	}
	if err := formats.EnsureBuiltins(rootCtx); err != nil {
		return fmt.Errorf("initialize built-in mailbox formats: %w", err)
	}
	details, err := service.NewMailboxDetailService(store, store, broker)
	if err != nil {
		return err
	}
	details.SetSettingsReader(tokenRefreshSettings)
	transfers, err := service.NewImportExportService(store, store, store, registry, broker)
	if err != nil {
		return err
	}
	transfers.SetPickupKeyPreparer(pickupKeys)
	backups, err := service.NewBackupService(store, broker)
	if err != nil {
		return err
	}
	backups.SetConfigValidator(providers.ValidateBackupStoreConfig)
	backups.SetConfigRedactor(providers.RedactBackupStoreConfig)
	backupRestores, stopBackupRuntime, err := startBackupRuntime(rootCtx, databaseURL, store, broker, logger)
	if err != nil {
		return err
	}
	defer stopBackupRuntime()
	router, err := httpapi.NewRouter(httpapi.Dependencies{
		Health: store, Providers: registry, AliasReader: store, Mailboxes: mailboxes,
		PickupKeys: pickupKeys, Accounts: accounts, Backups: backups,
		BackupRestores: backupRestores,
		Details:        details, Formats: formats, Transfers: transfers, Retrieval: retrieval,
		ProviderConnections: providerConnections, TokenRefreshSettings: tokenRefreshSettings,
		AdminToken: adminToken, Logger: logger,
	})
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              envOrDefault("HTTP_ADDR", "127.0.0.1:8080"),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("mail platform backend listening", "address", server.Addr, "persistent", persistent)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-rootCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func startCredentialRefreshRuntime(parent context.Context, repository service.CredentialRefreshRepository, refresher service.DueCredentialRefresher, settings *service.TokenRefreshSettingsService, logger *slog.Logger) (func(), error) {
	if strings.EqualFold(envOrDefault("TOKEN_REFRESH_WORKER_ENABLED", "true"), "false") {
		logger.Info("credential refresh worker disabled by environment")
		return func() {}, nil
	}
	interval, err := envDuration("TOKEN_REFRESH_WORKER_INTERVAL", time.Minute)
	if err != nil {
		return nil, err
	}
	concurrency, err := envPositiveInt("TOKEN_REFRESH_WORKER_CONCURRENCY", 4)
	if err != nil {
		return nil, err
	}
	itemTimeout, err := envDuration("TOKEN_REFRESH_WORKER_ITEM_TIMEOUT", 45*time.Second)
	if err != nil {
		return nil, err
	}
	errorBackoff, err := envDuration("TOKEN_REFRESH_WORKER_ERROR_BACKOFF", 15*time.Minute)
	if err != nil {
		return nil, err
	}
	worker, err := service.NewCredentialRefreshWorker(repository, refresher, logger, service.CredentialRefreshWorkerConfig{
		Enabled: true, Interval: interval, Concurrency: concurrency,
		ItemTimeout: itemTimeout, ErrorBackoff: errorBackoff,
	})
	if err != nil {
		return nil, fmt.Errorf("create credential refresh worker: %w", err)
	}
	worker.SetSettingsReader(settings)

	runtimeCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := worker.Run(runtimeCtx); err != nil {
			logger.Error("credential refresh worker stopped", "error", err)
		}
	}()
	logger.Info("credential refresh worker started", "interval", interval, "concurrency", concurrency, "item_timeout", itemTimeout, "error_backoff", errorBackoff)

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				logger.Warn("credential refresh worker shutdown timed out")
			}
		})
	}, nil
}

func startBackupRuntime(parent context.Context, databaseURL string, store ports.Store, broker ports.SecretBroker, logger *slog.Logger) (httpapi.BackupRestoreCoordinator, func(), error) {
	if strings.TrimSpace(databaseURL) == "" || strings.EqualFold(envOrDefault("BACKUP_WORKER_ENABLED", "true"), "false") {
		return nil, func() {}, nil
	}
	tools, err := backupengine.NewPostgreSQLTools(
		databaseURL,
		strings.TrimSpace(os.Getenv("PG_DUMP_PATH")),
		strings.TrimSpace(os.Getenv("PG_RESTORE_PATH")),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create PostgreSQL backup tools: %w", err)
	}
	worker, err := backupengine.NewWorker(store, broker, tools, providers.NewBackupStore, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("create backup worker: %w", err)
	}
	pollInterval, err := envDuration("BACKUP_WORKER_POLL_INTERVAL", 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	worker.SetPollInterval(pollInterval)
	scheduler, err := backupengine.NewScheduler(store, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("create backup scheduler: %w", err)
	}
	schedulerInterval, err := envDuration("BACKUP_SCHEDULER_INTERVAL", 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	scheduler.SetInterval(schedulerInterval)

	runtimeCtx, cancel := context.WithCancel(parent)
	restores, err := backupengine.NewRestoreCoordinator(runtimeCtx, worker, store)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create backup restore coordinator: %w", err)
	}
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		if err := scheduler.Run(runtimeCtx); err != nil {
			logger.Error("backup scheduler stopped", "error", err)
		}
	}()
	go func() {
		defer group.Done()
		if err := worker.Run(runtimeCtx); err != nil {
			logger.Error("backup worker stopped", "error", err)
		}
	}()
	logger.Info("backup runtime started", "worker_poll_interval", pollInterval, "scheduler_interval", schedulerInterval)

	var once sync.Once
	return restores, func() {
		once.Do(func() {
			cancel()
			done := make(chan struct{})
			go func() {
				group.Wait()
				restores.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				logger.Warn("backup runtime shutdown timed out")
			}
		})
	}, nil
}

func openStore(ctx context.Context) (ports.Store, func(), bool, error) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return memory.New(), func() {}, false, nil
	}
	store, err := postgresrepo.Open(ctx, databaseURL)
	if err != nil {
		return nil, nil, true, err
	}
	if strings.ToLower(envOrDefault("AUTO_MIGRATE", "true")) != "false" {
		if err := store.Migrate(ctx); err != nil {
			store.Close()
			return nil, nil, true, fmt.Errorf("apply database migrations: %w", err)
		}
	}
	return store, store.Close, true, nil
}

func loadSecretKey() ([]byte, bool, error) {
	encoded := strings.TrimSpace(os.Getenv("APP_ENCRYPTION_KEY_BASE64"))
	if encoded != "" {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, false, fmt.Errorf("decode APP_ENCRYPTION_KEY_BASE64: %w", err)
		}
		if len(key) != 16 && len(key) != 24 && len(key) != 32 {
			return nil, false, fmt.Errorf("APP_ENCRYPTION_KEY_BASE64 must decode to 16, 24, or 32 bytes")
		}
		return key, false, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, true, fmt.Errorf("generate development secret key: %w", err)
	}
	return key, true, nil
}

func loadPickupPepper(fallbackKey []byte) ([]byte, bool, error) {
	encoded := strings.TrimSpace(os.Getenv("PICKUP_KEY_PEPPER_BASE64"))
	if encoded != "" {
		pepper, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, false, fmt.Errorf("decode PICKUP_KEY_PEPPER_BASE64: %w", err)
		}
		if len(pepper) < 16 {
			return nil, false, fmt.Errorf("PICKUP_KEY_PEPPER_BASE64 must decode to at least 16 bytes")
		}
		return pepper, false, nil
	}
	digest := sha256.Sum256(append(append([]byte(nil), fallbackKey...), []byte("mailbox-pickup-key")...))
	return digest[:], true, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}

func envPositiveInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func selectCloudflareConfig(environment providers.CloudflareConfig, persisted service.CloudflareRuntimeConfig, found bool) providers.CloudflareConfig {
	if !found {
		return environment
	}
	if !persisted.Enabled {
		return providers.CloudflareConfig{}
	}
	return providers.CloudflareConfig{
		APIToken:  persisted.APIToken,
		AccountID: persisted.AccountID,
		ZoneID:    persisted.ZoneID,
		ZoneName:  persisted.ZoneName,
		BaseURL:   persisted.APIBaseURL,
	}
}

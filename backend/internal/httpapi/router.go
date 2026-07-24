package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

type Dependencies struct {
	Health               ports.HealthRepository
	Providers            ports.ProviderRegistry
	AliasReader          MailboxAliasReader
	Mailboxes            *service.MailboxService
	PickupKeys           *security.PickupKeyService
	Accounts             *service.AccountService
	Backups              *service.BackupService
	BackupRestores       BackupRestoreCoordinator
	Details              *service.MailboxDetailService
	Formats              *service.FormatService
	Transfers            *service.ImportExportService
	Retrieval            *service.MessageRetrievalService
	MessageCache         *service.MessageCacheService
	MessageProbeSettings *service.MessageProbeSettingsService
	ProviderConnections  *service.ProviderConnectionService
	TokenRefreshSettings *service.TokenRefreshSettingsService
	AdminToken           string
	Logger               *slog.Logger
}

type MailboxAliasReader interface {
	GetAlias(ctx context.Context, id string) (domain.MailboxAlias, error)
}

type BackupRestoreCoordinator interface {
	Start(ctx context.Context, runID string) (domain.BackupRestoreOperation, error)
	Get(ctx context.Context, id string) (domain.BackupRestoreOperation, error)
}

func NewRouter(deps Dependencies) (http.Handler, error) {
	if deps.Health == nil || deps.Providers == nil || deps.AliasReader == nil || deps.Mailboxes == nil || deps.PickupKeys == nil || deps.Accounts == nil || deps.Backups == nil || deps.Details == nil || deps.Formats == nil || deps.Transfers == nil || deps.Retrieval == nil {
		return nil, fmt.Errorf("%w: all HTTP dependencies are required", domain.ErrInvalid)
	}
	h := &handler{deps: deps, admin: security.NewAdminAuthenticator(deps.AdminToken)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /api/v1/providers", h.providers)
	if deps.ProviderConnections != nil {
		mux.HandleFunc("GET /api/v1/provider-connections", h.listProviderConnections)
		mux.HandleFunc("GET /api/v1/provider-connections/{provider}", h.getProviderConnection)
		mux.HandleFunc("PUT /api/v1/provider-connections/{provider}", h.saveProviderConnection)
	}
	if deps.TokenRefreshSettings != nil {
		mux.HandleFunc("GET /api/v1/settings/token-refresh", h.getTokenRefreshSettings)
		mux.HandleFunc("PUT /api/v1/settings/token-refresh", h.updateTokenRefreshSettings)
	}
	if deps.MessageProbeSettings != nil {
		mux.HandleFunc("GET /api/v1/settings/message-probe", h.getMessageProbeSettings)
		mux.HandleFunc("PUT /api/v1/settings/message-probe", h.updateMessageProbeSettings)
	}
	mux.HandleFunc("GET /api/v1/mailboxes", h.listMailboxes)
	mux.HandleFunc("POST /api/v1/mailboxes", h.createMailbox)
	mux.HandleFunc("GET /api/v1/mailboxes/overview", h.mailboxOverview)
	mux.HandleFunc("GET /api/v1/mailboxes/{mailboxID}/detail", h.mailboxDetail)
	mux.HandleFunc("GET /api/v1/mailboxes/{mailboxID}/messages", h.retrieveMailboxMessages)
	mux.HandleFunc("GET /api/v1/mailbox-aliases/{aliasID}/messages", h.retrieveAliasMessages)
	if deps.MessageCache != nil {
		mux.HandleFunc("GET /api/v1/mailboxes/{mailboxID}/cached-messages", h.listMailboxCachedMessages)
		mux.HandleFunc("POST /api/v1/mailboxes/{mailboxID}/messages/sync", h.syncMailboxMessages)
		mux.HandleFunc("GET /api/v1/mailbox-aliases/{aliasID}/cached-messages", h.listAliasCachedMessages)
		mux.HandleFunc("POST /api/v1/mailbox-aliases/{aliasID}/messages/sync", h.syncAliasMessages)
	}
	mux.HandleFunc("GET /api/v1/pickup/messages", h.retrievePickupMessages)
	mux.HandleFunc("POST /api/v1/mailboxes/{mailboxID}/credentials/reveal", h.revealCredential)
	mux.HandleFunc("POST /api/v1/mailboxes/import/preview", h.previewMailboxImport)
	mux.HandleFunc("POST /api/v1/mailboxes/import", h.importMailboxes)
	mux.HandleFunc("POST /api/v1/mailboxes/export/preview", h.previewMailboxExport)
	mux.HandleFunc("POST /api/v1/mailboxes/export", h.exportMailboxes)
	mux.HandleFunc("GET /api/v1/mailboxes/{mailboxID}", h.getMailbox)
	mux.HandleFunc("GET /api/v1/mailboxes/{mailboxID}/aliases", h.listAliases)
	mux.HandleFunc("POST /api/v1/mailboxes/{mailboxID}/aliases", h.createAlias)
	mux.HandleFunc("GET /api/v1/mailboxes/{mailboxID}/pickup-keys", h.listPickupKeys)
	mux.HandleFunc("POST /api/v1/mailboxes/{mailboxID}/pickup-keys", h.issuePickupKey)
	mux.HandleFunc("DELETE /api/v1/mailboxes/{mailboxID}/pickup-keys/{keyID}", h.revokePickupKey)
	mux.HandleFunc("GET /api/v1/platform-accounts", h.listAccounts)
	mux.HandleFunc("POST /api/v1/platform-accounts", h.createAccount)
	mux.HandleFunc("GET /api/v1/platform-accounts/{accountID}/mailbox", h.resolveAccount)
	mux.HandleFunc("GET /api/v1/mailbox-formats", h.listMailboxFormats)
	mux.HandleFunc("POST /api/v1/mailbox-formats", h.createMailboxFormat)
	mux.HandleFunc("GET /api/v1/mailbox-formats/{formatID}", h.getMailboxFormat)
	mux.HandleFunc("PUT /api/v1/mailbox-formats/{formatID}", h.updateMailboxFormat)
	mux.HandleFunc("GET /api/v1/backups/targets", h.listBackupTargets)
	mux.HandleFunc("POST /api/v1/backups/targets", h.createBackupTarget)
	mux.HandleFunc("GET /api/v1/backups/targets/{targetID}", h.getBackupTarget)
	mux.HandleFunc("PUT /api/v1/backups/targets/{targetID}", h.updateBackupTarget)
	mux.HandleFunc("GET /api/v1/backups/runs", h.listBackupRuns)
	mux.HandleFunc("POST /api/v1/backups/runs", h.queueBackupRun)
	mux.HandleFunc("GET /api/v1/backups/runs/{runID}", h.getBackupRun)
	mux.HandleFunc("POST /api/v1/backups/runs/{runID}/restore", h.startBackupRestore)
	mux.HandleFunc("GET /api/v1/backups/restores/{restoreID}", h.getBackupRestore)
	mux.HandleFunc("POST /api/v1/backups", h.queueDefaultBackupRun)
	return withMiddleware(mux, deps.Logger, h.admin), nil
}

type handler struct {
	deps  Dependencies
	admin *security.AdminAuthenticator
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.deps.Health.Ping(ctx); err != nil {
		writeError(w, fmt.Errorf("health check: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (h *handler) providers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": h.deps.Providers.List(r.Context())})
}

type tokenRefreshSettingsRequest struct {
	Enabled         bool  `json:"enabled"`
	LeadTimeMinutes int   `json:"lead_time_minutes"`
	Version         int64 `json:"version"`
}

func (h *handler) getTokenRefreshSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.deps.TokenRefreshSettings.Get(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *handler) updateTokenRefreshSettings(w http.ResponseWriter, r *http.Request) {
	var request tokenRefreshSettingsRequest
	if decodeJSON(w, r, &request) != nil {
		return
	}
	settings, err := h.deps.TokenRefreshSettings.Update(r.Context(), service.UpdateTokenRefreshSettingsInput{
		Enabled: request.Enabled, LeadTimeMinutes: request.LeadTimeMinutes, Version: request.Version,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

type providerConnectionRequest struct {
	AccountID  string `json:"account_id"`
	ZoneID     string `json:"zone_id"`
	ZoneName   string `json:"zone_name"`
	APIBaseURL string `json:"api_base_url"`
	APIToken   string `json:"api_token"`
	Enabled    *bool  `json:"enabled"`
	Version    int64  `json:"version"`
}

func (h *handler) listProviderConnections(w http.ResponseWriter, r *http.Request) {
	items, err := h.deps.ProviderConnections.List(r.Context(), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *handler) getProviderConnection(w http.ResponseWriter, r *http.Request) {
	item, err := h.deps.ProviderConnections.Get(r.Context(), domain.ProviderKey(r.PathValue("provider")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *handler) saveProviderConnection(w http.ResponseWriter, r *http.Request) {
	var request providerConnectionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	item, err := h.deps.ProviderConnections.Save(r.Context(), domain.ProviderKey(r.PathValue("provider")), service.SaveProviderConnectionInput{
		AccountID: request.AccountID, ZoneID: request.ZoneID, ZoneName: request.ZoneName,
		APIBaseURL: request.APIBaseURL, APIToken: request.APIToken,
		Enabled: request.Enabled, Version: request.Version,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *handler) listMailboxes(w http.ResponseWriter, r *http.Request) {
	items, err := h.deps.Mailboxes.List(r.Context(), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	counts, err := h.deps.Mailboxes.Counts(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": counts.MainMailboxes,
		"main_mailbox_count": counts.MainMailboxes, "alias_count": counts.Aliases,
	})
}

type createMailboxRequest struct {
	Provider          domain.ProviderKey `json:"provider"`
	Address           string             `json:"address"`
	DisplayName       string             `json:"display_name"`
	ExternalReference string             `json:"external_reference"`
	Metadata          json.RawMessage    `json:"metadata"`
}

func (h *handler) createMailbox(w http.ResponseWriter, r *http.Request) {
	var request createMailboxRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	mailbox, err := h.deps.Mailboxes.Create(r.Context(), service.CreateMailboxInput{
		Provider: request.Provider, Address: request.Address, DisplayName: request.DisplayName,
		ExternalReference: request.ExternalReference, Metadata: request.Metadata,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mailbox)
}

func (h *handler) getMailbox(w http.ResponseWriter, r *http.Request) {
	mailbox, err := h.deps.Mailboxes.Get(r.Context(), r.PathValue("mailboxID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mailbox)
}

type aliasRequest struct {
	Provider domain.ProviderKey `json:"provider"`
	Address  string             `json:"address"`
	Kind     domain.AliasKind   `json:"kind"`
	Enabled  *bool              `json:"enabled"`
	Metadata json.RawMessage    `json:"metadata"`
}

func (h *handler) listAliases(w http.ResponseWriter, r *http.Request) {
	items, err := h.deps.Mailboxes.ListAliases(r.Context(), r.PathValue("mailboxID"), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *handler) createAlias(w http.ResponseWriter, r *http.Request) {
	var request aliasRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	alias, err := h.deps.Mailboxes.CreateAlias(r.Context(), r.PathValue("mailboxID"), service.CreateAliasInput{
		Provider: request.Provider, Address: request.Address, Kind: request.Kind,
		Enabled: request.Enabled, Metadata: request.Metadata,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, alias)
}

type issuePickupKeyRequest struct {
	Label     string     `json:"label"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type issuedPickupKeyResponse struct {
	Key   domain.MailboxPickupKey `json:"key"`
	Token string                  `json:"token"`
}

func (h *handler) issuePickupKey(w http.ResponseWriter, r *http.Request) {
	var request issuePickupKeyRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &request); err != nil {
			return
		}
	}
	mailboxID := r.PathValue("mailboxID")
	if _, err := h.deps.Mailboxes.Get(r.Context(), mailboxID); err != nil {
		writeError(w, err)
		return
	}
	key, token, err := h.deps.PickupKeys.Issue(r.Context(), mailboxID, request.Label, request.ExpiresAt)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, issuedPickupKeyResponse{Key: key, Token: token})
}

func (h *handler) listPickupKeys(w http.ResponseWriter, r *http.Request) {
	if _, err := h.deps.Mailboxes.Get(r.Context(), r.PathValue("mailboxID")); err != nil {
		writeError(w, err)
		return
	}
	items, err := h.deps.PickupKeys.List(r.Context(), r.PathValue("mailboxID"), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *handler) revokePickupKey(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.PickupKeys.Revoke(r.Context(), r.PathValue("mailboxID"), r.PathValue("keyID")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) retrieveMailboxMessages(w http.ResponseWriter, r *http.Request) {
	query, err := parseMessageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.retrieveMessages(w, r, service.RetrieveMessagesInput{MailboxID: r.PathValue("mailboxID"), Query: query})
}

func (h *handler) retrieveAliasMessages(w http.ResponseWriter, r *http.Request) {
	query, err := parseMessageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.retrieveMessages(w, r, service.RetrieveMessagesInput{AliasID: r.PathValue("aliasID"), Query: query})
}

func (h *handler) retrievePickupMessages(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, domain.ErrUnauthorized)
		return
	}
	key, err := h.deps.PickupKeys.Lookup(r.Context(), token)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrKeyExpired), errors.Is(err, domain.ErrKeyRevoked):
			writeError(w, err)
		case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalid):
			writeError(w, domain.ErrUnauthorized)
		default:
			writeError(w, err)
		}
		return
	}

	input := service.RetrieveMessagesInput{MailboxID: key.MailboxID}
	if aliasID := strings.TrimSpace(r.URL.Query().Get("alias_id")); aliasID != "" {
		alias, err := h.deps.AliasReader.GetAlias(r.Context(), aliasID)
		if err != nil {
			writeError(w, err)
			return
		}
		if alias.MailboxID != key.MailboxID {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "forbidden", "message": "pickup key does not grant access to this alias",
			})
			return
		}
		input.MailboxID, input.AliasID = "", aliasID
	}
	input.Query, err = parseMessageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.retrieveMessages(w, r, input)
}

func (h *handler) retrieveMessages(w http.ResponseWriter, r *http.Request, input service.RetrieveMessagesInput) {
	items, err := h.deps.Retrieval.Retrieve(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type accountRequest struct {
	Platform          string          `json:"platform"`
	ExternalReference string          `json:"external_reference"`
	MailboxID         string          `json:"mailbox_id"`
	MailboxAliasID    *string         `json:"mailbox_alias_id"`
	LoginAddress      string          `json:"login_address"`
	Status            string          `json:"status"`
	Metadata          json.RawMessage `json:"metadata"`
}

func (h *handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	items, err := h.deps.Accounts.List(r.Context(), r.URL.Query().Get("platform"), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var request accountRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	account, err := h.deps.Accounts.Create(r.Context(), service.CreateAccountInput{
		Platform: request.Platform, ExternalReference: request.ExternalReference,
		MailboxID: request.MailboxID, MailboxAliasID: request.MailboxAliasID,
		LoginAddress: request.LoginAddress, Status: request.Status, Metadata: request.Metadata,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, account)
}

func (h *handler) resolveAccount(w http.ResponseWriter, r *http.Request) {
	routed, err := h.deps.Accounts.ResolveMailbox(r.Context(), r.PathValue("accountID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, routed)
}

type backupTargetRequest struct {
	Name           string                  `json:"name"`
	Kind           domain.BackupTargetKind `json:"kind"`
	Config         json.RawMessage         `json:"config"`
	Enabled        *bool                   `json:"enabled"`
	Schedule       string                  `json:"schedule"`
	RetentionCount int                     `json:"retention_count"`
	Metadata       json.RawMessage         `json:"metadata"`
}

type updateBackupTargetRequest struct {
	Name           *string                  `json:"name"`
	Kind           *domain.BackupTargetKind `json:"kind"`
	Config         json.RawMessage          `json:"config"`
	Enabled        *bool                    `json:"enabled"`
	Schedule       *string                  `json:"schedule"`
	RetentionCount *int                     `json:"retention_count"`
	Metadata       *json.RawMessage         `json:"metadata"`
	Version        int64                    `json:"version"`
}

func (h *handler) listBackupTargets(w http.ResponseWriter, r *http.Request) {
	items, err := h.deps.Backups.ListTargetSettings(r.Context(), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *handler) createBackupTarget(w http.ResponseWriter, r *http.Request) {
	var request backupTargetRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	target, err := h.deps.Backups.CreateTarget(r.Context(), service.CreateBackupTargetInput{
		Name: request.Name, Kind: request.Kind, Config: request.Config, Enabled: request.Enabled,
		Schedule: request.Schedule, RetentionCount: request.RetentionCount, Metadata: request.Metadata,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	settings, err := h.deps.Backups.DescribeTarget(r.Context(), target)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, settings)
}

func (h *handler) getBackupTarget(w http.ResponseWriter, r *http.Request) {
	target, err := h.deps.Backups.GetTargetSettings(r.Context(), r.PathValue("targetID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, target)
}

func (h *handler) updateBackupTarget(w http.ResponseWriter, r *http.Request) {
	var request updateBackupTargetRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	target, err := h.deps.Backups.UpdateTarget(r.Context(), r.PathValue("targetID"), service.UpdateBackupTargetInput{
		Name: request.Name, Kind: request.Kind, Config: request.Config, Enabled: request.Enabled,
		Schedule: request.Schedule, RetentionCount: request.RetentionCount,
		Metadata: request.Metadata, Version: request.Version,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	settings, err := h.deps.Backups.DescribeTarget(r.Context(), target)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *handler) listBackupRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := h.deps.Backups.ListRuns(r.Context(), r.URL.Query().Get("target_id"), parseListOptions(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs, "count": len(runs)})
}

type queueBackupRunRequest struct {
	TargetID string `json:"target_id"`
}

func (h *handler) queueBackupRun(w http.ResponseWriter, r *http.Request) {
	var request queueBackupRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	run, err := h.deps.Backups.QueueRun(r.Context(), request.TargetID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (h *handler) getBackupRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.deps.Backups.GetRun(r.Context(), r.PathValue("runID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

type startBackupRestoreRequest struct {
	Confirm string `json:"confirm"`
}

func (h *handler) startBackupRestore(w http.ResponseWriter, r *http.Request) {
	if isNilBackupRestoreCoordinator(h.deps.BackupRestores) {
		writeError(w, fmt.Errorf("%w: PostgreSQL backup restore runtime is disabled", domain.ErrNotConfigured))
		return
	}
	var request startBackupRestoreRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if strings.TrimSpace(request.Confirm) != "RESTORE" {
		writeError(w, fmt.Errorf("%w: confirm must equal RESTORE", domain.ErrInvalid))
		return
	}
	operation, err := h.deps.BackupRestores.Start(r.Context(), r.PathValue("runID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, operation)
}

func (h *handler) getBackupRestore(w http.ResponseWriter, r *http.Request) {
	if isNilBackupRestoreCoordinator(h.deps.BackupRestores) {
		writeError(w, fmt.Errorf("%w: PostgreSQL backup restore runtime is disabled", domain.ErrNotConfigured))
		return
	}
	operation, err := h.deps.BackupRestores.Get(r.Context(), r.PathValue("restoreID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

func isNilBackupRestoreCoordinator(coordinator BackupRestoreCoordinator) bool {
	if coordinator == nil {
		return true
	}
	value := reflect.ValueOf(coordinator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type overviewMailbox struct {
	ID                string               `json:"id"`
	ParentID          string               `json:"parent_id,omitempty"`
	Kind              string               `json:"kind"`
	Provider          string               `json:"provider"`
	Address           string               `json:"address"`
	NormalizedAddress string               `json:"normalized_address"`
	DisplayName       string               `json:"display_name,omitempty"`
	Status            domain.MailboxStatus `json:"status"`
	RetrievalKey      overviewRetrievalKey `json:"retrieval_key"`
	Auth              overviewAuth         `json:"auth"`
	Forwarding        *overviewForwarding  `json:"forwarding,omitempty"`
	LastMessageAt     *time.Time           `json:"last_message_at,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	Children          []overviewMailbox    `json:"children"`
}

type overviewRetrievalKey struct {
	Status    string     `json:"status"`
	MaskedKey string     `json:"masked_key,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	IssuedAt  *time.Time `json:"issued_at,omitempty"`
}

type overviewAuth struct {
	Modes                     []string   `json:"modes"`
	AutoRefresh               bool       `json:"auto_refresh"`
	RefreshStatus             string     `json:"refresh_status,omitempty"`
	RefreshTokenValidity      string     `json:"refresh_token_validity,omitempty"`
	GraphAccessTokenExpiresAt *time.Time `json:"graph_access_token_expires_at,omitempty"`
	IMAPAccessTokenExpiresAt  *time.Time `json:"imap_access_token_expires_at,omitempty"`
}

type overviewForwarding struct {
	Target   string `json:"target"`
	Verified bool   `json:"verified"`
}

type overviewBackupDestination struct {
	Provider        domain.BackupTargetKind `json:"provider"`
	Label           string                  `json:"label"`
	Status          string                  `json:"status"`
	LastCompletedAt *time.Time              `json:"last_completed_at,omitempty"`
}

func (h *handler) mailboxOverview(w http.ResponseWriter, r *http.Request) {
	autoRefresh := false
	if h.deps.TokenRefreshSettings != nil {
		settings, err := h.deps.TokenRefreshSettings.Get(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		autoRefresh = settings.Enabled
	}
	mailboxes, err := h.listAllMailboxes(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]overviewMailbox, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		summaries, err := h.deps.Details.Summaries(r.Context(), mailbox.ID, autoRefresh)
		if err != nil {
			writeError(w, err)
			return
		}
		mailboxAuth := overviewCredentialAuth(overviewProvider(mailbox.Provider), summaries)
		lastMessageAt, err := h.overviewLastMessageAt(r.Context(), mailbox.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		aliases, err := h.listAllAliases(r.Context(), mailbox.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		keys, err := h.listAllPickupKeys(r.Context(), mailbox.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		children := make([]overviewMailbox, 0, len(aliases))
		for _, alias := range aliases {
			provider := overviewProvider(alias.Provider)
			aliasAuth := mailboxAuth
			if alias.Kind == domain.AliasKindForward {
				aliasAuth = overviewProviderAuth(provider, false)
			}
			aliasLastMessageAt, err := h.overviewLastMessageAt(r.Context(), alias.ID)
			if err != nil {
				writeError(w, err)
				return
			}
			child := overviewMailbox{
				ID: alias.ID, ParentID: mailbox.ID, Kind: "split", Provider: provider,
				Address: alias.Address, NormalizedAddress: alias.NormalizedAddress,
				Status: mailbox.Status, RetrievalKey: pickupKeyOverview(keys),
				Auth: aliasAuth, LastMessageAt: aliasLastMessageAt, CreatedAt: alias.CreatedAt, Children: []overviewMailbox{},
			}
			if alias.Kind == domain.AliasKindForward {
				child.Forwarding = &overviewForwarding{Target: mailbox.Address, Verified: false}
			}
			children = append(children, child)
		}
		provider := overviewProvider(mailbox.Provider)
		items = append(items, overviewMailbox{
			ID: mailbox.ID, Kind: "primary", Provider: provider, Address: mailbox.Address,
			NormalizedAddress: mailbox.NormalizedAddress, DisplayName: mailbox.DisplayName,
			Status: mailbox.Status, RetrievalKey: pickupKeyOverview(keys),
			Auth: mailboxAuth, LastMessageAt: lastMessageAt, CreatedAt: mailbox.CreatedAt, Children: children,
		})
	}
	targets, err := h.listAllBackupTargets(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	destinations := make([]overviewBackupDestination, 0, len(targets))
	automatic := false
	cadence := ""
	var lastCompleted *time.Time
	for _, target := range targets {
		if target.Enabled && target.Schedule != "" {
			automatic = true
			if cadence == "" {
				cadence = target.Schedule
			}
		}
		status := "pending"
		if !target.Enabled {
			status = "disabled"
		}
		runs, err := h.listAllBackupRuns(r.Context(), target.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		if len(runs) > 0 && status != "disabled" {
			switch runs[0].State {
			case domain.BackupRunSucceeded:
				status = "synced"
			case domain.BackupRunFailed:
				status = "error"
			default:
				status = "pending"
			}
		}
		var targetCompleted *time.Time
		for _, run := range runs {
			if run.State == domain.BackupRunSucceeded && run.FinishedAt != nil && (targetCompleted == nil || run.FinishedAt.After(*targetCompleted)) {
				timeCopy := *run.FinishedAt
				targetCompleted = &timeCopy
			}
		}
		if targetCompleted != nil && (lastCompleted == nil || targetCompleted.After(*lastCompleted)) {
			timeCopy := *targetCompleted
			lastCompleted = &timeCopy
		}
		destinations = append(destinations, overviewBackupDestination{
			Provider: target.Kind, Label: target.Name, Status: status, LastCompletedAt: targetCompleted,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mailboxes": items,
		"backup": map[string]any{
			"automatic": automatic, "schedule": cadence, "last_completed_at": lastCompleted,
			"database_size_bytes": 0, "destinations": destinations,
		},
		"updated_at": time.Now().UTC(),
	})
}

func (h *handler) listAllMailboxes(ctx context.Context) ([]domain.Mailbox, error) {
	var items []domain.Mailbox
	for offset := 0; ; offset += 500 {
		page, err := h.deps.Mailboxes.List(ctx, ports.ListOptions{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if len(page) < 500 {
			return items, nil
		}
	}
}

func (h *handler) listAllAliases(ctx context.Context, mailboxID string) ([]domain.MailboxAlias, error) {
	var items []domain.MailboxAlias
	for offset := 0; ; offset += 500 {
		page, err := h.deps.Mailboxes.ListAliases(ctx, mailboxID, ports.ListOptions{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if len(page) < 500 {
			return items, nil
		}
	}
}

func (h *handler) listAllPickupKeys(ctx context.Context, mailboxID string) ([]domain.MailboxPickupKey, error) {
	var items []domain.MailboxPickupKey
	for offset := 0; ; offset += 500 {
		page, err := h.deps.PickupKeys.List(ctx, mailboxID, ports.ListOptions{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if len(page) < 500 {
			return items, nil
		}
	}
}

func (h *handler) listAllBackupTargets(ctx context.Context) ([]domain.BackupTarget, error) {
	var items []domain.BackupTarget
	for offset := 0; ; offset += 500 {
		page, err := h.deps.Backups.ListTargets(ctx, ports.ListOptions{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if len(page) < 500 {
			return items, nil
		}
	}
}

func (h *handler) listAllBackupRuns(ctx context.Context, targetID string) ([]domain.BackupRun, error) {
	var items []domain.BackupRun
	for offset := 0; ; offset += 500 {
		page, err := h.deps.Backups.ListRuns(ctx, targetID, ports.ListOptions{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if len(page) < 500 {
			return items, nil
		}
	}
}

func (h *handler) queueDefaultBackupRun(w http.ResponseWriter, r *http.Request) {
	targets, err := h.deps.Backups.ListTargets(r.Context(), ports.ListOptions{Limit: 500})
	if err != nil {
		writeError(w, err)
		return
	}
	for _, target := range targets {
		if !target.Enabled {
			continue
		}
		run, err := h.deps.Backups.QueueRun(r.Context(), target.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	writeError(w, fmt.Errorf("%w: no enabled backup target", domain.ErrInvalid))
}

func overviewProvider(provider domain.ProviderKey) string {
	switch provider {
	case domain.ProviderGmail:
		return "google"
	case domain.ProviderCloudflareRoute:
		return "cloudflare"
	default:
		return string(provider)
	}
}

func overviewProviderAuth(provider string, autoRefresh bool) overviewAuth {
	switch provider {
	case "google":
		return overviewAuth{Modes: []string{}, AutoRefresh: autoRefresh, RefreshStatus: "missing", RefreshTokenValidity: "missing"}
	case "cloudflare":
		return overviewAuth{Modes: []string{"forward"}, AutoRefresh: false, RefreshTokenValidity: "not_applicable"}
	default:
		return overviewAuth{Modes: []string{}, AutoRefresh: autoRefresh, RefreshStatus: "missing", RefreshTokenValidity: "missing"}
	}
}

func overviewCredentialAuth(provider string, summaries []service.CredentialSummary) overviewAuth {
	if provider == "cloudflare" {
		return overviewProviderAuth(provider, false)
	}
	result := overviewProviderAuth(provider, false)
	seenModes := make(map[string]struct{})
	for _, summary := range summaries {
		for _, method := range summary.RetrievalMethods {
			mode := ""
			switch method {
			case domain.RetrievalMicrosoftGraph, domain.RetrievalOutlookREST:
				mode = "graph"
			case domain.RetrievalIMAPOAuth, domain.RetrievalIMAPPassword:
				mode = "imap"
			case domain.RetrievalGmailAPI:
				mode = "oauth"
			}
			if mode != "" {
				seenModes[mode] = struct{}{}
			}
		}
		result.AutoRefresh = result.AutoRefresh || summary.AutoRefresh
		if result.RefreshStatus == "missing" || result.RefreshStatus == "" {
			result.RefreshStatus = summary.RefreshStatus
			result.RefreshTokenValidity = summary.RefreshTokenValidity
		}
		if summary.GraphTokenExpiresAt != nil {
			result.GraphAccessTokenExpiresAt = summary.GraphTokenExpiresAt
		}
		if summary.IMAPTokenExpiresAt != nil {
			result.IMAPAccessTokenExpiresAt = summary.IMAPTokenExpiresAt
		}
	}
	for _, mode := range []string{"graph", "oauth", "imap"} {
		if _, exists := seenModes[mode]; exists {
			result.Modes = append(result.Modes, mode)
		}
	}
	return result
}

func (h *handler) overviewLastMessageAt(ctx context.Context, targetID string) (*time.Time, error) {
	if h.deps.MessageCache == nil {
		return nil, nil
	}
	return h.deps.MessageCache.LastMessageAt(ctx, targetID)
}

func pickupKeyOverview(keys []domain.MailboxPickupKey) overviewRetrievalKey {
	result := overviewRetrievalKey{Status: "missing"}
	now := time.Now().UTC()
	for _, key := range keys {
		if key.RevokedAt != nil {
			continue
		}
		status := "ready"
		if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
			status = "expired"
		} else if key.ExpiresAt != nil && key.ExpiresAt.Before(now.Add(24*time.Hour)) {
			status = "expiring"
		}
		issuedAt := key.CreatedAt
		candidate := overviewRetrievalKey{
			Status: status, MaskedKey: key.Prefix + "...", ExpiresAt: key.ExpiresAt, IssuedAt: &issuedAt,
		}
		if status == "ready" {
			return candidate
		}
		if result.Status == "missing" || (result.Status == "expired" && status == "expiring") {
			result = candidate
		}
	}
	return result
}

func parseListOptions(r *http.Request) ports.ListOptions {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	return ports.ListOptions{Limit: limit, Offset: offset}
}

func parseMessageQuery(r *http.Request) (domain.MessageQuery, error) {
	values := r.URL.Query()
	query := domain.MessageQuery{}
	if raw := strings.TrimSpace(values.Get("after")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return domain.MessageQuery{}, fmt.Errorf("%w: after must be RFC3339", domain.ErrInvalid)
		}
		parsed = parsed.UTC()
		query.After = &parsed
	}
	var err error
	if query.Limit, err = parseNonNegativeQueryInt(values.Get("limit"), "limit"); err != nil {
		return domain.MessageQuery{}, err
	}
	if query.PageSize, err = parseNonNegativeQueryInt(values.Get("page_size"), "page_size"); err != nil {
		return domain.MessageQuery{}, err
	}
	if query.MaxPages, err = parseNonNegativeQueryInt(values.Get("max_pages"), "max_pages"); err != nil {
		return domain.MessageQuery{}, err
	}
	if raw := strings.TrimSpace(values.Get("unread")); raw != "" {
		query.Unread, err = strconv.ParseBool(raw)
		if err != nil {
			return domain.MessageQuery{}, fmt.Errorf("%w: unread must be a boolean", domain.ErrInvalid)
		}
	}
	if raw := strings.TrimSpace(values.Get("folder")); raw != "" {
		switch {
		case strings.EqualFold(raw, string(domain.MessageFolderInbox)):
			query.Folder = domain.MessageFolderInbox
		case strings.EqualFold(raw, string(domain.MessageFolderJunk)):
			query.Folder = domain.MessageFolderJunk
		default:
			return domain.MessageQuery{}, fmt.Errorf("%w: folder must be INBOX or Junk", domain.ErrInvalid)
		}
	}
	query.RetrievalMethod = domain.RetrievalMethod(strings.TrimSpace(values.Get("method")))
	return query, nil
}

func parseNonNegativeQueryInt(raw, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: %s must be a non-negative integer", domain.ErrInvalid, name)
	}
	return value, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, fmt.Errorf("%w: invalid JSON: %v", domain.ErrInvalid, err))
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		trailingErr := fmt.Errorf("%w: request body must contain one JSON value", domain.ErrInvalid)
		writeError(w, trailingErr)
		return trailingErr
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	switch {
	case errors.Is(err, domain.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, domain.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, domain.ErrKeyExpired):
		status, code = http.StatusGone, "pickup_key_expired"
	case errors.Is(err, domain.ErrKeyRevoked):
		status, code = http.StatusGone, "pickup_key_revoked"
	case errors.Is(err, domain.ErrNotConfigured):
		status, code = http.StatusNotImplemented, "not_configured"
	case errors.Is(err, domain.ErrUnauthorized):
		status, code = http.StatusUnauthorized, "unauthorized"
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeJSON(w, status, map[string]any{"error": code, "message": err.Error()})
}

func withMiddleware(next http.Handler, logger *slog.Logger, admin *security.AdminAuthenticator) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("http panic", "panic", recovered)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error"})
			}
			logger.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}()
		if admin != nil && admin.Configured() && strings.HasPrefix(r.URL.Path, "/api/v1/") && !isPickupMessagesRequest(r) && !admin.Verify(bearerToken(r.Header.Get("Authorization"))) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "valid bearer token required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPickupMessagesRequest(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/api/v1/pickup/messages"
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

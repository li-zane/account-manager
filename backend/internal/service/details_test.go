package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
)

type detailRefreshSettingsReader struct {
	settings TokenRefreshSettings
}

func (r *detailRefreshSettingsReader) Get(context.Context) (TokenRefreshSettings, error) {
	return r.settings, nil
}

func TestMicrosoftDualCredentialDetailAndReveal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	graphExpiresAt := now.Add(2 * time.Hour)
	imapExpiresAt := now.Add(time.Hour)
	refreshAfter := now.Add(30 * time.Minute)
	mailbox := domain.Mailbox{
		ID: "mbx_microsoft_dual_detail", Provider: domain.ProviderMicrosoft,
		Address: "owner@outlook.com", NormalizedAddress: "owner@outlook.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	credential := domain.MailboxCredential{
		ID: "cred_microsoft_dual_detail", MailboxID: mailbox.ID,
		Kind:      domain.CredentialMicrosoftDualToken,
		ExpiresAt: &imapExpiresAt, RefreshAfter: &refreshAfter, RefreshStatus: "unknown",
		Metadata:  json.RawMessage(`{"retrieval_verification":{"microsoft_graph":{"status":"verified","checked_at":"2026-07-24T11:55:00Z"},"imap_oauth":{"status":"failed","checked_at":"2026-07-24T11:56:00Z"}}}`),
		CreatedAt: now, UpdatedAt: now,
	}
	secret := domain.MicrosoftCredentialSecret{
		SchemaVersion:    domain.MicrosoftCredentialSecretVersion,
		ClientID:         "dual-client-id",
		RefreshToken:     "shared-refresh-secret",
		GraphAccessToken: "graph-access-secret", IMAPAccessToken: "imap-access-secret",
		GraphTokenExpiresAt: &graphExpiresAt, IMAPTokenExpiresAt: &imapExpiresAt,
	}
	service := newCredentialDetailFixture(t, now, mailbox, &credential, secret)
	settings := &detailRefreshSettingsReader{settings: TokenRefreshSettings{Enabled: true, LeadTimeMinutes: 5}}
	service.SetSettingsReader(settings)

	detail, err := service.Get(ctx, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Credentials) != 1 {
		t.Fatalf("credentials = %+v", detail.Credentials)
	}
	summary := detail.Credentials[0]
	if summary.ClientID != "dual-client-id" || detail.ClientID != "dual-client-id" {
		t.Fatalf("client IDs: summary=%q detail=%q", summary.ClientID, detail.ClientID)
	}
	if !sameRetrievalMethods(summary.RetrievalMethods, domain.RetrievalMicrosoftGraph, domain.RetrievalOutlookREST, domain.RetrievalIMAPOAuth) {
		t.Fatalf("retrieval methods = %v", summary.RetrievalMethods)
	}
	if !summary.HasRefreshToken {
		t.Fatalf("refresh token flags = %+v", summary)
	}
	if summary.RefreshTokenValidity != "no_fixed_expiry" {
		t.Fatalf("refresh token validity = %q", summary.RefreshTokenValidity)
	}
	graphCapability := findRetrievalCapability(summary.RetrievalCapabilities, domain.RetrievalMicrosoftGraph)
	restCapability := findRetrievalCapability(summary.RetrievalCapabilities, domain.RetrievalOutlookREST)
	imapCapability := findRetrievalCapability(summary.RetrievalCapabilities, domain.RetrievalIMAPOAuth)
	if graphCapability.Status != "verified" || restCapability.Status != "configured" || imapCapability.Status != "failed" {
		t.Fatalf("retrieval capabilities = %+v", summary.RetrievalCapabilities)
	}
	if graphCapability.AccessTokenExpiresAt == nil || !graphCapability.AccessTokenExpiresAt.Equal(graphExpiresAt) || imapCapability.AccessTokenExpiresAt == nil || !imapCapability.AccessTokenExpiresAt.Equal(imapExpiresAt) {
		t.Fatalf("capability expiries = %+v", summary.RetrievalCapabilities)
	}
	if summary.RefreshToken != maskedCredentialValue {
		t.Fatalf("masked refresh token fields = %+v", summary)
	}
	if summary.ExpiresAt == nil || !summary.ExpiresAt.Equal(imapExpiresAt) || detail.ExpiresAt == nil || !detail.ExpiresAt.Equal(imapExpiresAt) {
		t.Fatalf("aggregate expiry: summary=%v detail=%v", summary.ExpiresAt, detail.ExpiresAt)
	}
	if summary.GraphTokenExpiresAt == nil || !summary.GraphTokenExpiresAt.Equal(graphExpiresAt) || summary.IMAPTokenExpiresAt == nil || !summary.IMAPTokenExpiresAt.Equal(imapExpiresAt) {
		t.Fatalf("per-mode expiry: graph=%v imap=%v", summary.GraphTokenExpiresAt, summary.IMAPTokenExpiresAt)
	}
	if summary.RefreshStatus != "active" {
		t.Fatalf("refresh status = %q", summary.RefreshStatus)
	}
	if !summary.AutoRefresh {
		t.Fatal("refreshable credential did not reflect the enabled server setting")
	}
	settings.settings.Enabled = false
	disabledDetail, err := service.Get(ctx, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabledDetail.Credentials[0].AutoRefresh {
		t.Fatal("credential detail did not reflect the disabled server setting")
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{"shared-refresh-secret", "graph-access-secret", "imap-access-secret"} {
		if strings.Contains(string(encoded), plaintext) {
			t.Fatalf("default detail leaked %q: %s", plaintext, encoded)
		}
	}
	if strings.Contains(string(encoded), "graph_refresh_token") || strings.Contains(string(encoded), "imap_refresh_token") {
		t.Fatalf("detail exposed per-method refresh-token fields: %s", encoded)
	}

	revealed, err := service.Reveal(ctx, mailbox.ID, domain.CredentialMicrosoftDualToken)
	if err != nil {
		t.Fatal(err)
	}
	if revealed.RefreshToken != "shared-refresh-secret" {
		t.Fatalf("revealed tokens = %+v", revealed)
	}
	if revealed.ClientID != "dual-client-id" || !sameRetrievalMethods(revealed.RetrievalMethods, domain.RetrievalMicrosoftGraph, domain.RetrievalOutlookREST, domain.RetrievalIMAPOAuth) {
		t.Fatalf("revealed identity/methods = %+v", revealed)
	}
	if revealed.ExpiresAt == nil || !revealed.ExpiresAt.Equal(imapExpiresAt) || revealed.GraphTokenExpiresAt == nil || !revealed.GraphTokenExpiresAt.Equal(graphExpiresAt) || revealed.IMAPTokenExpiresAt == nil || !revealed.IMAPTokenExpiresAt.Equal(imapExpiresAt) {
		t.Fatalf("revealed expiries = %+v", revealed)
	}
	if !revealed.RevealedUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("revealed_until = %v", revealed.RevealedUntil)
	}
	autoSelected, err := service.Reveal(ctx, mailbox.ID, "")
	if err != nil || autoSelected.RefreshToken != "shared-refresh-secret" {
		t.Fatalf("auto-selected reveal = %+v, err=%v", autoSelected, err)
	}
}

func TestMicrosoftLegacyDualCredentialUsesGenericTokenForBothModes(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	mailbox := domain.Mailbox{
		ID: "mbx_microsoft_legacy_dual", Provider: domain.ProviderMicrosoft,
		Address: "legacy@outlook.com", NormalizedAddress: "legacy@outlook.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	credential := domain.MailboxCredential{
		ID: "cred_microsoft_legacy_dual", MailboxID: mailbox.ID,
		Kind: domain.CredentialMicrosoftDualToken, ClientID: "legacy-client-id",
		ExpiresAt: &expiresAt, RefreshStatus: "unknown", CreatedAt: now, UpdatedAt: now,
	}
	legacy := domain.MailboxCredentialSecret{ClientID: "legacy-client-id", RefreshToken: "legacy-shared-refresh"}
	service := newCredentialDetailFixture(t, now, mailbox, &credential, legacy)

	detail, err := service.Get(context.Background(), mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	summary := detail.Credentials[0]
	if !summary.HasRefreshToken || summary.RefreshToken != maskedCredentialValue {
		t.Fatalf("legacy dual summary = %+v", summary)
	}
	for _, capability := range summary.RetrievalCapabilities {
		if capability.Status != "configured" || capability.AccessTokenExpiresAt != nil {
			t.Fatalf("legacy capability invented an access-token expiry: %+v", capability)
		}
	}
	revealed, err := service.Reveal(context.Background(), mailbox.ID, domain.CredentialMicrosoftDualToken)
	if err != nil {
		t.Fatal(err)
	}
	if revealed.RefreshToken != "legacy-shared-refresh" {
		t.Fatalf("legacy dual reveal = %+v", revealed)
	}
}

func TestMicrosoftDualCredentialReadsLegacyGraphRefreshTokenAsSharedToken(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	mailbox := domain.Mailbox{
		ID: "mbx_microsoft_partial_dual", Provider: domain.ProviderMicrosoft,
		Address: "partial@outlook.com", NormalizedAddress: "partial@outlook.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	credential := domain.MailboxCredential{
		ID: "cred_microsoft_partial_dual", MailboxID: mailbox.ID,
		Kind: domain.CredentialMicrosoftDualToken, ExpiresAt: &expiresAt,
		RefreshStatus: "unknown", CreatedAt: now, UpdatedAt: now,
	}
	secret := domain.MicrosoftCredentialSecret{
		SchemaVersion: domain.MicrosoftCredentialSecretVersion,
		ClientID:      "partial-client", GraphRefreshToken: "graph-only-refresh",
		GraphTokenExpiresAt: &expiresAt,
	}
	service := newCredentialDetailFixture(t, now, mailbox, &credential, secret)
	detail, err := service.Get(context.Background(), mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	summary := detail.Credentials[0]
	if !summary.HasRefreshToken || summary.RefreshToken != maskedCredentialValue || summary.RefreshStatus != "active" {
		t.Fatalf("partial dual summary = %+v", summary)
	}
}

func newCredentialDetailFixture(t *testing.T, now time.Time, mailbox domain.Mailbox, credential *domain.MailboxCredential, secret any) *MailboxDetailService {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "test-v1")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	credential.EncryptedSecret, credential.KeyVersion, err = broker.Seal(ctx, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCredential(ctx, *credential); err != nil {
		t.Fatal(err)
	}
	service, err := NewMailboxDetailService(store, store, broker)
	if err != nil {
		t.Fatal(err)
	}
	service.clock = func() time.Time { return now }
	return service
}

func sameRetrievalMethods(actual []domain.RetrievalMethod, expected ...domain.RetrievalMethod) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func findRetrievalCapability(items []RetrievalCapabilitySummary, method domain.RetrievalMethod) RetrievalCapabilitySummary {
	for _, item := range items {
		if item.Method == method {
			return item
		}
	}
	return RetrievalCapabilitySummary{Method: method, Status: "missing"}
}

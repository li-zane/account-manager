package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/security"
	"github.com/li-zane/account-manager/backend/internal/service"
)

type transferFixture struct {
	store    *memory.Store
	broker   *security.AESGCMBroker
	pickup   *security.PickupKeyService
	transfer *service.ImportExportService
}

func newTransferFixture(t *testing.T) transferFixture {
	t.Helper()
	store := memory.New()
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: providers.MicrosoftAdapter{}, Retriever: providers.MicrosoftAdapter{}},
		ports.ProviderRegistration{Provider: providers.GmailAdapter{}, Retriever: providers.GmailAdapter{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := security.NewAESGCMBroker([]byte("01234567890123456789012345678901"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	formats, err := service.NewFormatService(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := formats.EnsureBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	transfer, err := service.NewImportExportService(store, store, store, registry, broker)
	if err != nil {
		t.Fatal(err)
	}
	pickup, err := security.NewPickupKeyService(store, []byte("abcdefghijklmnopqrstuvwxyz123456"))
	if err != nil {
		t.Fatal(err)
	}
	pickup.SetSecretBroker(broker)
	transfer.SetPickupKeyPreparer(pickup)
	transfer.SetPickupKeyExporter(pickup)
	return transferFixture{store: store, broker: broker, pickup: pickup, transfer: transfer}
}

func TestCustomSixPartImportKeepsMailboxAndPlatformSecretsSeparate(t *testing.T) {
	fixture := newTransferFixture(t)
	ctx := context.Background()
	microsoft := domain.ProviderMicrosoft
	format := domain.MailboxFormat{
		ID: "format_test_platform_six", Name: "Test platform six-part", Kind: domain.MailboxFormatDelimited,
		Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &microsoft, Enabled: true,
		Fields: []domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "platform_password", Target: "platform_account_password", Sensitive: true},
			{Column: "password", Target: "password", Sensitive: true},
			{Column: "client_id", Target: "client_id"},
			{Column: "refresh_token", Target: "refresh_token", Sensitive: true},
			{Column: "access_token", Target: "platform_access_token", Sensitive: true},
		},
		ParserConfig: json.RawMessage(`{"platform":"test-platform"}`),
	}
	if err := fixture.store.CreateMailboxFormat(ctx, format); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.transfer.Import(ctx, service.MailboxImportRequest{
		FormatID: format.ID,
		Data: strings.Join([]string{
			"owner@example.com----gpt-password----mail-password----client-fixture----refresh-fixture----access-fixture",
			"invalid-address----too-few",
		}, "\n"),
		ConflictStrategy: domain.ConflictSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.ValidRows != 1 || result.InvalidRows != 1 || len(result.RowErrors) != 1 {
		t.Fatalf("import result = %+v", result)
	}

	mailbox, err := fixture.store.GetMailboxByIdentity(ctx, domain.ProviderMicrosoft, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := fixture.store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if err != nil {
		t.Fatal(err)
	}
	var mailboxSecret domain.MailboxCredentialSecret
	openJSONSecret(t, fixture.broker, credential.EncryptedSecret, credential.KeyVersion, &mailboxSecret)
	if mailboxSecret.Password != "mail-password" || mailboxSecret.RefreshToken != "refresh-fixture" || credential.ClientID != "client-fixture" {
		t.Fatalf("mailbox credential mapping = %+v, client_id=%q", mailboxSecret, credential.ClientID)
	}
	var microsoftSecret domain.MicrosoftCredentialSecret
	openJSONSecret(t, fixture.broker, credential.EncryptedSecret, credential.KeyVersion, &microsoftSecret)
	if microsoftSecret.SchemaVersion != 0 || microsoftSecret.ClientID != "client-fixture" || microsoftSecret.RefreshToken != "refresh-fixture" {
		t.Fatalf("Microsoft version-zero import envelope = %+v", microsoftSecret)
	}

	accounts, err := fixture.store.ListPlatformAccountsByMailbox(ctx, mailbox.ID, ports.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Platform != "test-platform" {
		t.Fatalf("platform accounts = %+v", accounts)
	}
	platformCredential, err := fixture.store.GetPlatformAccountCredential(ctx, accounts[0].ID, "login")
	if err != nil {
		t.Fatal(err)
	}
	var platformSecret domain.PlatformAccountCredentialSecret
	openJSONSecret(t, fixture.broker, platformCredential.EncryptedSecret, platformCredential.KeyVersion, &platformSecret)
	if platformSecret.Password != "gpt-password" || platformSecret.AccessToken != "access-fixture" {
		t.Fatalf("platform credential mapping = %+v", platformSecret)
	}
}

func TestImportConflictStrategiesAndSensitiveExport(t *testing.T) {
	fixture := newTransferFixture(t)
	ctx := context.Background()
	importData := func(strategy domain.ConflictStrategy, data string) (service.ImportCommitResult, error) {
		return fixture.transfer.Import(ctx, service.MailboxImportRequest{
			FormatID: "fmt_builtin_outlook4", Data: data, ConflictStrategy: strategy,
		})
	}

	if _, err := importData(domain.ConflictSkip, "existing@example.com----old-password----client-old----refresh-old"); err != nil {
		t.Fatal(err)
	}
	mailbox, err := fixture.store.GetMailboxByIdentity(ctx, domain.ProviderMicrosoft, "existing@example.com")
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := importData(domain.ConflictSkip, "existing@example.com----ignored-password----client-new----refresh-ignored")
	if err != nil || skipped.Skipped != 1 {
		t.Fatalf("skip result = %+v, err=%v", skipped, err)
	}
	assertMailboxRefreshToken(t, fixture, mailbox.ID, "refresh-old")

	_, err = importData(domain.ConflictError, strings.Join([]string{
		"new@example.com----new-password----client-new----refresh-new",
		"existing@example.com----replacement-password----client-new----refresh-replacement",
	}, "\n"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if _, lookupErr := fixture.store.GetMailboxByIdentity(ctx, domain.ProviderMicrosoft, "new@example.com"); !errors.Is(lookupErr, domain.ErrNotFound) {
		t.Fatalf("transaction retained a preceding row: %v", lookupErr)
	}

	updated, err := importData(domain.ConflictUpdate, "existing@example.com----updated-password----client-updated----refresh-updated")
	if err != nil || updated.Updated != 1 {
		t.Fatalf("update result = %+v, err=%v", updated, err)
	}
	assertMailboxRefreshToken(t, fixture, mailbox.ID, "refresh-updated")

	plain, err := fixture.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: "fmt_builtin_outlook4", MailboxIDs: []string{mailbox.ID},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Content, "updated-password") || strings.Contains(plain.Content, "refresh-updated") || plain.SensitiveIncluded {
		t.Fatalf("default export exposed a protected field: %q", plain.Content)
	}
	_, err = fixture.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: "fmt_builtin_outlook4", MailboxIDs: []string{mailbox.ID}, IncludeSensitive: true,
	}, false)
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("sensitive export error = %v", err)
	}
	sensitive, err := fixture.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: "fmt_builtin_outlook4", MailboxIDs: []string{mailbox.ID}, IncludeSensitive: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sensitive.SensitiveIncluded || !strings.Contains(sensitive.Content, "updated-password") || !strings.Contains(sensitive.Content, "refresh-updated") {
		t.Fatalf("authorized export = %+v", sensitive)
	}
}

func TestImportedMailboxGetsAutomaticPickupKeyAndUniversalSensitiveExport(t *testing.T) {
	fixture := newTransferFixture(t)
	ctx := context.Background()
	result, err := fixture.transfer.Import(ctx, service.MailboxImportRequest{
		FormatID:         "fmt_builtin_outlook4",
		Data:             "pickup@example.com----mail-password----client-id----shared-refresh-token",
		ConflictStrategy: domain.ConflictSkip,
	})
	if err != nil || result.Created != 1 || len(result.MailboxIDs) != 1 {
		t.Fatalf("import result = %+v, err=%v", result, err)
	}
	keys, err := fixture.pickup.List(ctx, result.MailboxIDs[0], ports.ListOptions{Limit: 10})
	if err != nil || len(keys) != 1 || len(keys[0].EncryptedToken) == 0 {
		t.Fatalf("automatic pickup keys = %+v, err=%v", keys, err)
	}
	redacted, err := fixture.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: "fmt_builtin_pickup2", MailboxIDs: result.MailboxIDs,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if redacted.Content != "pickup@example.com----" || redacted.SensitiveIncluded {
		t.Fatalf("redacted pickup export = %+v", redacted)
	}
	exported, err := fixture.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: "fmt_builtin_pickup2", MailboxIDs: result.MailboxIDs, IncludeSensitive: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(exported.Content, "----")
	if len(parts) != 2 || parts[0] != "pickup@example.com" || !strings.HasPrefix(parts[1], "am_pk_") || !exported.SensitiveIncluded {
		t.Fatalf("sensitive pickup export = %+v", exported)
	}
	if _, err := fixture.pickup.Lookup(ctx, parts[1]); err != nil {
		t.Fatalf("exported pickup key is not usable: %v", err)
	}
}

func TestGmailProviderCredentialImportExportRoundTrip(t *testing.T) {
	t.Run("OAuth", func(t *testing.T) {
		format := gmailOAuthTransferFormat()
		roundTripGmailCredential(t, format,
			strings.Join([]string{"OAuth.User@Gmail.com", string(domain.CredentialGmailOAuth), "gmail-client-id", "gmail-refresh-token"}, "----"),
			"oauth.user@gmail.com", domain.CredentialGmailOAuth,
			[]string{"gmail-refresh-token"}, []string{"gmail-client-id"},
			func(t *testing.T, secret providers.GmailCredentialSecret) {
				t.Helper()
				if secret.ClientID != "gmail-client-id" || secret.RefreshToken != "gmail-refresh-token" || secret.Username != "" || secret.Password != "" {
					t.Fatalf("Gmail OAuth envelope = %+v", secret)
				}
			},
		)
	})

	t.Run("IMAP password", func(t *testing.T) {
		format := gmailIMAPTransferFormat()
		roundTripGmailCredential(t, format,
			strings.Join([]string{"IMAP.User@Gmail.com", string(domain.CredentialIMAPPassword), "gmail-app-password"}, "----"),
			"imap.user@gmail.com", domain.CredentialIMAPPassword,
			[]string{"gmail-app-password"}, []string{"imap.user@gmail.com"},
			func(t *testing.T, secret providers.GmailCredentialSecret) {
				t.Helper()
				if secret.Username != "imap.user@gmail.com" || secret.Password != "gmail-app-password" || secret.ClientID != "" || secret.RefreshToken != "" {
					t.Fatalf("Gmail IMAP envelope = %+v", secret)
				}
			},
		)
	})

	t.Run("IMAP connection fields", func(t *testing.T) {
		format := gmailIMAPConnectionTransferFormat()
		proxyURL := "socks5://proxy-user:proxy-pass@127.0.0.1:1080"
		roundTripGmailCredential(t, format,
			strings.Join([]string{
				"Forward.Target@Gmail.com", string(domain.CredentialIMAPPassword), "x1-login@gmail.com",
				"imap.x1.test", "1993", "false", proxyURL, "Inbox.Custom", "Spam.Custom", "x1-app-password",
			}, "----"),
			"forward.target@gmail.com", domain.CredentialIMAPPassword,
			[]string{"x1-app-password", proxyURL},
			[]string{"x1-login@gmail.com", "imap.x1.test", "1993", "false", "Inbox.Custom", "Spam.Custom"},
			func(t *testing.T, secret providers.GmailCredentialSecret) {
				t.Helper()
				if secret.Username != "x1-login@gmail.com" || secret.Password != "x1-app-password" || secret.Host != "imap.x1.test" || secret.Port != 1993 {
					t.Fatalf("Gmail IMAP connection envelope = %+v", secret)
				}
				if secret.UseTLS == nil || *secret.UseTLS || secret.ProxyURL != proxyURL || secret.InboxFolder != "Inbox.Custom" || secret.JunkFolder != "Spam.Custom" {
					t.Fatalf("Gmail IMAP transport envelope = %+v", secret)
				}
			},
		)
	})
}

func TestGmailIMAPImportRejectsInvalidPortAndBoolean(t *testing.T) {
	for _, test := range []struct {
		name       string
		port       string
		useTLS     string
		wantDetail string
	}{
		{name: "port is not numeric", port: "nine-nine-three", useTLS: "true", wantDetail: "imap_port must be an integer between 1 and 65535"},
		{name: "port exceeds range", port: "65536", useTLS: "true", wantDetail: "imap_port must be an integer between 1 and 65535"},
		{name: "TLS is not boolean", port: "993", useTLS: "sometimes", wantDetail: "use_tls must be a boolean"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTransferFixture(t)
			format := gmailIMAPConnectionTransferFormat()
			if err := fixture.store.CreateMailboxFormat(context.Background(), format); err != nil {
				t.Fatal(err)
			}
			data := strings.Join([]string{
				"invalid@gmail.com", string(domain.CredentialIMAPPassword), "invalid@gmail.com",
				"imap.gmail.com", test.port, test.useTLS, "", "INBOX", "[Gmail]/Spam", "app-password",
			}, "----")
			result, err := fixture.transfer.Import(context.Background(), service.MailboxImportRequest{
				FormatID: format.ID, Data: data, ConflictStrategy: domain.ConflictSkip,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ValidRows != 0 || result.InvalidRows != 1 || len(result.RowErrors) != 1 || !strings.Contains(strings.Join(result.RowErrors[0].Errors, " "), test.wantDetail) {
				t.Fatalf("invalid Gmail IMAP connection result = %+v", result)
			}
		})
	}
}

func TestGmailImportRejectsIncompleteProviderCredential(t *testing.T) {
	tests := []struct {
		name       string
		format     domain.MailboxFormat
		data       string
		wantDetail string
	}{
		{
			name: "OAuth client ID", format: gmailOAuthTransferFormat(),
			data:       strings.Join([]string{"missing-client@gmail.com", string(domain.CredentialGmailOAuth), "", "refresh-token"}, "----"),
			wantDetail: "gmail_oauth requires client_id",
		},
		{
			name: "OAuth refresh token", format: gmailOAuthTransferFormat(),
			data:       strings.Join([]string{"missing-refresh@gmail.com", string(domain.CredentialGmailOAuth), "client-id", ""}, "----"),
			wantDetail: "gmail_oauth requires refresh_token",
		},
		{
			name: "IMAP password", format: gmailIMAPTransferFormat(),
			data:       strings.Join([]string{"missing-password@gmail.com", string(domain.CredentialIMAPPassword), ""}, "----"),
			wantDetail: "imap_password requires password",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTransferFixture(t)
			if err := fixture.store.CreateMailboxFormat(context.Background(), test.format); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.transfer.Import(context.Background(), service.MailboxImportRequest{
				FormatID: test.format.ID, Data: test.data, ConflictStrategy: domain.ConflictSkip,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ValidRows != 0 || result.InvalidRows != 1 || len(result.RowErrors) != 1 || !strings.Contains(strings.Join(result.RowErrors[0].Errors, " "), test.wantDetail) {
				t.Fatalf("incomplete credential result = %+v", result)
			}
		})
	}
}

func roundTripGmailCredential(t *testing.T, format domain.MailboxFormat, input, normalizedAddress string, kind domain.CredentialKind, sensitiveValues, publicValues []string, assertSecret func(*testing.T, providers.GmailCredentialSecret)) {
	t.Helper()
	ctx := context.Background()
	source := newTransferFixture(t)
	if err := source.store.CreateMailboxFormat(ctx, format); err != nil {
		t.Fatal(err)
	}
	result, err := source.transfer.Import(ctx, service.MailboxImportRequest{
		FormatID: format.ID, Data: input, ConflictStrategy: domain.ConflictSkip,
	})
	if err != nil || result.Created != 1 || result.InvalidRows != 0 {
		t.Fatalf("Gmail import result = %+v, err=%v", result, err)
	}
	mailbox, err := source.store.GetMailboxByIdentity(ctx, domain.ProviderGmail, normalizedAddress)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredGmailSecret(t, source, mailbox.ID, kind, assertSecret)

	redacted, err := source.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: format.ID, MailboxIDs: []string{mailbox.ID},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range sensitiveValues {
		if strings.Contains(redacted.Content, sensitive) {
			t.Fatalf("default Gmail export exposed %q: %q", sensitive, redacted.Content)
		}
	}
	for _, public := range publicValues {
		if !strings.Contains(redacted.Content, public) {
			t.Fatalf("default Gmail export omitted public field %q: %q", public, redacted.Content)
		}
	}
	if redacted.SensitiveIncluded {
		t.Fatalf("default Gmail export marked sensitive fields included")
	}

	exported, err := source.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: format.ID, MailboxIDs: []string{mailbox.ID}, IncludeSensitive: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range sensitiveValues {
		if !strings.Contains(exported.Content, sensitive) {
			t.Fatalf("authorized Gmail export omitted %q: %q", sensitive, exported.Content)
		}
	}
	if !exported.SensitiveIncluded {
		t.Fatalf("authorized Gmail export did not report sensitive fields")
	}

	destination := newTransferFixture(t)
	if err := destination.store.CreateMailboxFormat(ctx, format); err != nil {
		t.Fatal(err)
	}
	result, err = destination.transfer.Import(ctx, service.MailboxImportRequest{
		FormatID: format.ID, Data: exported.Content, ConflictStrategy: domain.ConflictSkip,
	})
	if err != nil || result.Created != 1 || result.InvalidRows != 0 {
		t.Fatalf("Gmail re-import result = %+v, err=%v, content=%q", result, err, exported.Content)
	}
	roundTripped, err := destination.store.GetMailboxByIdentity(ctx, domain.ProviderGmail, normalizedAddress)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredGmailSecret(t, destination, roundTripped.ID, kind, assertSecret)
}

func assertStoredGmailSecret(t *testing.T, fixture transferFixture, mailboxID string, kind domain.CredentialKind, assertSecret func(*testing.T, providers.GmailCredentialSecret)) {
	t.Helper()
	credential, err := fixture.store.GetCredential(context.Background(), mailboxID, kind)
	if err != nil {
		t.Fatal(err)
	}
	var secret providers.GmailCredentialSecret
	openJSONSecret(t, fixture.broker, credential.EncryptedSecret, credential.KeyVersion, &secret)
	assertSecret(t, secret)
}

func gmailOAuthTransferFormat() domain.MailboxFormat {
	gmail := domain.ProviderGmail
	return domain.MailboxFormat{
		ID: "fmt_test_gmail_oauth", Name: "Test Gmail OAuth", Kind: domain.MailboxFormatDelimited,
		Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &gmail, Enabled: true,
		Fields: []domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "credential_kind", Target: "credential_kind", Required: true},
			{Column: "client_id", Target: "client_id", Required: true},
			{Column: "refresh_token", Target: "refresh_token", Required: true, Sensitive: true},
		},
	}
}

func gmailIMAPTransferFormat() domain.MailboxFormat {
	gmail := domain.ProviderGmail
	return domain.MailboxFormat{
		ID: "fmt_test_gmail_imap", Name: "Test Gmail IMAP", Kind: domain.MailboxFormatDelimited,
		Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &gmail, Enabled: true,
		Fields: []domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "credential_kind", Target: "credential_kind", Required: true},
			{Column: "password", Target: "password", Required: true, Sensitive: true},
		},
	}
}

func gmailIMAPConnectionTransferFormat() domain.MailboxFormat {
	gmail := domain.ProviderGmail
	return domain.MailboxFormat{
		ID: "fmt_test_gmail_imap_connection", Name: "Test Gmail IMAP connection", Kind: domain.MailboxFormatDelimited,
		Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &gmail, Enabled: true,
		Fields: []domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "credential_kind", Target: "credential_kind", Required: true},
			{Column: "username", Target: "username"},
			{Column: "host", Target: "host"},
			{Column: "port", Target: "port"},
			{Column: "use_tls", Target: "use_tls"},
			{Column: "proxy_url", Target: "proxy_url"},
			{Column: "inbox_folder", Target: "inbox_folder"},
			{Column: "junk_folder", Target: "junk_folder"},
			{Column: "password", Target: "password", Required: true, Sensitive: true},
		},
	}
}

func assertMailboxRefreshToken(t *testing.T, fixture transferFixture, mailboxID, expected string) {
	t.Helper()
	credential, err := fixture.store.GetCredential(context.Background(), mailboxID, domain.CredentialMicrosoftDualToken)
	if err != nil {
		t.Fatal(err)
	}
	var secret domain.MailboxCredentialSecret
	openJSONSecret(t, fixture.broker, credential.EncryptedSecret, credential.KeyVersion, &secret)
	if secret.RefreshToken != expected {
		t.Fatalf("refresh token = %q, want %q", secret.RefreshToken, expected)
	}
}

func openJSONSecret(t *testing.T, broker *security.AESGCMBroker, sealed []byte, keyVersion string, target any) {
	t.Helper()
	plaintext, err := broker.Open(context.Background(), sealed, keyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(plaintext, target); err != nil {
		t.Fatal(err)
	}
}

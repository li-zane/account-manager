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

type templateTransferFixture struct {
	store      *memory.Store
	broker     *security.AESGCMBroker
	pickupKeys *security.PickupKeyService
	transfer   *service.ImportExportService
}

func newTemplateTransferFixture(t *testing.T) templateTransferFixture {
	t.Helper()
	store := memory.New()
	cloudflare := providers.CloudflareRouteAdapter{}
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: providers.MicrosoftAdapter{}, Retriever: providers.MicrosoftAdapter{}},
		ports.ProviderRegistration{Provider: providers.GmailAdapter{}, Retriever: providers.GmailAdapter{}},
		ports.ProviderRegistration{Provider: cloudflare},
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
	pickupKeys, err := security.NewPickupKeyService(store, []byte("template-test-pickup-key-pepper"))
	if err != nil {
		t.Fatal(err)
	}
	transfer.SetPickupKeyPreparer(pickupKeys)
	return templateTransferFixture{store: store, broker: broker, pickupKeys: pickupKeys, transfer: transfer}
}

func TestTemplateSingleLineExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	format := microsoftTemplateFormat(
		"format_template_single",
		"Template single line",
		`mail={{email}}; password={{password_json}}; client={{client_id_json}}; rt={{refresh_token_json}}`,
		[]domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "password", Target: "password", Required: true, Sensitive: true},
			{Column: "client_id", Target: "client_id", Required: true},
			{Column: "refresh_token", Target: "refresh_token", Required: true, Sensitive: true},
		},
	)
	source := newTemplateTransferFixture(t)
	createTemplateFormat(t, source.store, format)
	input := `mail=User@Outlook.com; password="p@ss; \"quoted\""; client="client-one"; rt="refresh-one"`
	created := importTemplate(t, source.transfer, format.ID, input)
	if created.Created != 1 || created.InvalidRows != 0 {
		t.Fatalf("single-line import = %+v", created)
	}

	exported, err := source.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: format.ID, MailboxIDs: created.MailboxIDs, IncludeSensitive: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := `mail=user@outlook.com; password="p@ss; \"quoted\""; client="client-one"; rt="refresh-one"`
	if exported.Content != want || !exported.SensitiveIncluded {
		t.Fatalf("single-line export = %q, sensitive=%v", exported.Content, exported.SensitiveIncluded)
	}

	destination := newTemplateTransferFixture(t)
	createTemplateFormat(t, destination.store, format)
	roundTripped := importTemplate(t, destination.transfer, format.ID, exported.Content)
	if roundTripped.Created != 1 || roundTripped.InvalidRows != 0 {
		t.Fatalf("single-line re-import = %+v", roundTripped)
	}
	mailbox, err := destination.store.GetMailboxByIdentity(ctx, domain.ProviderMicrosoft, "user@outlook.com")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := destination.store.GetCredential(ctx, mailbox.ID, domain.CredentialMicrosoftGraphOAuth)
	if err != nil {
		t.Fatal(err)
	}
	var secret domain.MailboxCredentialSecret
	openJSONSecret(t, destination.broker, credential.EncryptedSecret, credential.KeyVersion, &secret)
	if secret.Password != `p@ss; "quoted"` || secret.RefreshToken != "refresh-one" || credential.ClientID != "client-one" {
		t.Fatalf("round-tripped secret = %+v, client=%q", secret, credential.ClientID)
	}
}

func TestTemplateMultiLineSingleRecordRoundTrip(t *testing.T) {
	ctx := context.Background()
	format := microsoftTemplateFormat(
		"format_template_multiline",
		"Template multiline",
		"Mailbox:\n  address={{email_json}}\n  client={{client_id}}\n  refresh={{refresh_token_json}}",
		[]domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "client_id", Target: "client_id", Required: true},
			{Column: "refresh_token", Target: "refresh_token", Required: true, Sensitive: true},
		},
	)
	source := newTemplateTransferFixture(t)
	createTemplateFormat(t, source.store, format)
	input := "Mailbox:\n  address=\"multi@outlook.com\"\n  client=client-multi\n  refresh=\"refresh-multi\""
	created := importTemplate(t, source.transfer, format.ID, input)
	if created.Created != 1 || created.InvalidRows != 0 {
		t.Fatalf("multiline import = %+v", created)
	}
	exported, err := source.transfer.Export(ctx, service.MailboxExportRequest{
		FormatID: format.ID, MailboxIDs: created.MailboxIDs, IncludeSensitive: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Content != input {
		t.Fatalf("multiline export = %q, want %q", exported.Content, input)
	}
	destination := newTemplateTransferFixture(t)
	createTemplateFormat(t, destination.store, format)
	roundTripped := importTemplate(t, destination.transfer, format.ID, exported.Content)
	if roundTripped.Created != 1 || roundTripped.InvalidRows != 0 {
		t.Fatalf("multiline re-import = %+v", roundTripped)
	}
}

func TestTemplateRepeatBlockRoundTripSupportsQuotedSeparators(t *testing.T) {
	for _, test := range []struct {
		name      string
		attribute string
	}{
		{name: "single quotes", attribute: `sep=',\n'`},
		{name: "double quotes", attribute: `sep=",\n"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			format := domain.MailboxFormat{
				ID:   "format_template_repeat_" + strings.ReplaceAll(test.name, " ", "_"),
				Name: "Template repeat " + test.name, Kind: domain.MailboxFormatTemplate,
				Direction: domain.MailboxFormatBoth, Enabled: true,
				Fields: []domain.MailboxFormatField{
					{Column: "email", Target: "address", Required: true},
					{Column: "provider", Target: "provider", Required: true},
				},
				Template: "{\n  \"mailboxes\": [\n%begin " + test.attribute + "%\n    {\"email\": {{email_json}}, \"provider\": {{provider_json}}}\n%end%\n  ]\n}",
			}
			input := "{\n  \"mailboxes\": [\n    {\"email\": \"one@outlook.com\", \"provider\": \"microsoft\"},\n    {\"email\": \"two@gmail.com\", \"provider\": \"gmail\"}\n  ]\n}"
			source := newTemplateTransferFixture(t)
			createTemplateFormat(t, source.store, format)
			created := importTemplate(t, source.transfer, format.ID, input)
			if created.Created != 2 || created.InvalidRows != 0 {
				t.Fatalf("repeat import = %+v", created)
			}
			exported, err := source.transfer.Export(ctx, service.MailboxExportRequest{
				FormatID: format.ID, MailboxIDs: created.MailboxIDs,
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			if exported.Content != input {
				t.Fatalf("repeat export = %q, want %q", exported.Content, input)
			}
			destination := newTemplateTransferFixture(t)
			createTemplateFormat(t, destination.store, format)
			roundTripped := importTemplate(t, destination.transfer, format.ID, exported.Content)
			if roundTripped.Created != 2 || roundTripped.InvalidRows != 0 {
				t.Fatalf("repeat re-import = %+v", roundTripped)
			}
		})
	}
}

func TestTemplateValidationErrorsAreExplicit(t *testing.T) {
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{name: "unmatched block", template: "%begin%\n{{email}}", want: "unmatched %begin% or %end%"},
		{name: "multiple blocks", template: "%begin%{{email}}%end%\n%begin%{{provider}}%end%", want: "at most one"},
		{name: "unknown variable", template: "mail={{mystery}}", want: "not mapped by format fields"},
		{name: "duplicate target", template: "{{email}}----{{address}}", want: "captured more than once"},
		{name: "variable outside block", template: "mail={{email}}\n%begin%{{provider}}%end%", want: "outside the %begin%"},
		{name: "invalid separator", template: "%begin separator=','%{{email}}%end%", want: "only supports a sep attribute"},
		{name: "unterminated variable", template: "mail={{email}", want: "unterminated variable"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTemplateTransferFixture(t)
			format := domain.MailboxFormat{
				ID: "format_template_invalid_" + string(rune('a'+index)), Name: "Invalid template " + test.name,
				Kind: domain.MailboxFormatTemplate, Direction: domain.MailboxFormatBoth, Enabled: true,
				Fields: []domain.MailboxFormatField{
					{Column: "email", Target: "address", Required: true},
					{Column: "provider", Target: "provider"},
				},
				Template: test.template,
			}
			createTemplateFormat(t, fixture.store, format)
			_, err := fixture.transfer.Preview(context.Background(), service.MailboxImportRequest{
				FormatID: format.ID, Data: "mail@example.com", ConflictStrategy: domain.ConflictSkip,
			})
			if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want ErrInvalid containing %q", err, test.want)
			}
		})
	}
}

func TestTemplateJSONCaptureReportsInvalidValue(t *testing.T) {
	fixture := newTemplateTransferFixture(t)
	format := microsoftTemplateFormat(
		"format_template_invalid_json", "Template invalid JSON", `mail={{email_json}}`,
		[]domain.MailboxFormatField{{Column: "email", Target: "address", Required: true}},
	)
	createTemplateFormat(t, fixture.store, format)
	preview, err := fixture.transfer.Preview(context.Background(), service.MailboxImportRequest{
		FormatID: format.ID, Data: "mail=not-json", ConflictStrategy: domain.ConflictSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.InvalidRows != 1 || len(preview.Rows) != 1 || !strings.Contains(strings.Join(preview.Rows[0].Errors, " "), "not valid JSON") {
		t.Fatalf("invalid JSON preview = %+v", preview)
	}
}

func TestProviderFromAddressAppliesToBuiltinAndTemplateImports(t *testing.T) {
	t.Run("simple builtin", func(t *testing.T) {
		fixture := newTemplateTransferFixture(t)
		data := strings.Join([]string{
			"person@outlook.com----gpt-one----mail-one",
			"person@googlemail.com----gpt-two----mail-two",
		}, "\n")
		preview, err := fixture.transfer.Preview(context.Background(), service.MailboxImportRequest{
			FormatID: "fmt_builtin_simple3", Data: data, ConflictStrategy: domain.ConflictSkip,
		})
		if err != nil {
			t.Fatal(err)
		}
		if preview.ValidRows != 2 || preview.Rows[0].Provider != domain.ProviderMicrosoft || preview.Rows[1].Provider != domain.ProviderGmail {
			t.Fatalf("simple provider preview = %+v", preview)
		}
	})

	t.Run("custom template", func(t *testing.T) {
		fixture := newTemplateTransferFixture(t)
		format := domain.MailboxFormat{
			ID: "format_template_provider_inference", Name: "Template provider inference",
			Kind: domain.MailboxFormatTemplate, Direction: domain.MailboxFormatBoth, Enabled: true,
			Fields: []domain.MailboxFormatField{
				{Column: "email", Target: "address", Required: true},
				{Column: "password", Target: "password", Required: true, Sensitive: true},
			},
			Template:     `{{email}}::{{password_json}}`,
			ParserConfig: json.RawMessage(`{"provider_from_address":true}`),
		}
		createTemplateFormat(t, fixture.store, format)
		preview, err := fixture.transfer.Preview(context.Background(), service.MailboxImportRequest{
			FormatID: format.ID, Data: `template@gmail.com::"app-password"`, ConflictStrategy: domain.ConflictSkip,
		})
		if err != nil {
			t.Fatal(err)
		}
		if preview.ValidRows != 1 || preview.Rows[0].Provider != domain.ProviderGmail {
			t.Fatalf("template provider preview = %+v", preview)
		}
		unknown, err := fixture.transfer.Preview(context.Background(), service.MailboxImportRequest{
			FormatID: format.ID, Data: `template@example.com::"app-password"`, ConflictStrategy: domain.ConflictSkip,
		})
		if err != nil {
			t.Fatal(err)
		}
		if unknown.InvalidRows != 1 || !strings.Contains(strings.Join(unknown.Rows[0].Errors, " "), "set a fixed provider or provider field") {
			t.Fatalf("unknown provider preview = %+v", unknown)
		}
	})
}

func TestCloudflareLegacyPickupKeyImportUsesOneWayPreparer(t *testing.T) {
	ctx := context.Background()
	fixture := newTemplateTransferFixture(t)
	rawPickupKey := "legacy-pickup-key-fixture"
	preview, err := fixture.transfer.Preview(ctx, service.MailboxImportRequest{
		FormatID: "fmt_builtin_cf_routed3",
		Data:     "route@rainynight.me----gpt-password----" + rawPickupKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ValidRows != 1 || !preview.Rows[0].HasPickupKey || preview.Rows[0].HasPassword {
		t.Fatalf("Cloudflare pickup-key preview = %+v", preview)
	}
	result, err := fixture.transfer.Import(ctx, service.MailboxImportRequest{
		FormatID: "fmt_builtin_cf_routed3",
		Data:     "route@rainynight.me----gpt-password----" + rawPickupKey,
	})
	if err != nil || result.Created != 1 {
		t.Fatalf("Cloudflare pickup-key import = %+v, err=%v", result, err)
	}
	mailbox, err := fixture.store.GetMailboxByIdentity(ctx, domain.ProviderCloudflareRoute, "route@rainynight.me")
	if err != nil {
		t.Fatal(err)
	}
	key, err := fixture.pickupKeys.Lookup(ctx, rawPickupKey)
	if err != nil {
		t.Fatal(err)
	}
	if key.MailboxID != mailbox.ID || key.Prefix != "legacy" || string(key.Digest) == rawPickupKey {
		t.Fatalf("prepared pickup key = %+v", key)
	}

	fixture.transfer.SetPickupKeyPreparer(nil)
	_, err = fixture.transfer.Import(ctx, service.MailboxImportRequest{
		FormatID: "fmt_builtin_cf_routed3",
		Data:     "other@rainynight.me----gpt-password----another-legacy-key",
	})
	if !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("missing pickup-key preparer error = %v", err)
	}
}

func microsoftTemplateFormat(id, name, template string, fields []domain.MailboxFormatField) domain.MailboxFormat {
	microsoft := domain.ProviderMicrosoft
	return domain.MailboxFormat{
		ID: id, Name: name, Kind: domain.MailboxFormatTemplate, Direction: domain.MailboxFormatBoth,
		Provider: &microsoft, Fields: fields, Template: template, Enabled: true,
	}
}

func createTemplateFormat(t *testing.T, store *memory.Store, format domain.MailboxFormat) {
	t.Helper()
	if err := store.CreateMailboxFormat(context.Background(), format); err != nil {
		t.Fatal(err)
	}
}

func importTemplate(t *testing.T, transfer *service.ImportExportService, formatID, data string) service.ImportCommitResult {
	t.Helper()
	result, err := transfer.Import(context.Background(), service.MailboxImportRequest{
		FormatID: formatID, Data: data, ConflictStrategy: domain.ConflictSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

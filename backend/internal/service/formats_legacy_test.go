package service_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestFormatServiceEnsuresLegacyThreePartBuiltins(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	formats, err := service.NewFormatService(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := formats.EnsureBuiltins(ctx); err != nil {
		t.Fatal(err)
	}
	if err := formats.EnsureBuiltins(ctx); err != nil {
		t.Fatalf("ensure builtins is not idempotent: %v", err)
	}

	cloudflare, err := formats.Get(ctx, "fmt_builtin_cf_routed3")
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyThreePartFormat(t, cloudflare, []string{"email", "gpt_password", "mail_access_key"}, domain.MailboxFormatImport, "pickup_key")
	if cloudflare.Provider == nil || *cloudflare.Provider != domain.ProviderCloudflareRoute {
		t.Fatalf("cloudflare provider = %v", cloudflare.Provider)
	}
	cloudflareConfig := decodeParserConfig(t, cloudflare.ParserConfig)
	if cloudflareConfig["platform"] != "chatgpt" {
		t.Fatalf("cloudflare parser config = %#v", cloudflareConfig)
	}

	simple, err := formats.Get(ctx, "fmt_builtin_simple3")
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyThreePartFormat(t, simple, []string{"email", "gpt_password", "password"}, domain.MailboxFormatBoth, "password")
	if simple.Provider != nil {
		t.Fatalf("simple format must not force a provider: %v", *simple.Provider)
	}
	simpleConfig := decodeParserConfig(t, simple.ParserConfig)
	if simpleConfig["platform"] != "chatgpt" || simpleConfig["provider_from_address"] != true {
		t.Fatalf("simple parser config = %#v", simpleConfig)
	}
}

func assertLegacyThreePartFormat(t *testing.T, format domain.MailboxFormat, columns []string, direction domain.MailboxFormatDirection, secretTarget string) {
	t.Helper()
	if !format.Builtin || !format.Enabled || format.Kind != domain.MailboxFormatDelimited || format.Direction != direction || format.Delimiter != "----" {
		t.Fatalf("format envelope = %+v", format)
	}
	if len(format.Fields) != len(columns) {
		t.Fatalf("fields = %+v", format.Fields)
	}
	for index, field := range format.Fields {
		if field.Column != columns[index] || !field.Required {
			t.Fatalf("field %d = %+v", index, field)
		}
	}
	if format.Fields[0].Target != "address" || format.Fields[0].Sensitive {
		t.Fatalf("email field = %+v", format.Fields[0])
	}
	if format.Fields[1].Target != "platform_account_password" || !format.Fields[1].Sensitive {
		t.Fatalf("GPT password field = %+v", format.Fields[1])
	}
	if format.Fields[2].Target != secretTarget || !format.Fields[2].Sensitive {
		t.Fatalf("mail secret field = %+v", format.Fields[2])
	}
}

func decodeParserConfig(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode parser config: %v", err)
	}
	return config
}

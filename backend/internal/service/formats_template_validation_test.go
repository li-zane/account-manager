package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestFormatServiceValidatesImportTemplateWhenSaved(t *testing.T) {
	registry, err := providers.NewRegistry(ports.ProviderRegistration{Provider: providers.MicrosoftAdapter{}})
	if err != nil {
		t.Fatal(err)
	}
	formats, err := service.NewFormatService(memory.New(), registry)
	if err != nil {
		t.Fatal(err)
	}
	microsoft := domain.ProviderMicrosoft
	base := service.SaveMailboxFormatInput{
		Name: "custom template", Kind: domain.MailboxFormatTemplate, Direction: domain.MailboxFormatBoth,
		Provider: &microsoft, Template: "{{email}}::{{password}}",
		Fields: []domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "password", Target: "password", Sensitive: true},
		},
	}
	if _, err := formats.Create(context.Background(), base); err != nil {
		t.Fatalf("create valid template: %v", err)
	}
	base.Name = "invalid custom template"
	base.Template = "{{email}}::{{unknown}}"
	if _, err := formats.Create(context.Background(), base); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("create invalid template error = %v, want invalid", err)
	}
}

func TestFormatServiceAllowsExplicitProviderInference(t *testing.T) {
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: providers.MicrosoftAdapter{}},
		ports.ProviderRegistration{Provider: providers.GmailAdapter{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	formats, err := service.NewFormatService(memory.New(), registry)
	if err != nil {
		t.Fatal(err)
	}
	input := service.SaveMailboxFormatInput{
		Name: "address inferred", Kind: domain.MailboxFormatDelimited, Direction: domain.MailboxFormatImport,
		Delimiter: "----", ParserConfig: json.RawMessage(`{"provider_from_address":true}`),
		Fields: []domain.MailboxFormatField{{Column: "email", Target: "address", Required: true}},
	}
	if _, err := formats.Create(context.Background(), input); err != nil {
		t.Fatalf("create inferred-provider format: %v", err)
	}
	input.Name = "missing provider"
	input.ParserConfig = nil
	if _, err := formats.Create(context.Background(), input); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("create format without provider error = %v, want invalid", err)
	}
}

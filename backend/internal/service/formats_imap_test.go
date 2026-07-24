package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestFormatServiceCanonicalizesGmailIMAPTargetsAndProtectsProxy(t *testing.T) {
	store := memory.New()
	registry, err := providers.NewRegistry(ports.ProviderRegistration{
		Provider: providers.GmailAdapter{}, Retriever: providers.GmailAdapter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	formats, err := service.NewFormatService(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	gmail := domain.ProviderGmail
	created, err := formats.Create(context.Background(), service.SaveMailboxFormatInput{
		Name: "Gmail IMAP transport", Kind: domain.MailboxFormatDelimited,
		Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &gmail,
		Fields: []domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "server", Target: "host"},
			{Column: "server_port", Target: "port"},
			{Column: "proxy", Target: "proxy_url"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Fields[1].Target != "imap_host" || created.Fields[2].Target != "imap_port" {
		t.Fatalf("canonical IMAP targets = %+v", created.Fields)
	}
	if !created.Fields[3].Sensitive {
		t.Fatalf("proxy_url was not forced sensitive: %+v", created.Fields[3])
	}

	_, err = formats.Create(context.Background(), service.SaveMailboxFormatInput{
		Name: "Duplicate Gmail IMAP host", Kind: domain.MailboxFormatDelimited,
		Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &gmail,
		Fields: []domain.MailboxFormatField{
			{Column: "email", Target: "address", Required: true},
			{Column: "host_a", Target: "host"},
			{Column: "host_b", Target: "imap_host"},
		},
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("alias-equivalent duplicate target error = %v", err)
	}
}

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
	"github.com/li-zane/account-manager/backend/internal/service"
)

type aliasProvisionTestProvider struct {
	key          domain.ProviderKey
	capabilities domain.ProviderCapabilities
	result       domain.ProvisionMailboxResult
	err          error
	calls        []domain.ProvisionMailboxRequest
}

func (p *aliasProvisionTestProvider) Descriptor(context.Context) domain.ProviderDescriptor {
	return domain.ProviderDescriptor{Key: p.key, Capabilities: p.capabilities}
}

func (p *aliasProvisionTestProvider) NormalizeAddress(address string) (string, error) {
	return strings.ToLower(strings.TrimSpace(address)), nil
}

func (p *aliasProvisionTestProvider) Provision(_ context.Context, request domain.ProvisionMailboxRequest) (domain.ProvisionMailboxResult, error) {
	p.calls = append(p.calls, request)
	return p.result, p.err
}

func TestCreateForwardAliasProvisionsProviderBeforeSaving(t *testing.T) {
	ctx := context.Background()
	routeProvider := &aliasProvisionTestProvider{
		key: domain.ProviderCloudflareRoute,
		capabilities: domain.ProviderCapabilities{
			ProvisionMailbox: true,
			ManageAliases:    true,
		},
		result: domain.ProvisionMailboxResult{
			ExternalReference: "rule-123",
			Metadata:          json.RawMessage(`{"zone":"example.net","managed":true,"destination_address":"stale@example.net"}`),
		},
	}
	mailboxes, store := newAliasProvisionTestService(t, routeProvider)
	mailbox, err := mailboxes.Create(ctx, service.CreateMailboxInput{
		Provider: domain.ProviderGmail,
		Address:  "Owner@Gmail.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	alias, err := mailboxes.CreateAlias(ctx, mailbox.ID, service.CreateAliasInput{
		Provider: domain.ProviderCloudflareRoute,
		Address:  "Login@Example.net",
		Kind:     domain.AliasKindForward,
		Metadata: json.RawMessage(`{"label":"primary route"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(routeProvider.calls) != 1 {
		t.Fatalf("provision calls = %d, want 1", len(routeProvider.calls))
	}
	request := routeProvider.calls[0]
	if request.Address != "login@example.net" {
		t.Fatalf("provision address = %q", request.Address)
	}
	var requestMetadata map[string]string
	if err := json.Unmarshal(request.Metadata, &requestMetadata); err != nil {
		t.Fatal(err)
	}
	if requestMetadata["destination_mailbox_id"] != mailbox.ID || requestMetadata["destination_address"] != "owner@gmail.com" {
		t.Fatalf("provision metadata = %+v", requestMetadata)
	}

	var metadata map[string]any
	if err := json.Unmarshal(alias.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["external_reference"] != "rule-123" || metadata["zone"] != "example.net" || metadata["managed"] != true || metadata["label"] != "primary route" {
		t.Fatalf("alias metadata = %+v", metadata)
	}
	if metadata["destination_mailbox_id"] != mailbox.ID || metadata["destination_address"] != "owner@gmail.com" {
		t.Fatalf("alias destination metadata = %+v", metadata)
	}
	saved, err := store.GetAlias(ctx, alias.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved.Metadata) != string(alias.Metadata) {
		t.Fatalf("saved metadata = %s, returned metadata = %s", saved.Metadata, alias.Metadata)
	}
}

func TestCreateForwardAliasProvisionFailureDoesNotSaveAlias(t *testing.T) {
	ctx := context.Background()
	provisionErr := errors.New("route conflict")
	routeProvider := &aliasProvisionTestProvider{
		key: domain.ProviderCloudflareRoute,
		capabilities: domain.ProviderCapabilities{
			ProvisionMailbox: true,
			ManageAliases:    true,
		},
		err: provisionErr,
	}
	mailboxes, store := newAliasProvisionTestService(t, routeProvider)
	mailbox, err := mailboxes.Create(ctx, service.CreateMailboxInput{Provider: domain.ProviderGmail, Address: "owner@gmail.com"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = mailboxes.CreateAlias(ctx, mailbox.ID, service.CreateAliasInput{
		Provider: domain.ProviderCloudflareRoute,
		Address:  "login@example.net",
		Kind:     domain.AliasKindForward,
	})
	if !errors.Is(err, provisionErr) {
		t.Fatalf("create alias error = %v, want provision error", err)
	}
	if len(routeProvider.calls) != 1 {
		t.Fatalf("provision calls = %d, want 1", len(routeProvider.calls))
	}
	aliases, listErr := store.ListAliases(ctx, mailbox.ID, ports.ListOptions{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(aliases) != 0 {
		t.Fatalf("saved aliases = %+v, want none", aliases)
	}
}

func TestCreateOrdinaryAliasDoesNotProvisionProvider(t *testing.T) {
	tests := []struct {
		name         string
		provider     domain.ProviderKey
		kind         domain.AliasKind
		capabilities domain.ProviderCapabilities
	}{
		{
			name:     "split alias on capable provider",
			provider: domain.ProviderCloudflareRoute,
			kind:     domain.AliasKindSplit,
			capabilities: domain.ProviderCapabilities{
				ProvisionMailbox: true,
				ManageAliases:    true,
			},
		},
		{name: "forward alias on local provider", provider: domain.ProviderMicrosoft, kind: domain.AliasKindForward},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			aliasProvider := &aliasProvisionTestProvider{key: tt.provider, capabilities: tt.capabilities}
			mailboxes, store := newAliasProvisionTestService(t, aliasProvider)
			mailbox, err := mailboxes.Create(ctx, service.CreateMailboxInput{Provider: domain.ProviderGmail, Address: "owner@gmail.com"})
			if err != nil {
				t.Fatal(err)
			}
			alias, err := mailboxes.CreateAlias(ctx, mailbox.ID, service.CreateAliasInput{
				Provider: tt.provider,
				Address:  "alias@example.net",
				Kind:     tt.kind,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(aliasProvider.calls) != 0 {
				t.Fatalf("provision calls = %d, want 0", len(aliasProvider.calls))
			}
			if _, err := store.GetAlias(ctx, alias.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func newAliasProvisionTestService(t *testing.T, aliasProvider ports.MailboxProvider) (*service.MailboxService, *memory.Store) {
	t.Helper()
	destinationProvider := &aliasProvisionTestProvider{key: domain.ProviderGmail}
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: destinationProvider},
		ports.ProviderRegistration{Provider: aliasProvider},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	mailboxes, err := service.NewMailboxService(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	return mailboxes, store
}

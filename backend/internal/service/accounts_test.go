package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/providers"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
	"github.com/li-zane/account-manager/backend/internal/service"
)

func TestPlatformAccountsRouteThroughMailboxID(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	registry, err := providers.NewRegistry(
		ports.ProviderRegistration{Provider: providers.GmailAdapter{}, Retriever: providers.GmailAdapter{}},
		ports.ProviderRegistration{Provider: providers.CloudflareRouteAdapter{}, Retriever: providers.CloudflareRouteAdapter{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	mailboxes, err := service.NewMailboxService(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := service.NewAccountService(store, store)
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := mailboxes.Create(ctx, service.CreateMailboxInput{Provider: domain.ProviderGmail, Address: "Owner@Gmail.com"})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := mailboxes.CreateAlias(ctx, mailbox.ID, service.CreateAliasInput{
		Provider: domain.ProviderCloudflareRoute, Address: "chat@example.net", Kind: domain.AliasKindSplit,
	})
	if err != nil {
		t.Fatal(err)
	}
	chatGPT, err := accounts.Create(ctx, service.CreateAccountInput{
		Platform: "chatgpt", ExternalReference: "chatgpt-user-1", MailboxID: mailbox.ID,
		MailboxAliasID: &alias.ID, LoginAddress: alias.Address,
	})
	if err != nil {
		t.Fatal(err)
	}
	grok, err := accounts.Create(ctx, service.CreateAccountInput{
		Platform: "grok", ExternalReference: "grok-user-1", MailboxID: mailbox.ID,
		LoginAddress: mailbox.Address,
	})
	if err != nil {
		t.Fatal(err)
	}
	if chatGPT.ID == grok.ID {
		t.Fatal("account IDs must be scoped by platform")
	}
	routed, err := accounts.ResolveMailbox(ctx, chatGPT.ID)
	if err != nil {
		t.Fatal(err)
	}
	if routed.Mailbox.ID != mailbox.ID || routed.Mailbox.Provider != domain.ProviderGmail {
		t.Fatalf("routed mailbox = %+v", routed.Mailbox)
	}
	if routed.Alias == nil || routed.Alias.ID != alias.ID {
		t.Fatalf("routed alias = %+v", routed.Alias)
	}
	linked, err := store.ListPlatformAccountsByMailbox(ctx, mailbox.ID, ports.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 2 {
		t.Fatalf("accounts linked to one mailbox = %d, want 2", len(linked))
	}
}

func TestPlatformAccountMayExistWithoutMailboxRoute(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	accounts, err := service.NewAccountService(store, store)
	if err != nil {
		t.Fatal(err)
	}

	account, err := accounts.Create(ctx, service.CreateAccountInput{Platform: "chatgpt", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if account.MailboxID != "" || account.ExternalReference != "" {
		t.Fatalf("unrouted account = %+v", account)
	}
	if !strings.HasPrefix(account.ID, "acct_chatgpt_") {
		t.Fatalf("account id = %q", account.ID)
	}
	if _, err := accounts.ResolveMailbox(ctx, account.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("resolve unrouted account error = %v, want not found", err)
	}
	items, err := store.ListPlatformAccounts(ctx, "chatgpt", ports.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != account.ID {
		t.Fatalf("listed accounts = %+v", items)
	}
}

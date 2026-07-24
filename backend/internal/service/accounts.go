package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type AccountService struct {
	accounts  ports.PlatformAccountRepository
	mailboxes ports.MailboxRepository
	clock     func() time.Time
}

func NewAccountService(accounts ports.PlatformAccountRepository, mailboxes ports.MailboxRepository) (*AccountService, error) {
	if accounts == nil || mailboxes == nil {
		return nil, fmt.Errorf("%w: account and mailbox repositories are required", domain.ErrInvalid)
	}
	return &AccountService{accounts: accounts, mailboxes: mailboxes, clock: time.Now}, nil
}

type CreateAccountInput struct {
	Platform          string
	ExternalReference string
	MailboxID         string
	MailboxAliasID    *string
	LoginAddress      string
	Status            string
	Metadata          json.RawMessage
}

func (s *AccountService) Create(ctx context.Context, input CreateAccountInput) (domain.PlatformAccount, error) {
	platform := strings.ToLower(strings.TrimSpace(input.Platform))
	if _, err := domain.NormalizeNamespace(platform); err != nil {
		return domain.PlatformAccount{}, err
	}
	mailboxID := strings.TrimSpace(input.MailboxID)
	loginAddress := strings.TrimSpace(input.LoginAddress)
	var mailbox domain.Mailbox
	var err error
	if mailboxID != "" {
		mailbox, err = s.mailboxes.GetMailbox(ctx, mailboxID)
		if err != nil {
			return domain.PlatformAccount{}, err
		}
	} else if input.MailboxAliasID != nil {
		return domain.PlatformAccount{}, fmt.Errorf("%w: mailbox id is required when alias is set", domain.ErrInvalid)
	}
	if input.MailboxAliasID != nil {
		alias, err := s.mailboxes.GetAlias(ctx, *input.MailboxAliasID)
		if err != nil {
			return domain.PlatformAccount{}, err
		}
		if alias.MailboxID != mailboxID {
			return domain.PlatformAccount{}, fmt.Errorf("%w: alias does not belong to mailbox", domain.ErrInvalid)
		}
		if loginAddress == "" {
			loginAddress = alias.Address
		}
	} else if mailboxID != "" && loginAddress == "" {
		loginAddress = mailbox.Address
	}
	externalReference := strings.TrimSpace(input.ExternalReference)
	var id string
	if externalReference == "" {
		id, err = domain.NewRandomID("acct_" + platform)
	} else {
		id, err = domain.NewPlatformAccountID(platform, externalReference)
	}
	if err != nil {
		return domain.PlatformAccount{}, err
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "active"
	}
	now := s.clock().UTC()
	account := domain.PlatformAccount{
		ID:                id,
		Platform:          platform,
		ExternalReference: externalReference,
		MailboxID:         mailboxID,
		MailboxAliasID:    input.MailboxAliasID,
		LoginAddress:      loginAddress,
		Status:            status,
		Metadata:          normalizedJSON(input.Metadata),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.accounts.CreatePlatformAccount(ctx, account); err != nil {
		return domain.PlatformAccount{}, err
	}
	return account, nil
}

func (s *AccountService) List(ctx context.Context, platform string, options ports.ListOptions) ([]domain.PlatformAccount, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != "" {
		if _, err := domain.NormalizeNamespace(platform); err != nil {
			return nil, err
		}
	}
	return s.accounts.ListPlatformAccounts(ctx, platform, options)
}

type RoutedAccount struct {
	Account domain.PlatformAccount `json:"account"`
	Mailbox domain.Mailbox         `json:"mailbox"`
	Alias   *domain.MailboxAlias   `json:"alias,omitempty"`
}

// ResolveMailbox demonstrates the routing boundary: platform accounts carry
// a mailbox ID, and the mailbox repository is the only lookup needed to route
// a request. No provider-native address is used as a relational key.
func (s *AccountService) ResolveMailbox(ctx context.Context, accountID string) (RoutedAccount, error) {
	account, err := s.accounts.GetPlatformAccount(ctx, accountID)
	if err != nil {
		return RoutedAccount{}, err
	}
	if account.MailboxID == "" {
		return RoutedAccount{}, fmt.Errorf("%w: platform account %q has no mailbox route", domain.ErrNotFound, accountID)
	}
	mailbox, err := s.mailboxes.GetMailbox(ctx, account.MailboxID)
	if err != nil {
		return RoutedAccount{}, err
	}
	var alias *domain.MailboxAlias
	if account.MailboxAliasID != nil {
		value, err := s.mailboxes.GetAlias(ctx, *account.MailboxAliasID)
		if err != nil {
			return RoutedAccount{}, err
		}
		alias = &value
	}
	return RoutedAccount{Account: account, Mailbox: mailbox, Alias: alias}, nil
}

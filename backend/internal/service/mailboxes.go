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

type MailboxService struct {
	repository ports.MailboxRepository
	providers  ports.ProviderRegistry
	pickupKeys PickupKeyEnsurer
	clock      func() time.Time
}

type PickupKeyEnsurer interface {
	Ensure(context.Context, string) (domain.MailboxPickupKey, error)
}

func NewMailboxService(repository ports.MailboxRepository, providers ports.ProviderRegistry) (*MailboxService, error) {
	if repository == nil || providers == nil {
		return nil, fmt.Errorf("%w: mailbox repository and provider registry are required", domain.ErrInvalid)
	}
	return &MailboxService{repository: repository, providers: providers, clock: time.Now}, nil
}

func (s *MailboxService) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

func (s *MailboxService) SetPickupKeyEnsurer(ensurer PickupKeyEnsurer) {
	s.pickupKeys = ensurer
}

type CreateMailboxInput struct {
	Provider          domain.ProviderKey
	Address           string
	DisplayName       string
	ExternalReference string
	Metadata          json.RawMessage
}

func (s *MailboxService) Create(ctx context.Context, input CreateMailboxInput) (domain.Mailbox, error) {
	registration, err := s.providers.Get(input.Provider)
	if err != nil {
		return domain.Mailbox{}, err
	}
	normalized, err := registration.Provider.NormalizeAddress(input.Address)
	if err != nil {
		return domain.Mailbox{}, err
	}
	id, err := domain.NewMailboxID(string(input.Provider), normalized)
	if err != nil {
		return domain.Mailbox{}, err
	}
	now := s.clock().UTC()
	mailbox := domain.Mailbox{
		ID:                id,
		Provider:          input.Provider,
		Address:           normalized,
		NormalizedAddress: normalized,
		DisplayName:       strings.TrimSpace(input.DisplayName),
		ExternalReference: strings.TrimSpace(input.ExternalReference),
		Status:            domain.MailboxStatusActive,
		Metadata:          normalizedJSON(input.Metadata),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.repository.CreateMailbox(ctx, mailbox); err != nil {
		return domain.Mailbox{}, err
	}
	if s.pickupKeys != nil {
		if _, err := s.pickupKeys.Ensure(ctx, mailbox.ID); err != nil {
			return domain.Mailbox{}, fmt.Errorf("ensure mailbox pickup key: %w", err)
		}
	}
	return mailbox, nil
}

func (s *MailboxService) Get(ctx context.Context, id string) (domain.Mailbox, error) {
	return s.repository.GetMailbox(ctx, id)
}

func (s *MailboxService) List(ctx context.Context, options ports.ListOptions) ([]domain.Mailbox, error) {
	return s.repository.ListMailboxes(ctx, options)
}

type MailboxCounts struct {
	MainMailboxes int64 `json:"main_mailboxes"`
	Aliases       int64 `json:"aliases"`
}

func (s *MailboxService) Counts(ctx context.Context) (MailboxCounts, error) {
	mailboxes, err := s.repository.CountMailboxes(ctx)
	if err != nil {
		return MailboxCounts{}, err
	}
	aliases, err := s.repository.CountAliases(ctx, "")
	if err != nil {
		return MailboxCounts{}, err
	}
	return MailboxCounts{MainMailboxes: mailboxes, Aliases: aliases}, nil
}

func (s *MailboxService) AliasCount(ctx context.Context, mailboxID string) (int64, error) {
	return s.repository.CountAliases(ctx, mailboxID)
}

type CreateAliasInput struct {
	Provider domain.ProviderKey
	Address  string
	Kind     domain.AliasKind
	Enabled  *bool
	Metadata json.RawMessage
}

func (s *MailboxService) CreateAlias(ctx context.Context, mailboxID string, input CreateAliasInput) (domain.MailboxAlias, error) {
	mailbox, err := s.repository.GetMailbox(ctx, mailboxID)
	if err != nil {
		return domain.MailboxAlias{}, err
	}
	providerKey := input.Provider
	if providerKey == "" {
		providerKey = mailbox.Provider
	}
	registration, err := s.providers.Get(providerKey)
	if err != nil {
		return domain.MailboxAlias{}, err
	}
	normalized, err := registration.Provider.NormalizeAddress(input.Address)
	if err != nil {
		return domain.MailboxAlias{}, err
	}
	if input.Kind == "" {
		input.Kind = domain.AliasKindSplit
	}
	if input.Kind != domain.AliasKindSplit && input.Kind != domain.AliasKindForward {
		return domain.MailboxAlias{}, fmt.Errorf("%w: alias kind %q", domain.ErrInvalid, input.Kind)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	id, err := domain.NewRandomID("alias")
	if err != nil {
		return domain.MailboxAlias{}, err
	}
	now := s.clock().UTC()
	alias := domain.MailboxAlias{
		ID:                id,
		MailboxID:         mailboxID,
		Provider:          providerKey,
		Address:           normalized,
		NormalizedAddress: normalized,
		Kind:              input.Kind,
		Enabled:           enabled,
		Metadata:          normalizedJSON(input.Metadata),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	descriptor := registration.Provider.Descriptor(ctx)
	if input.Kind == domain.AliasKindForward && descriptor.Capabilities.ProvisionMailbox && descriptor.Capabilities.ManageAliases {
		requestMetadata, err := json.Marshal(map[string]string{
			"destination_mailbox_id": mailbox.ID,
			"destination_address":    mailbox.NormalizedAddress,
		})
		if err != nil {
			return domain.MailboxAlias{}, fmt.Errorf("encode alias provisioning metadata: %w", err)
		}
		result, err := registration.Provider.Provision(ctx, domain.ProvisionMailboxRequest{
			Address:  normalized,
			Metadata: requestMetadata,
		})
		if err != nil {
			return domain.MailboxAlias{}, err
		}
		alias.Metadata, err = provisionedAliasMetadata(alias.Metadata, requestMetadata, result)
		if err != nil {
			return domain.MailboxAlias{}, err
		}
	}
	if err := s.repository.CreateAlias(ctx, alias); err != nil {
		return domain.MailboxAlias{}, err
	}
	return alias, nil
}

func provisionedAliasMetadata(input, request json.RawMessage, result domain.ProvisionMailboxResult) (json.RawMessage, error) {
	metadata := make(map[string]json.RawMessage)
	// Provider metadata may add route details, but the service-owned destination
	// fields must continue to point at the parent mailbox used for provisioning.
	for _, value := range []json.RawMessage{input, result.Metadata, request} {
		if len(value) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(value, &fields); err != nil {
			return nil, fmt.Errorf("decode alias provisioning metadata: %w", err)
		}
		for key, field := range fields {
			metadata[key] = append(json.RawMessage(nil), field...)
		}
	}
	if externalReference := strings.TrimSpace(result.ExternalReference); externalReference != "" {
		encoded, err := json.Marshal(externalReference)
		if err != nil {
			return nil, fmt.Errorf("encode alias external reference: %w", err)
		}
		metadata["external_reference"] = encoded
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode provisioned alias metadata: %w", err)
	}
	return encoded, nil
}

func (s *MailboxService) ListAliases(ctx context.Context, mailboxID string, options ports.ListOptions) ([]domain.MailboxAlias, error) {
	if _, err := s.repository.GetMailbox(ctx, mailboxID); err != nil {
		return nil, err
	}
	return s.repository.ListAliases(ctx, mailboxID, options)
}

func normalizedJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), value...)
}

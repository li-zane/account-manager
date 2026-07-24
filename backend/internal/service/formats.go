package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

type FormatService struct {
	repository ports.MailboxFormatRepository
	providers  ports.ProviderRegistry
	clock      func() time.Time
}

func NewFormatService(repository ports.MailboxFormatRepository, providers ports.ProviderRegistry) (*FormatService, error) {
	if repository == nil || providers == nil {
		return nil, fmt.Errorf("%w: format repository and provider registry are required", domain.ErrInvalid)
	}
	return &FormatService{repository: repository, providers: providers, clock: time.Now}, nil
}

type SaveMailboxFormatInput struct {
	Name         string
	Kind         domain.MailboxFormatKind
	Direction    domain.MailboxFormatDirection
	Delimiter    string
	Fields       []domain.MailboxFormatField
	Provider     *domain.ProviderKey
	HasHeader    bool
	Template     string
	ParserConfig json.RawMessage
	Enabled      *bool
	Version      int64
}

func (s *FormatService) Create(ctx context.Context, input SaveMailboxFormatInput) (domain.MailboxFormat, error) {
	format, err := s.build(ctx, input)
	if err != nil {
		return domain.MailboxFormat{}, err
	}
	id, err := domain.NewRandomID("format")
	if err != nil {
		return domain.MailboxFormat{}, err
	}
	now := s.clock().UTC()
	format.ID, format.Version, format.CreatedAt, format.UpdatedAt = id, 1, now, now
	if err := s.repository.CreateMailboxFormat(ctx, format); err != nil {
		return domain.MailboxFormat{}, err
	}
	return format, nil
}

func (s *FormatService) Update(ctx context.Context, id string, input SaveMailboxFormatInput) (domain.MailboxFormat, error) {
	current, err := s.repository.GetMailboxFormat(ctx, id)
	if err != nil {
		return domain.MailboxFormat{}, err
	}
	format, err := s.build(ctx, input)
	if err != nil {
		return domain.MailboxFormat{}, err
	}
	expectedVersion := input.Version
	if expectedVersion == 0 {
		expectedVersion = current.Version
	}
	format.ID = current.ID
	format.Builtin = current.Builtin
	format.Version = expectedVersion + 1
	format.CreatedAt = current.CreatedAt
	format.UpdatedAt = s.clock().UTC()
	if err := s.repository.UpdateMailboxFormat(ctx, format, expectedVersion); err != nil {
		return domain.MailboxFormat{}, err
	}
	return format, nil
}

func (s *FormatService) Get(ctx context.Context, id string) (domain.MailboxFormat, error) {
	return s.repository.GetMailboxFormat(ctx, id)
}

func (s *FormatService) List(ctx context.Context, options ports.ListOptions) ([]domain.MailboxFormat, error) {
	return s.repository.ListMailboxFormats(ctx, options)
}

func (s *FormatService) EnsureBuiltins(ctx context.Context) error {
	now := s.clock().UTC()
	for _, format := range builtinFormats(now) {
		if _, err := s.repository.GetMailboxFormat(ctx, format.ID); err == nil {
			continue
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err := s.repository.CreateMailboxFormat(ctx, format); err != nil && !errors.Is(err, domain.ErrConflict) {
			return err
		}
	}
	return nil
}

func (s *FormatService) build(ctx context.Context, input SaveMailboxFormatInput) (domain.MailboxFormat, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.MailboxFormat{}, fmt.Errorf("%w: format name is required", domain.ErrInvalid)
	}
	kind := input.Kind
	if kind == "" {
		kind = domain.MailboxFormatDelimited
	}
	if kind != domain.MailboxFormatDelimited && kind != domain.MailboxFormatTemplate && kind != domain.MailboxFormatJSON {
		return domain.MailboxFormat{}, fmt.Errorf("%w: format kind %q", domain.ErrInvalid, kind)
	}
	direction := input.Direction
	if direction == "" {
		direction = domain.MailboxFormatBoth
	}
	if direction != domain.MailboxFormatImport && direction != domain.MailboxFormatExport && direction != domain.MailboxFormatBoth {
		return domain.MailboxFormat{}, fmt.Errorf("%w: format direction %q", domain.ErrInvalid, direction)
	}
	if kind == domain.MailboxFormatDelimited {
		if input.Delimiter == "" || strings.ContainsAny(input.Delimiter, "\r\n") || len(input.Delimiter) > 16 {
			return domain.MailboxFormat{}, fmt.Errorf("%w: delimiter must be 1-16 bytes without newlines", domain.ErrInvalid)
		}
	}
	if kind == domain.MailboxFormatTemplate && strings.TrimSpace(input.Template) == "" {
		return domain.MailboxFormat{}, fmt.Errorf("%w: template format requires a template", domain.ErrInvalid)
	}
	parserConfig := normalizedJSON(input.ParserConfig)
	config := formatParserConfig(domain.MailboxFormat{ParserConfig: parserConfig})
	if len(input.Fields) == 0 || len(input.Fields) > 64 {
		return domain.MailboxFormat{}, fmt.Errorf("%w: format requires 1-64 fields", domain.ErrInvalid)
	}
	fields := make([]domain.MailboxFormatField, len(input.Fields))
	columns := make(map[string]struct{}, len(fields))
	targets := make(map[string]struct{}, len(fields))
	for index, field := range input.Fields {
		field.Column = strings.TrimSpace(field.Column)
		field.Target = canonicalFormatTarget(field.Target)
		if field.Column == "" || !validFormatTarget(field.Target) {
			return domain.MailboxFormat{}, fmt.Errorf("%w: field %d has an invalid column or target", domain.ErrInvalid, index+1)
		}
		columnKey := strings.ToLower(field.Column)
		if _, exists := columns[columnKey]; exists {
			return domain.MailboxFormat{}, fmt.Errorf("%w: duplicate format column %q", domain.ErrInvalid, field.Column)
		}
		if _, exists := targets[field.Target]; exists && !strings.HasPrefix(field.Target, "metadata.") {
			return domain.MailboxFormat{}, fmt.Errorf("%w: duplicate format target %q", domain.ErrInvalid, field.Target)
		}
		columns[columnKey], targets[field.Target] = struct{}{}, struct{}{}
		if sensitiveFormatTarget(field.Target) {
			field.Sensitive = true
		}
		fields[index] = field
	}
	if direction != domain.MailboxFormatExport {
		if _, ok := targets["address"]; !ok {
			return domain.MailboxFormat{}, fmt.Errorf("%w: import format requires an address target", domain.ErrInvalid)
		}
		if (input.Provider == nil || *input.Provider == "") && !config.ProviderFromAddress {
			if _, ok := targets["provider"]; !ok {
				return domain.MailboxFormat{}, fmt.Errorf("%w: import format requires a fixed provider, provider field, or provider_from_address parser setting", domain.ErrInvalid)
			}
		}
	}
	var provider *domain.ProviderKey
	if input.Provider != nil && *input.Provider != "" {
		if _, err := s.providers.Get(*input.Provider); err != nil {
			return domain.MailboxFormat{}, err
		}
		value := *input.Provider
		provider = &value
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	format := domain.MailboxFormat{
		Name: name, Kind: kind, Direction: direction, Delimiter: input.Delimiter,
		Fields: fields, Provider: provider, HasHeader: input.HasHeader,
		Template: input.Template, ParserConfig: parserConfig, Enabled: enabled,
	}
	if kind == domain.MailboxFormatTemplate && direction != domain.MailboxFormatExport {
		if _, err := compileMailboxTemplate(format); err != nil {
			return domain.MailboxFormat{}, err
		}
	}
	return format, nil
}

func validFormatTarget(target string) bool {
	switch canonicalFormatTarget(target) {
	case "address", "display_name", "external_reference", "provider", "status",
		"credential_kind", "client_id", "refresh_token", "password", "pickup_key", "expires_at", "refresh_after",
		"username", "imap_host", "imap_port", "use_tls", "proxy_url", "inbox_folder", "junk_folder",
		"platform", "platform_external_reference", "platform_account_password", "platform_access_token":
		return true
	default:
		return strings.HasPrefix(target, "metadata.") && len(target) > len("metadata.")
	}
}

func sensitiveFormatTarget(target string) bool {
	target = canonicalFormatTarget(target)
	return target == "refresh_token" || target == "password" || target == "pickup_key" || target == "proxy_url" || target == "platform_account_password" || target == "platform_access_token"
}

func canonicalFormatTarget(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	switch target {
	case "host":
		return "imap_host"
	case "port":
		return "imap_port"
	default:
		return target
	}
}

func builtinFormats(now time.Time) []domain.MailboxFormat {
	microsoft := domain.ProviderMicrosoft
	cloudflareRoute := domain.ProviderCloudflareRoute
	return []domain.MailboxFormat{
		{
			ID: "fmt_builtin_outlook4", Name: "Outlook 4-part", Kind: domain.MailboxFormatDelimited,
			Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &microsoft, Enabled: true, Builtin: true, Version: 1,
			Fields: []domain.MailboxFormatField{
				{Column: "email", Target: "address", Required: true},
				{Column: "password", Target: "password", Sensitive: true},
				{Column: "client_id", Target: "client_id"},
				{Column: "refresh_token", Target: "refresh_token", Sensitive: true},
			},
			ParserConfig: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "fmt_builtin_registered6", Name: "Registered 6-part", Kind: domain.MailboxFormatDelimited,
			Direction: domain.MailboxFormatBoth, Delimiter: "----", Provider: &microsoft, Enabled: true, Builtin: true, Version: 1,
			Fields: []domain.MailboxFormatField{
				{Column: "email", Target: "address", Required: true},
				{Column: "gpt_password", Target: "platform_account_password", Sensitive: true},
				{Column: "password", Target: "password", Sensitive: true},
				{Column: "client_id", Target: "client_id"},
				{Column: "refresh_token", Target: "refresh_token", Sensitive: true},
				{Column: "access_token", Target: "platform_access_token", Sensitive: true},
			},
			ParserConfig: json.RawMessage(`{"platform":"chatgpt"}`), CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "fmt_builtin_cf_routed3", Name: "Cloudflare routed 3-part", Kind: domain.MailboxFormatDelimited,
			Direction: domain.MailboxFormatImport, Delimiter: "----", Provider: &cloudflareRoute, Enabled: true, Builtin: true, Version: 1,
			Fields: []domain.MailboxFormatField{
				{Column: "email", Target: "address", Required: true},
				{Column: "gpt_password", Target: "platform_account_password", Required: true, Sensitive: true},
				{Column: "mail_access_key", Target: "pickup_key", Required: true, Sensitive: true},
			},
			ParserConfig: json.RawMessage(`{"platform":"chatgpt"}`), CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "fmt_builtin_simple3", Name: "Email, GPT password and mailbox password", Kind: domain.MailboxFormatDelimited,
			Direction: domain.MailboxFormatBoth, Delimiter: "----", Enabled: true, Builtin: true, Version: 1,
			Fields: []domain.MailboxFormatField{
				{Column: "email", Target: "address", Required: true},
				{Column: "gpt_password", Target: "platform_account_password", Required: true, Sensitive: true},
				{Column: "password", Target: "password", Required: true, Sensitive: true},
			},
			ParserConfig: json.RawMessage(`{"platform":"chatgpt","provider_from_address":true}`), CreatedAt: now, UpdatedAt: now,
		},
	}
}

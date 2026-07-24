package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const defaultCloudflareAPIBase = "https://api.cloudflare.com/client/v4"

var ErrDestinationVerificationPending = errors.New("destination address verification is pending")

type CloudflareConfig struct {
	APIToken  string
	AccountID string
	ZoneID    string
	ZoneName  string
	BaseURL   string
}

type CloudflareConfigLoader func(context.Context) (CloudflareConfig, error)

// CloudflareRouteAdapter owns Cloudflare Email Routing API calls. Secrets stay
// in the adapter and are never copied into mailbox or route metadata.
type CloudflareRouteAdapter struct {
	Configured bool
	Resolver   ports.DestinationResolver

	client    *http.Client
	baseURL   string
	apiToken  string
	accountID string
	zoneID    string
	zoneName  string
	loader    CloudflareConfigLoader
}

func NewCloudflareRouteAdapter(config CloudflareConfig, client *http.Client) CloudflareRouteAdapter {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultCloudflareAPIBase
	}
	token := strings.TrimSpace(config.APIToken)
	accountID := strings.TrimSpace(config.AccountID)
	zoneID := strings.TrimSpace(config.ZoneID)
	zoneName := strings.ToLower(strings.TrimSpace(config.ZoneName))
	return CloudflareRouteAdapter{
		Configured: token != "" && accountID != "" && zoneID != "",
		client:     client,
		baseURL:    baseURL,
		apiToken:   token,
		accountID:  accountID,
		zoneID:     zoneID,
		zoneName:   zoneName,
	}
}

// NewDynamicCloudflareRouteAdapter resolves the active connection for every
// provider operation. Settings updates therefore take effect without mutating
// the registry or exposing provider credentials outside the loader boundary.
func NewDynamicCloudflareRouteAdapter(loader CloudflareConfigLoader, client *http.Client) CloudflareRouteAdapter {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return CloudflareRouteAdapter{client: client, loader: loader}
}

func (a CloudflareRouteAdapter) Descriptor(ctx context.Context) domain.ProviderDescriptor {
	configured := a.configured()
	if a.loader != nil {
		resolved, err := a.resolve(ctx)
		configured = err == nil && resolved.configured()
	}
	return domain.ProviderDescriptor{
		Key:         domain.ProviderCloudflareRoute,
		DisplayName: "Cloudflare Email Routing",
		Configured:  configured,
		Capabilities: domain.ProviderCapabilities{
			ProvisionMailbox: true,
			ManageAliases:    true,
			Forwarding:       true,
			RefreshTokens:    false,
			RetrievalMethods: []domain.RetrievalMethod{domain.RetrievalForwarded},
		},
	}
}

func (a CloudflareRouteAdapter) NormalizeAddress(address string) (string, error) {
	return normalizeAddress(address)
}

func (a CloudflareRouteAdapter) Provision(ctx context.Context, request domain.ProvisionMailboxRequest) (domain.ProvisionMailboxResult, error) {
	address, err := normalizeAddress(request.Address)
	if err != nil {
		return domain.ProvisionMailboxResult{}, err
	}
	localPart, zone, ok := strings.Cut(address, "@")
	if !ok {
		return domain.ProvisionMailboxResult{}, fmt.Errorf("%w: invalid routed address", domain.ErrInvalid)
	}
	var metadata struct {
		DestinationMailboxID string `json:"destination_mailbox_id"`
		DestinationAddress   string `json:"destination_address"`
	}
	if len(request.Metadata) == 0 || json.Unmarshal(request.Metadata, &metadata) != nil {
		return domain.ProvisionMailboxResult{}, fmt.Errorf("%w: destination metadata is required", domain.ErrInvalid)
	}
	result, err := a.CreateRoute(ctx, domain.DomainRouteRequest{
		Zone:                 zone,
		LocalPart:            localPart,
		DestinationMailboxID: strings.TrimSpace(metadata.DestinationMailboxID),
		DestinationAddress:   strings.TrimSpace(metadata.DestinationAddress),
	})
	if err != nil {
		return domain.ProvisionMailboxResult{}, err
	}
	return domain.ProvisionMailboxResult{
		ExternalReference: result.ExternalReference,
		Metadata:          result.Metadata,
	}, nil
}

func (a CloudflareRouteAdapter) RetrievalMethods() []domain.RetrievalMethod {
	return []domain.RetrievalMethod{domain.RetrievalForwarded}
}

func (a CloudflareRouteAdapter) Retrieve(context.Context, domain.Mailbox, domain.MailboxCredential, domain.MessageQuery) ([]domain.Message, error) {
	return nil, fmt.Errorf("%w: routed mail is retrieved through its destination mailbox", domain.ErrInvalid)
}

func (a CloudflareRouteAdapter) Refresh(context.Context, domain.Mailbox, domain.MailboxCredential) (domain.RefreshedCredential, error) {
	return domain.RefreshedCredential{}, fmt.Errorf("%w: Cloudflare routes do not have mailbox credentials", domain.ErrInvalid)
}

func (a CloudflareRouteAdapter) CreateRoute(ctx context.Context, request domain.DomainRouteRequest) (domain.DomainRouteResult, error) {
	if a.loader != nil {
		resolved, err := a.resolve(ctx)
		if err != nil {
			return domain.DomainRouteResult{}, err
		}
		return resolved.CreateRoute(ctx, request)
	}
	if !a.configured() {
		return domain.DomainRouteResult{}, notConfigured(domain.ProviderCloudflareRoute, "route creation")
	}
	zone := strings.ToLower(strings.TrimSpace(request.Zone))
	localPart := strings.ToLower(strings.TrimSpace(request.LocalPart))
	if zone == "" || localPart == "" || strings.Contains(localPart, "@") {
		return domain.DomainRouteResult{}, fmt.Errorf("%w: zone and local part are required", domain.ErrInvalid)
	}
	if a.zoneName != "" && zone != a.zoneName && !strings.HasSuffix(zone, "."+a.zoneName) {
		return domain.DomainRouteResult{}, fmt.Errorf("%w: routed address is outside the configured zone", domain.ErrInvalid)
	}
	sourceAddress, err := normalizeAddress(localPart + "@" + zone)
	if err != nil {
		return domain.DomainRouteResult{}, err
	}
	destinationAddress, err := normalizeAddress(request.DestinationAddress)
	if err != nil {
		return domain.DomainRouteResult{}, fmt.Errorf("%w: invalid destination address", domain.ErrInvalid)
	}
	if sourceAddress == destinationAddress {
		return domain.DomainRouteResult{}, fmt.Errorf("%w: route source and destination must differ", domain.ErrInvalid)
	}
	if err := a.ensureDestination(ctx, destinationAddress); err != nil {
		return domain.DomainRouteResult{}, err
	}

	rules, err := a.listRules(ctx)
	if err != nil {
		return domain.DomainRouteResult{}, err
	}
	for _, rule := range rules {
		if !ruleMatchesSource(rule, sourceAddress) {
			continue
		}
		if ruleForwardsTo(rule, destinationAddress) {
			metadata, _ := json.Marshal(map[string]any{"zone": zone, "managed": true})
			return domain.DomainRouteResult{SourceAddress: sourceAddress, ExternalReference: rule.Tag, Metadata: metadata}, nil
		}
		return domain.DomainRouteResult{}, fmt.Errorf("%w: routed address already targets another destination", domain.ErrConflict)
	}

	payload := cloudflareRule{
		Name:     "account-manager: " + sourceAddress,
		Enabled:  true,
		Matchers: []cloudflareMatcher{{Type: "literal", Field: "to", Value: sourceAddress}},
		Actions:  []cloudflareAction{{Type: "forward", Value: []string{destinationAddress}}},
	}
	var created cloudflareRule
	if err := a.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(a.zoneID)+"/email/routing/rules", payload, &created); err != nil {
		return domain.DomainRouteResult{}, err
	}
	if strings.TrimSpace(created.Tag) == "" {
		return domain.DomainRouteResult{}, fmt.Errorf("cloudflare route creation returned no rule id")
	}
	metadata, _ := json.Marshal(map[string]any{"zone": zone, "managed": true})
	return domain.DomainRouteResult{SourceAddress: sourceAddress, ExternalReference: created.Tag, Metadata: metadata}, nil
}

func (a CloudflareRouteAdapter) DeleteRoute(ctx context.Context, externalReference string) error {
	if a.loader != nil {
		resolved, err := a.resolve(ctx)
		if err != nil {
			return err
		}
		return resolved.DeleteRoute(ctx, externalReference)
	}
	if !a.configured() {
		return notConfigured(domain.ProviderCloudflareRoute, "route deletion")
	}
	ruleID := strings.TrimSpace(externalReference)
	if ruleID == "" || strings.ContainsAny(ruleID, "/\\") {
		return fmt.Errorf("%w: route reference is required", domain.ErrInvalid)
	}
	return a.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(a.zoneID)+"/email/routing/rules/"+url.PathEscape(ruleID), nil, nil)
}

func (a CloudflareRouteAdapter) VerifyDestination(ctx context.Context, destinationAddress string) error {
	if a.loader != nil {
		resolved, err := a.resolve(ctx)
		if err != nil {
			return err
		}
		return resolved.VerifyDestination(ctx, destinationAddress)
	}
	if !a.configured() {
		return notConfigured(domain.ProviderCloudflareRoute, "destination verification")
	}
	normalized, err := normalizeAddress(destinationAddress)
	if err != nil {
		return err
	}
	addresses, err := a.listDestinations(ctx)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if !strings.EqualFold(strings.TrimSpace(address.Email), normalized) {
			continue
		}
		if address.Verified != nil {
			return nil
		}
		return fmt.Errorf("%w: destination must be confirmed in Cloudflare", ErrDestinationVerificationPending)
	}
	return fmt.Errorf("%w: destination is not registered in Cloudflare", ErrDestinationVerificationPending)
}

func (a CloudflareRouteAdapter) ensureDestination(ctx context.Context, destinationAddress string) error {
	normalized, err := normalizeAddress(destinationAddress)
	if err != nil {
		return err
	}
	addresses, err := a.listDestinations(ctx)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if !strings.EqualFold(strings.TrimSpace(address.Email), normalized) {
			continue
		}
		if address.Verified != nil {
			return nil
		}
		return fmt.Errorf("%w: destination must be confirmed in Cloudflare", ErrDestinationVerificationPending)
	}
	var created cloudflareDestination
	path := "/accounts/" + url.PathEscape(a.accountID) + "/email/routing/addresses"
	if err := a.do(ctx, http.MethodPost, path, map[string]string{"email": normalized}, &created); err != nil {
		return err
	}
	return fmt.Errorf("%w: confirmation was sent to the destination address", ErrDestinationVerificationPending)
}

func (a CloudflareRouteAdapter) configured() bool {
	return (a.Configured || a.apiToken != "") && strings.TrimSpace(a.apiToken) != "" && strings.TrimSpace(a.accountID) != "" && strings.TrimSpace(a.zoneID) != ""
}

func (a CloudflareRouteAdapter) resolve(ctx context.Context) (CloudflareRouteAdapter, error) {
	if a.loader == nil {
		return a, nil
	}
	config, err := a.loader(ctx)
	if err != nil {
		return CloudflareRouteAdapter{}, fmt.Errorf("load Cloudflare provider connection: %w", err)
	}
	resolved := NewCloudflareRouteAdapter(config, a.client)
	resolved.Resolver = a.Resolver
	return resolved, nil
}

func (a CloudflareRouteAdapter) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (a CloudflareRouteAdapter) apiBase() string {
	if strings.TrimSpace(a.baseURL) != "" {
		return strings.TrimRight(a.baseURL, "/")
	}
	return defaultCloudflareAPIBase
}

type cloudflareEnvelope struct {
	Success bool                 `json:"success"`
	Errors  []cloudflareAPIError `json:"errors"`
	Result  json.RawMessage      `json:"result"`
}

type cloudflareAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflareDestination struct {
	Tag      string     `json:"tag"`
	Email    string     `json:"email"`
	Verified *time.Time `json:"verified"`
}

type cloudflareMatcher struct {
	Type  string `json:"type"`
	Field string `json:"field"`
	Value string `json:"value"`
}

type cloudflareAction struct {
	Type  string   `json:"type"`
	Value []string `json:"value"`
}

type cloudflareRule struct {
	Tag      string              `json:"tag,omitempty"`
	Name     string              `json:"name,omitempty"`
	Enabled  bool                `json:"enabled"`
	Matchers []cloudflareMatcher `json:"matchers"`
	Actions  []cloudflareAction  `json:"actions"`
}

func (a CloudflareRouteAdapter) listDestinations(ctx context.Context) ([]cloudflareDestination, error) {
	var result []cloudflareDestination
	path := "/accounts/" + url.PathEscape(a.accountID) + "/email/routing/addresses?per_page=50"
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (a CloudflareRouteAdapter) listRules(ctx context.Context) ([]cloudflareRule, error) {
	var result []cloudflareRule
	path := "/zones/" + url.PathEscape(a.zoneID) + "/email/routing/rules?per_page=50"
	if err := a.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (a CloudflareRouteAdapter) do(ctx context.Context, method, path string, payload any, output any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode Cloudflare request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.apiBase()+path, body)
	if err != nil {
		return fmt.Errorf("create Cloudflare request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+a.apiToken)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("Cloudflare API request failed: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 2<<20)
	var envelope cloudflareEnvelope
	if err := json.NewDecoder(limited).Decode(&envelope); err != nil {
		return fmt.Errorf("Cloudflare API returned an invalid response (status %d)", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		code, message := 0, "request rejected"
		if len(envelope.Errors) > 0 {
			code = envelope.Errors[0].Code
			message = sanitizeProviderMessage(envelope.Errors[0].Message, a.apiToken)
		}
		return fmt.Errorf("Cloudflare API error %d (status %d): %s", code, response.StatusCode, message)
	}
	if output == nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, output); err != nil {
		return fmt.Errorf("decode Cloudflare API result: %w", err)
	}
	return nil
}

func ruleMatchesSource(rule cloudflareRule, source string) bool {
	for _, matcher := range rule.Matchers {
		if strings.EqualFold(matcher.Type, "literal") && strings.EqualFold(matcher.Field, "to") && strings.EqualFold(strings.TrimSpace(matcher.Value), source) {
			return true
		}
	}
	return false
}

func ruleForwardsTo(rule cloudflareRule, destination string) bool {
	for _, action := range rule.Actions {
		if !strings.EqualFold(action.Type, "forward") {
			continue
		}
		for _, value := range action.Value {
			if strings.EqualFold(strings.TrimSpace(value), destination) {
				return true
			}
		}
	}
	return false
}

func sanitizeProviderMessage(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "<redacted>")
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 240 {
		message = message[:240]
	}
	if message == "" {
		return "request rejected"
	}
	return message
}

var _ ports.DomainRoutingProvider = CloudflareRouteAdapter{}

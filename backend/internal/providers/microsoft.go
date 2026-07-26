package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const (
	defaultMicrosoftTokenEndpoint = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	defaultMicrosoftGraphBaseURL  = "https://graph.microsoft.com/v1.0"
	defaultMicrosoftGraphScope    = "https://graph.microsoft.com/Mail.Read offline_access"
	defaultMicrosoftGraphFallback = "https://graph.microsoft.com/.default"
	defaultMicrosoftIMAPScope     = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"
	defaultMicrosoftPageSize      = 50
	defaultMicrosoftMaxPages      = 10
	maxMicrosoftPageSize          = 100
	maxMicrosoftMessageLimit      = 500
	maxMicrosoftPages             = 100
	maxMicrosoftResponseBytes     = 10 << 20
)

type MicrosoftConfig struct {
	TokenEndpoint string
	GraphBaseURL  string
	RefreshLead   time.Duration
	Now           func() time.Time
}

// MicrosoftAdapter owns Microsoft token and Graph payloads. Credential
// plaintext exists only within this adapter and is opened/sealed through the
// injected broker.
type MicrosoftAdapter struct {
	Configured bool

	secrets       ports.SecretBroker
	httpClient    *http.Client
	tokenEndpoint string
	graphBaseURL  string
	refreshLead   time.Duration
	now           func() time.Time
	imapFetch     microsoftIMAPFetchFunc
}

func NewMicrosoftAdapter(config MicrosoftConfig, secrets ports.SecretBroker, client *http.Client) MicrosoftAdapter {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	tokenEndpoint := strings.TrimSpace(config.TokenEndpoint)
	if tokenEndpoint == "" {
		tokenEndpoint = defaultMicrosoftTokenEndpoint
	}
	graphBaseURL := strings.TrimRight(strings.TrimSpace(config.GraphBaseURL), "/")
	if graphBaseURL == "" {
		graphBaseURL = defaultMicrosoftGraphBaseURL
	}
	refreshLead := config.RefreshLead
	if refreshLead <= 0 {
		refreshLead = 5 * time.Minute
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return MicrosoftAdapter{
		Configured:    secrets != nil,
		secrets:       secrets,
		httpClient:    client,
		tokenEndpoint: tokenEndpoint,
		graphBaseURL:  graphBaseURL,
		refreshLead:   refreshLead,
		now:           now,
	}
}

func (a MicrosoftAdapter) Descriptor(context.Context) domain.ProviderDescriptor {
	return domain.ProviderDescriptor{
		Key:         domain.ProviderMicrosoft,
		DisplayName: "Microsoft Outlook / Hotmail",
		Configured:  a.configured(),
		Capabilities: domain.ProviderCapabilities{
			ProvisionMailbox: false,
			ManageAliases:    false,
			RefreshTokens:    true,
			RetrievalMethods: []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth, domain.RetrievalDualToken},
		},
	}
}

func (a MicrosoftAdapter) NormalizeAddress(address string) (string, error) {
	return normalizeAddress(address)
}

func (a MicrosoftAdapter) Provision(context.Context, domain.ProvisionMailboxRequest) (domain.ProvisionMailboxResult, error) {
	return domain.ProvisionMailboxResult{}, fmt.Errorf("%w: Microsoft mailbox provisioning is not configured", domain.ErrNotConfigured)
}

func (a MicrosoftAdapter) RetrievalMethods() []domain.RetrievalMethod {
	return []domain.RetrievalMethod{domain.RetrievalMicrosoftGraph, domain.RetrievalIMAPOAuth, domain.RetrievalDualToken}
}

func (a MicrosoftAdapter) Retrieve(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
	if !a.configured() {
		return nil, notConfigured(domain.ProviderMicrosoft, "retrieve")
	}
	if mailbox.Provider != "" && mailbox.Provider != domain.ProviderMicrosoft {
		return nil, fmt.Errorf("%w: mailbox is not a Microsoft mailbox", domain.ErrInvalid)
	}
	secret, err := a.openCredential(ctx, credential)
	if err != nil {
		return nil, err
	}
	method, err := microsoftRetrievalMethod(credential.Kind, query.RetrievalMethod)
	if err != nil {
		return nil, err
	}
	switch method {
	case domain.RetrievalMicrosoftGraph:
		token, expiresAt := graphAccessToken(secret)
		if err := a.validateAccessToken(token, expiresAt, credential.ExpiresAt, "Microsoft Graph"); err != nil {
			return nil, err
		}
		return a.retrieveGraph(ctx, token, query)
	case domain.RetrievalIMAPOAuth:
		token, expiresAt := imapAccessToken(secret)
		if err := a.validateAccessToken(token, expiresAt, credential.ExpiresAt, "Microsoft IMAP"); err != nil {
			return nil, err
		}
		username := firstNonEmpty(secret.IMAPUsername, mailbox.NormalizedAddress, mailbox.Address)
		if strings.TrimSpace(username) == "" {
			return nil, fmt.Errorf("%w: Microsoft IMAP username is required", domain.ErrInvalid)
		}
		secret.IMAPUsername = username
		secret.IMAPAccessToken = token
		if strings.TrimSpace(query.RecipientAddress) != "" {
			normalized, normalizeErr := normalizeAddress(query.RecipientAddress)
			if normalizeErr != nil {
				return nil, fmt.Errorf("%w: invalid recipient filter", domain.ErrInvalid)
			}
			query.RecipientAddress = normalized
		}
		fetch := a.imapFetch
		if fetch == nil {
			fetch = retrieveMicrosoftIMAP
		}
		messages, fetchErr := fetch(ctx, secret, query)
		if fetchErr != nil {
			return nil, fetchErr
		}
		return filterMicrosoftIMAPMessages(messages, query), nil
	default:
		return nil, fmt.Errorf("%w: unsupported Microsoft retrieval method %q", domain.ErrInvalid, method)
	}
}

func (a MicrosoftAdapter) EnsureAccessToken(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential, method domain.RetrievalMethod, force bool) (domain.RefreshedCredential, bool, error) {
	if !a.configured() {
		return domain.RefreshedCredential{}, false, notConfigured(domain.ProviderMicrosoft, "refresh")
	}
	if _, err := microsoftRetrievalMethod(credential.Kind, method); err != nil {
		return domain.RefreshedCredential{}, false, err
	}
	secret, err := a.openCredential(ctx, credential)
	if err != nil {
		return domain.RefreshedCredential{}, false, err
	}
	var token string
	var expiresAt *time.Time
	switch method {
	case domain.RetrievalMicrosoftGraph:
		token, expiresAt = graphAccessToken(secret)
	case domain.RetrievalIMAPOAuth:
		token, expiresAt = imapAccessToken(secret)
	default:
		return domain.RefreshedCredential{}, false, fmt.Errorf("%w: unsupported Microsoft retrieval method %q", domain.ErrInvalid, method)
	}
	if !force && strings.TrimSpace(token) != "" && (expiresAt == nil || expiresAt.After(a.clock()().UTC().Add(a.refreshLead))) {
		return domain.RefreshedCredential{}, false, nil
	}
	clientID := firstNonEmpty(secret.ClientID, credential.ClientID)
	refreshToken := firstNonEmpty(secret.RefreshToken, secret.IMAPRefreshToken, secret.GraphRefreshToken)
	if strings.TrimSpace(clientID) == "" {
		return domain.RefreshedCredential{}, false, fmt.Errorf("%w: Microsoft OAuth client ID is required", domain.ErrInvalid)
	}
	var result microsoftTokenResult
	if method == domain.RetrievalMicrosoftGraph {
		result, err = a.refreshGraphAccessToken(ctx, clientID, refreshToken, secret.GraphScope)
		if err == nil {
			applyGraphToken(&secret, result)
		}
	} else {
		result, err = a.refreshAccessToken(ctx, clientID, refreshToken, microsoftIMAPScope(secret.IMAPScope))
		if err == nil {
			applyIMAPToken(&secret, result)
		}
	}
	if err != nil {
		return domain.RefreshedCredential{}, false, err
	}
	secret.ClientID = clientID
	secret.SchemaVersion = domain.MicrosoftCredentialSecretVersion
	setSharedMicrosoftRefreshToken(&secret, firstNonEmpty(result.RefreshToken, refreshToken))
	refreshed, err := a.sealRefreshedCredential(ctx, secret, result.ExpiresAt)
	return refreshed, err == nil, err
}

func (a MicrosoftAdapter) SyncIncremental(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential, request domain.MessageSyncRequest) (domain.MessageSyncResult, error) {
	secret, err := a.openCredential(ctx, credential)
	if err != nil {
		return domain.MessageSyncResult{}, err
	}
	switch request.Method {
	case domain.RetrievalMicrosoftGraph:
		token, expiresAt := graphAccessToken(secret)
		if err := a.validateAccessToken(token, expiresAt, credential.ExpiresAt, "Microsoft Graph"); err != nil {
			return domain.MessageSyncResult{}, err
		}
		return a.syncGraphDelta(ctx, token, request)
	case domain.RetrievalIMAPOAuth:
		token, expiresAt := imapAccessToken(secret)
		if err := a.validateAccessToken(token, expiresAt, credential.ExpiresAt, "Microsoft IMAP"); err != nil {
			return domain.MessageSyncResult{}, err
		}
		secret.IMAPAccessToken = token
		secret.IMAPUsername = firstNonEmpty(secret.IMAPUsername, mailbox.NormalizedAddress, mailbox.Address)
		return syncMicrosoftIMAP(ctx, secret, request)
	default:
		return domain.MessageSyncResult{}, fmt.Errorf("%w: unsupported Microsoft incremental method %q", domain.ErrInvalid, request.Method)
	}
}

func (a MicrosoftAdapter) Refresh(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential) (domain.RefreshedCredential, error) {
	if !a.configured() {
		return domain.RefreshedCredential{}, notConfigured(domain.ProviderMicrosoft, "refresh")
	}
	if mailbox.Provider != "" && mailbox.Provider != domain.ProviderMicrosoft {
		return domain.RefreshedCredential{}, fmt.Errorf("%w: mailbox is not a Microsoft mailbox", domain.ErrInvalid)
	}
	secret, err := a.openCredential(ctx, credential)
	if err != nil {
		return domain.RefreshedCredential{}, err
	}
	clientID := strings.TrimSpace(secret.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(credential.ClientID)
	}
	if clientID == "" {
		return domain.RefreshedCredential{}, fmt.Errorf("%w: Microsoft OAuth client ID is required", domain.ErrInvalid)
	}
	secret.ClientID = clientID
	secret.SchemaVersion = domain.MicrosoftCredentialSecretVersion

	var expiresAt *time.Time
	switch credential.Kind {
	case domain.CredentialMicrosoftGraphOAuth:
		source := firstNonEmpty(secret.RefreshToken, secret.GraphRefreshToken)
		result, refreshErr := a.refreshGraphAccessToken(ctx, clientID, source, secret.GraphScope)
		if refreshErr != nil {
			return domain.RefreshedCredential{}, refreshErr
		}
		applyGraphToken(&secret, result)
		setSharedMicrosoftRefreshToken(&secret, firstNonEmpty(result.RefreshToken, source))
		expiresAt = result.ExpiresAt
	case domain.CredentialMicrosoftIMAPOAuth:
		source := firstNonEmpty(secret.RefreshToken, secret.IMAPRefreshToken)
		result, refreshErr := a.refreshAccessToken(ctx, clientID, source, microsoftIMAPScope(secret.IMAPScope))
		if refreshErr != nil {
			return domain.RefreshedCredential{}, refreshErr
		}
		applyIMAPToken(&secret, result)
		setSharedMicrosoftRefreshToken(&secret, firstNonEmpty(result.RefreshToken, source))
		expiresAt = result.ExpiresAt
	case domain.CredentialMicrosoftDualToken:
		return a.refreshSharedDualCredential(ctx, credential, secret, clientID)
	default:
		return domain.RefreshedCredential{}, fmt.Errorf("%w: unsupported Microsoft credential kind %q", domain.ErrInvalid, credential.Kind)
	}

	return a.sealRefreshedCredential(ctx, secret, expiresAt)
}

func (a MicrosoftAdapter) refreshSharedDualCredential(ctx context.Context, credential domain.MailboxCredential, secret domain.MicrosoftCredentialSecret, clientID string) (domain.RefreshedCredential, error) {
	// Legacy Outlook imports contain one refresh-token chain. The compatibility
	// fields may differ after an older dual refresh, where IMAP was the second
	// request, so prefer it when the canonical field is absent.
	canonical := firstNonEmpty(secret.RefreshToken, secret.IMAPRefreshToken, secret.GraphRefreshToken)
	graphResult, refreshErr := a.refreshGraphAccessToken(ctx, clientID, canonical, secret.GraphScope)
	if refreshErr != nil {
		return domain.RefreshedCredential{}, fmt.Errorf("Microsoft Graph token refresh: %w", refreshErr)
	}
	applyGraphToken(&secret, graphResult)
	canonical = firstNonEmpty(graphResult.RefreshToken, canonical)
	setSharedMicrosoftRefreshToken(&secret, canonical)

	// Seal before the second remote call. If that call consumes the caller's
	// deadline, the service can still durably store the rotated Graph token.
	checkpointExpiry := earliestTime(graphResult.ExpiresAt, secret.IMAPTokenExpiresAt)
	if secret.IMAPTokenExpiresAt == nil {
		checkpointExpiry = earliestTime(graphResult.ExpiresAt, credential.ExpiresAt)
	}
	checkpoint, checkpointErr := a.sealRefreshedCredential(ctx, secret, checkpointExpiry)
	if checkpointErr != nil {
		return domain.RefreshedCredential{}, checkpointErr
	}

	imapResult, refreshErr := a.refreshAccessToken(ctx, clientID, canonical, microsoftIMAPScope(secret.IMAPScope))
	if refreshErr != nil {
		checkpoint.PersistOnError = true
		return checkpoint, fmt.Errorf("Microsoft IMAP token refresh: %w", refreshErr)
	}
	applyIMAPToken(&secret, imapResult)
	canonical = firstNonEmpty(imapResult.RefreshToken, canonical)
	setSharedMicrosoftRefreshToken(&secret, canonical)
	expiresAt := earliestTime(graphResult.ExpiresAt, imapResult.ExpiresAt)
	refreshed, err := a.sealRefreshedCredential(ctx, secret, expiresAt)
	if err != nil {
		checkpoint.PersistOnError = true
		return checkpoint, err
	}
	return refreshed, nil
}

func (a MicrosoftAdapter) sealRefreshedCredential(ctx context.Context, secret domain.MicrosoftCredentialSecret, expiresAt *time.Time) (domain.RefreshedCredential, error) {
	sealed, keyVersion, err := a.sealCredential(ctx, secret)
	if err != nil {
		return domain.RefreshedCredential{}, err
	}
	return domain.RefreshedCredential{
		EncryptedSecret: sealed,
		KeyVersion:      keyVersion,
		ExpiresAt:       expiresAt,
		RefreshAfter:    a.refreshAfter(expiresAt),
	}, nil
}

func setSharedMicrosoftRefreshToken(secret *domain.MicrosoftCredentialSecret, refreshToken string) {
	secret.RefreshToken = refreshToken
	secret.GraphRefreshToken = ""
	secret.IMAPRefreshToken = ""
}

func (a MicrosoftAdapter) configured() bool {
	return a.Configured && a.secrets != nil
}

func (a MicrosoftAdapter) clock() func() time.Time {
	if a.now != nil {
		return a.now
	}
	return time.Now
}

func (a MicrosoftAdapter) openCredential(ctx context.Context, credential domain.MailboxCredential) (domain.MicrosoftCredentialSecret, error) {
	if len(credential.EncryptedSecret) == 0 || strings.TrimSpace(credential.KeyVersion) == "" {
		return domain.MicrosoftCredentialSecret{}, fmt.Errorf("%w: encrypted Microsoft credential is required", domain.ErrInvalid)
	}
	plaintext, err := a.secrets.Open(ctx, credential.EncryptedSecret, credential.KeyVersion)
	if err != nil {
		return domain.MicrosoftCredentialSecret{}, fmt.Errorf("open Microsoft credential: %w", err)
	}
	defer clearBytes(plaintext)
	var secret domain.MicrosoftCredentialSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		return domain.MicrosoftCredentialSecret{}, fmt.Errorf("%w: malformed Microsoft credential payload", domain.ErrInvalid)
	}
	if secret.SchemaVersion == 0 {
		// Version-zero payloads are the original one-click import envelope,
		// which contained only refresh_token and used credential.ClientID.
		if secret.RefreshToken == "" && secret.GraphRefreshToken == "" && secret.IMAPRefreshToken == "" && secret.AccessToken == "" && secret.GraphAccessToken == "" && secret.IMAPAccessToken == "" {
			return domain.MicrosoftCredentialSecret{}, fmt.Errorf("%w: empty Microsoft credential payload", domain.ErrInvalid)
		}
		secret.SchemaVersion = domain.MicrosoftCredentialSecretVersion
	}
	if secret.SchemaVersion != domain.MicrosoftCredentialSecretVersion {
		return domain.MicrosoftCredentialSecret{}, fmt.Errorf("%w: unsupported Microsoft credential schema version", domain.ErrInvalid)
	}
	if strings.TrimSpace(secret.ClientID) == "" {
		secret.ClientID = strings.TrimSpace(credential.ClientID)
	}
	return secret, nil
}

func (a MicrosoftAdapter) sealCredential(ctx context.Context, secret domain.MicrosoftCredentialSecret) ([]byte, string, error) {
	plaintext, err := json.Marshal(secret)
	if err != nil {
		return nil, "", fmt.Errorf("encode Microsoft credential: %w", err)
	}
	defer clearBytes(plaintext)
	sealed, keyVersion, err := a.secrets.Seal(ctx, plaintext)
	if err != nil {
		return nil, "", fmt.Errorf("seal Microsoft credential: %w", err)
	}
	if len(sealed) == 0 || strings.TrimSpace(keyVersion) == "" {
		return nil, "", fmt.Errorf("%w: secret broker returned an empty Microsoft credential", domain.ErrInvalid)
	}
	return sealed, keyVersion, nil
}

type microsoftTokenResult struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresAt    *time.Time
}

func (a MicrosoftAdapter) refreshAccessToken(ctx context.Context, clientID, refreshToken, scope string) (microsoftTokenResult, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return microsoftTokenResult{}, fmt.Errorf("%w: Microsoft refresh token is required", domain.ErrInvalid)
	}
	endpoint := strings.TrimSpace(a.tokenEndpoint)
	if endpoint == "" {
		endpoint = defaultMicrosoftTokenEndpoint
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}
	if strings.TrimSpace(scope) != "" {
		form.Set("scope", strings.TrimSpace(scope))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return microsoftTokenResult{}, fmt.Errorf("create Microsoft token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.client().Do(req)
	if err != nil {
		return microsoftTokenResult{}, fmt.Errorf("Microsoft token request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimitedBody(response.Body)
	if err != nil {
		return microsoftTokenResult{}, fmt.Errorf("read Microsoft token response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var upstream struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &upstream)
		return microsoftTokenResult{}, microsoftOAuthError{StatusCode: response.StatusCode, Code: safeUpstreamCode(upstream.Error)}
	}
	var payload struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		TokenType    string          `json:"token_type"`
		Scope        string          `json:"scope"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return microsoftTokenResult{}, fmt.Errorf("%w: malformed Microsoft token response", domain.ErrInvalid)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return microsoftTokenResult{}, fmt.Errorf("%w: Microsoft token response omitted access_token", domain.ErrInvalid)
	}
	var expiresAt *time.Time
	if len(payload.ExpiresIn) > 0 && !bytes.Equal(payload.ExpiresIn, []byte("null")) {
		expiresIn, parseErr := parseExpiresIn(payload.ExpiresIn)
		if parseErr != nil || expiresIn <= 0 {
			return microsoftTokenResult{}, fmt.Errorf("%w: invalid Microsoft token expiry", domain.ErrInvalid)
		}
		expires := a.clock()().UTC().Add(time.Duration(expiresIn) * time.Second)
		expiresAt = &expires
	}
	return microsoftTokenResult{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
		Scope:        payload.Scope,
		ExpiresAt:    expiresAt,
	}, nil
}

func (a MicrosoftAdapter) refreshGraphAccessToken(ctx context.Context, clientID, refreshToken, scope string) (microsoftTokenResult, error) {
	requestedScope := microsoftGraphScope(scope)
	result, err := a.refreshAccessToken(ctx, clientID, refreshToken, requestedScope)
	if err == nil || requestedScope != defaultMicrosoftGraphScope || !errors.Is(err, domain.ErrUnauthorized) {
		return result, err
	}
	return a.refreshAccessToken(ctx, clientID, refreshToken, defaultMicrosoftGraphFallback)
}

type microsoftOAuthError struct {
	StatusCode int
	Code       string
}

func (e microsoftOAuthError) Error() string {
	return fmt.Sprintf("Microsoft token request failed (status %d, code %s)", e.StatusCode, safeUpstreamCode(e.Code))
}

func (e microsoftOAuthError) Unwrap() error {
	if e.Code == "invalid_grant" || e.Code == "invalid_client" || e.StatusCode == http.StatusUnauthorized {
		return domain.ErrUnauthorized
	}
	return nil
}

func (a MicrosoftAdapter) retrieveGraph(ctx context.Context, accessToken string, query domain.MessageQuery) ([]domain.Message, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultMicrosoftPageSize
	}
	if limit > maxMicrosoftMessageLimit {
		limit = maxMicrosoftMessageLimit
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = min(limit, defaultMicrosoftPageSize)
	}
	if pageSize > maxMicrosoftPageSize {
		pageSize = maxMicrosoftPageSize
	}
	maxPages := query.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMicrosoftMaxPages
	}
	if maxPages > maxMicrosoftPages {
		maxPages = maxMicrosoftPages
	}

	folder := "inbox"
	switch query.Folder {
	case "", domain.MessageFolderInbox:
	case domain.MessageFolderJunk:
		folder = "junkemail"
	default:
		return nil, fmt.Errorf("%w: unsupported Microsoft mail folder %q", domain.ErrInvalid, query.Folder)
	}
	requestedRecipient := ""
	if strings.TrimSpace(query.RecipientAddress) != "" {
		var err error
		requestedRecipient, err = normalizeAddress(query.RecipientAddress)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid recipient filter", domain.ErrInvalid)
		}
	}

	base, err := url.Parse(a.graphURL())
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("%w: invalid Microsoft Graph base URL", domain.ErrInvalid)
	}
	initial, err := url.Parse(strings.TrimRight(base.String(), "/") + "/me/mailFolders/" + url.PathEscape(folder) + "/messages")
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Microsoft Graph messages URL", domain.ErrInvalid)
	}
	params := initial.Query()
	params.Set("$top", strconv.Itoa(pageSize))
	params.Set("$orderby", "receivedDateTime DESC")
	params.Set("$select", strings.Join(graphSelectFields, ","))
	filters := make([]string, 0, 2)
	if query.After != nil {
		filters = append(filters, "receivedDateTime ge "+query.After.UTC().Format(time.RFC3339))
	}
	if query.Before != nil {
		filters = append(filters, "receivedDateTime lt "+query.Before.UTC().Format(time.RFC3339))
	}
	if query.Unread {
		filters = append(filters, "isRead eq false")
	}
	if len(filters) > 0 {
		params.Set("$filter", strings.Join(filters, " and "))
	}
	initial.RawQuery = params.Encode()
	next := initial
	result := make([]domain.Message, 0, min(limit, pageSize))
	seen := make(map[string]struct{})

	for pageNumber := 0; pageNumber < maxPages && next != nil && len(result) < limit; pageNumber++ {
		if !sameOrigin(base, next) {
			return nil, fmt.Errorf("%w: Microsoft Graph pagination changed origin", domain.ErrInvalid)
		}
		page, pageErr := a.fetchGraphPage(ctx, accessToken, next)
		if pageErr != nil {
			return nil, pageErr
		}
		for _, item := range page.Value {
			item, pageErr = a.hydrateGraphInlineImages(ctx, accessToken, item)
			if pageErr != nil {
				return nil, pageErr
			}
			message, normalizeErr := normalizeGraphMessage(item)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			if requestedRecipient != "" && !MessageMatchesRecipient(message, requestedRecipient) {
				continue
			}
			fingerprint := message.ID
			if fingerprint == "" {
				fingerprint = message.InternetMessageID
			}
			if fingerprint != "" {
				if _, exists := seen[fingerprint]; exists {
					continue
				}
				seen[fingerprint] = struct{}{}
			}
			result = append(result, message)
			if len(result) == limit {
				break
			}
		}
		next, err = resolveGraphNextLink(base, next, page.NextLink)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

var graphSelectFields = []string{
	"id", "internetMessageId", "subject", "receivedDateTime", "bodyPreview", "body",
	"from", "sender", "toRecipients", "ccRecipients", "internetMessageHeaders", "isRead", "hasAttachments",
}

type graphEmailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type graphRecipient struct {
	EmailAddress graphEmailAddress `json:"emailAddress"`
}

type graphHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type graphMessage struct {
	ID                     string           `json:"id"`
	InternetMessageID      string           `json:"internetMessageId"`
	Subject                string           `json:"subject"`
	ReceivedDateTime       string           `json:"receivedDateTime"`
	BodyPreview            string           `json:"bodyPreview"`
	Body                   graphBody        `json:"body"`
	From                   graphRecipient   `json:"from"`
	Sender                 graphRecipient   `json:"sender"`
	ToRecipients           []graphRecipient `json:"toRecipients"`
	CcRecipients           []graphRecipient `json:"ccRecipients"`
	InternetMessageHeaders []graphHeader    `json:"internetMessageHeaders"`
	IsRead                 *bool            `json:"isRead"`
	HasAttachments         bool             `json:"hasAttachments"`
	Removed                json.RawMessage  `json:"@removed"`
}

type graphAttachment struct {
	ContentType  string `json:"contentType"`
	ContentID    string `json:"contentId"`
	ContentBytes []byte `json:"contentBytes"`
	IsInline     bool   `json:"isInline"`
}

func (a MicrosoftAdapter) hydrateGraphInlineImages(ctx context.Context, accessToken string, item graphMessage) (graphMessage, error) {
	if !item.HasAttachments || !strings.EqualFold(item.Body.ContentType, "html") || !strings.Contains(strings.ToLower(item.Body.Content), "cid:") {
		return item, nil
	}
	target, err := url.Parse(a.graphURL() + "/me/messages/" + url.PathEscape(item.ID) + "/attachments?$top=50")
	if err != nil {
		return graphMessage{}, fmt.Errorf("%w: invalid Microsoft Graph attachments URL", domain.ErrInvalid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return graphMessage{}, fmt.Errorf("create Microsoft Graph attachments request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := a.client().Do(req)
	if err != nil {
		return graphMessage{}, fmt.Errorf("Microsoft Graph attachments request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimitedBody(response.Body)
	if err != nil {
		return graphMessage{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return graphMessage{}, microsoftAPIError{Service: "Microsoft Graph", StatusCode: response.StatusCode, Code: "attachments"}
	}
	var payload struct {
		Value []graphAttachment `json:"value"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return graphMessage{}, fmt.Errorf("%w: malformed Microsoft Graph attachments response", domain.ErrInvalid)
	}
	images := make(map[string]string)
	for _, attachment := range payload.Value {
		contentID := strings.Trim(strings.TrimSpace(attachment.ContentID), "<>")
		if !attachment.IsInline || contentID == "" || !strings.HasPrefix(strings.ToLower(attachment.ContentType), "image/") || len(attachment.ContentBytes) == 0 {
			continue
		}
		images[contentID] = "data:" + attachment.ContentType + ";base64," + base64.StdEncoding.EncodeToString(attachment.ContentBytes)
	}
	item.Body.Content = replaceCIDImages(item.Body.Content, images)
	return item, nil
}

type graphBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type graphPage struct {
	Value     []graphMessage `json:"value"`
	NextLink  string         `json:"@odata.nextLink"`
	DeltaLink string         `json:"@odata.deltaLink"`
}

func (a MicrosoftAdapter) fetchGraphPage(ctx context.Context, accessToken string, target *url.URL) (graphPage, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		page, err := a.fetchGraphPageOnce(ctx, accessToken, target)
		if err == nil {
			return page, nil
		}
		lastErr = err
		if attempt > 0 || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)) {
			break
		}
		select {
		case <-ctx.Done():
			return graphPage{}, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return graphPage{}, lastErr
}

func (a MicrosoftAdapter) fetchGraphPageOnce(ctx context.Context, accessToken string, target *url.URL) (graphPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return graphPage{}, fmt.Errorf("create Microsoft Graph request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Prefer", `IdType="ImmutableId"`)
	response, err := a.client().Do(req)
	if err != nil {
		return graphPage{}, fmt.Errorf("Microsoft Graph request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := readLimitedBody(response.Body)
	if err != nil {
		return graphPage{}, fmt.Errorf("read Microsoft Graph response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var upstream struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &upstream)
		return graphPage{}, microsoftAPIError{Service: "Microsoft Graph", StatusCode: response.StatusCode, Code: safeUpstreamCode(upstream.Error.Code)}
	}
	var page graphPage
	if err := json.Unmarshal(body, &page); err != nil {
		return graphPage{}, fmt.Errorf("%w: malformed Microsoft Graph response", domain.ErrInvalid)
	}
	return page, nil
}

func normalizeGraphMessage(item graphMessage) (domain.Message, error) {
	headers := make(map[string][]string)
	for _, header := range item.InternetMessageHeaders {
		name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(header.Name))
		if name == "" {
			continue
		}
		headers[name] = append(headers[name], header.Value)
	}
	to := graphRecipientAddresses(item.ToRecipients)
	cc := graphRecipientAddresses(item.CcRecipients)
	from := strings.TrimSpace(item.From.EmailAddress.Address)
	if from == "" {
		from = strings.TrimSpace(item.Sender.EmailAddress.Address)
	}
	var receivedAt time.Time
	if strings.TrimSpace(item.ReceivedDateTime) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, item.ReceivedDateTime)
		if err != nil {
			return domain.Message{}, fmt.Errorf("%w: Microsoft Graph returned an invalid receivedDateTime", domain.ErrInvalid)
		}
		receivedAt = parsed
	}
	message := domain.Message{
		ID:                strings.TrimSpace(item.ID),
		InternetMessageID: strings.TrimSpace(item.InternetMessageID),
		From:              from,
		To:                to,
		Cc:                cc,
		Subject:           strings.TrimSpace(item.Subject),
		ReceivedAt:        receivedAt,
		Headers:           headers,
	}
	if item.IsRead != nil {
		message.Unread = !*item.IsRead
	}
	if strings.EqualFold(strings.TrimSpace(item.Body.ContentType), "html") {
		message.HTML = item.Body.Content
	} else {
		message.Text = item.Body.Content
	}
	message.RecipientAddresses = ExtractRecipientAddresses(message.To, message.Cc, message.Headers)
	return message, nil
}

func graphRecipientAddresses(recipients []graphRecipient) []string {
	result := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		address := strings.TrimSpace(recipient.EmailAddress.Address)
		if address != "" {
			result = append(result, address)
		}
	}
	return result
}

func resolveGraphNextLink(origin, current *url.URL, raw string) (*url.URL, error) {
	return resolveMicrosoftNextLink(origin, current, raw, "Microsoft Graph")
}

func resolveMicrosoftNextLink(origin, current *url.URL, raw, service string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	next, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s nextLink", domain.ErrInvalid, service)
	}
	if !next.IsAbs() {
		next = current.ResolveReference(next)
	}
	if next.User != nil || !sameOrigin(origin, next) {
		return nil, fmt.Errorf("%w: %s pagination changed origin", domain.ErrInvalid, service)
	}
	return next, nil
}

type microsoftAPIError struct {
	Service    string
	StatusCode int
	Code       string
}

func (e microsoftAPIError) Error() string {
	return fmt.Sprintf("%s request failed (status %d, code %s)", e.Service, e.StatusCode, safeUpstreamCode(e.Code))
}

func (e microsoftAPIError) Unwrap() error {
	if e.StatusCode == http.StatusGone {
		return domain.ErrSyncCursorInvalid
	}
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		return domain.ErrUnauthorized
	}
	return nil
}

func (a MicrosoftAdapter) syncGraphDelta(ctx context.Context, accessToken string, request domain.MessageSyncRequest) (domain.MessageSyncResult, error) {
	base, err := url.Parse(a.graphURL())
	if err != nil {
		return domain.MessageSyncResult{}, fmt.Errorf("%w: invalid Microsoft Graph base URL", domain.ErrInvalid)
	}
	var next *url.URL
	if strings.TrimSpace(request.Cursor) != "" {
		next, err = url.Parse(request.Cursor)
		if err != nil || !next.IsAbs() || !sameOrigin(base, next) {
			return domain.MessageSyncResult{}, fmt.Errorf("%w: invalid Microsoft Graph delta cursor", domain.ErrSyncCursorInvalid)
		}
	} else {
		folder := "inbox"
		if request.Folder == domain.MessageFolderJunk {
			folder = "junkemail"
		}
		next = base.ResolveReference(&url.URL{Path: strings.TrimRight(base.Path, "/") + "/me/mailFolders/" + folder + "/messages/delta"})
		query := next.Query()
		query.Set("$select", strings.Join(graphSelectFields, ","))
		query.Set("$top", strconv.Itoa(max(1, min(request.PageSize, maxMicrosoftPageSize))))
		next.RawQuery = query.Encode()
	}
	maxPages := request.MaxPages
	if maxPages <= 0 || maxPages > maxMicrosoftPages {
		maxPages = defaultMicrosoftMaxPages
	}
	limit := request.Limit
	if limit <= 0 || limit > maxMicrosoftMessageLimit {
		limit = maxMicrosoftMessageLimit
	}
	result := domain.MessageSyncResult{Complete: true}
	for pageNumber := 0; next != nil && pageNumber < maxPages; pageNumber++ {
		page, fetchErr := a.fetchGraphPage(ctx, accessToken, next)
		if fetchErr != nil {
			return domain.MessageSyncResult{}, fetchErr
		}
		for _, item := range page.Value {
			if len(item.Removed) > 0 && string(item.Removed) != "null" {
				if id := strings.TrimSpace(item.ID); id != "" {
					result.DeletedProviderMessageIDs = append(result.DeletedProviderMessageIDs, id)
				}
				continue
			}
			item, fetchErr = a.hydrateGraphInlineImages(ctx, accessToken, item)
			if fetchErr != nil {
				return domain.MessageSyncResult{}, fetchErr
			}
			message, normalizeErr := normalizeGraphMessage(item)
			if normalizeErr != nil {
				return domain.MessageSyncResult{}, normalizeErr
			}
			result.Messages = append(result.Messages, message)
		}
		if page.NextLink != "" {
			resolved, resolveErr := resolveGraphNextLink(base, next, page.NextLink)
			if resolveErr != nil {
				return domain.MessageSyncResult{}, resolveErr
			}
			result.Cursor = resolved.String()
			next = resolved
		} else if page.DeltaLink != "" {
			resolved, resolveErr := resolveGraphNextLink(base, next, page.DeltaLink)
			if resolveErr != nil {
				return domain.MessageSyncResult{}, resolveErr
			}
			result.Cursor = resolved.String()
			next = nil
		} else {
			next = nil
		}
		if len(result.Messages) >= limit && page.NextLink != "" {
			result.Complete = false
			break
		}
	}
	if next != nil {
		result.Complete = false
	}
	return result, nil
}

func microsoftGraphScope(scope string) string {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	if normalized == "" || strings.Contains(normalized, "outlook.office.com/") {
		return defaultMicrosoftGraphScope
	}
	if strings.Contains(normalized, "graph.microsoft.com/.default") {
		return defaultMicrosoftGraphFallback
	}
	return strings.TrimSpace(scope)
}

func microsoftIMAPScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return defaultMicrosoftIMAPScope
	}
	return strings.TrimSpace(scope)
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func graphAccessToken(secret domain.MicrosoftCredentialSecret) (string, *time.Time) {
	if strings.TrimSpace(secret.GraphAccessToken) != "" {
		return secret.GraphAccessToken, secret.GraphTokenExpiresAt
	}
	if secret.AccessTokenMethod == domain.RetrievalMicrosoftGraph && strings.TrimSpace(secret.AccessToken) != "" {
		return secret.AccessToken, secret.AccessTokenExpiresAt
	}
	return "", nil
}

func applyGraphToken(secret *domain.MicrosoftCredentialSecret, result microsoftTokenResult) {
	secret.GraphAccessToken = result.AccessToken
	secret.GraphTokenExpiresAt = result.ExpiresAt
	if result.Scope != "" {
		secret.GraphScope = result.Scope
	}
	if result.TokenType != "" {
		secret.TokenType = result.TokenType
	}
}

func applyIMAPToken(secret *domain.MicrosoftCredentialSecret, result microsoftTokenResult) {
	secret.IMAPAccessToken = result.AccessToken
	secret.IMAPTokenExpiresAt = result.ExpiresAt
	if result.Scope != "" {
		secret.IMAPScope = result.Scope
	}
	if result.TokenType != "" {
		secret.TokenType = result.TokenType
	}
}

func (a MicrosoftAdapter) refreshAfter(expiresAt *time.Time) *time.Time {
	if expiresAt == nil {
		return nil
	}
	now := a.clock()().UTC()
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return &now
	}
	lead := a.refreshLead
	if lead <= 0 {
		lead = 5 * time.Minute
	}
	if lead >= remaining {
		lead = remaining / 2
	}
	refreshAfter := expiresAt.Add(-lead)
	return &refreshAfter
}

func (a MicrosoftAdapter) graphURL() string {
	if strings.TrimSpace(a.graphBaseURL) == "" {
		return defaultMicrosoftGraphBaseURL
	}
	return a.graphBaseURL
}

func (a MicrosoftAdapter) client() *http.Client {
	if a.httpClient != nil {
		return a.httpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func parseExpiresIn(raw json.RawMessage) (int64, error) {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return strconv.ParseInt(value, 10, 64)
}

func readLimitedBody(reader io.Reader) ([]byte, error) {
	limited := io.LimitReader(reader, maxMicrosoftResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxMicrosoftResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxMicrosoftResponseBytes)
	}
	return body, nil
}

func safeUpstreamCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 80 {
		return "unknown"
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return "unknown"
	}
	return code
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func earliestTime(values ...*time.Time) *time.Time {
	var earliest *time.Time
	for _, value := range values {
		if value == nil {
			continue
		}
		if earliest == nil || value.Before(*earliest) {
			copyValue := *value
			earliest = &copyValue
		}
	}
	return earliest
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

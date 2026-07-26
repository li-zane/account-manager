package providers

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	netmail "net/mail"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	message "github.com/emersion/go-message"
	mailmessage "github.com/emersion/go-message/mail"
	"golang.org/x/net/proxy"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const (
	defaultGmailAPIBase       = "https://gmail.googleapis.com/gmail/v1"
	defaultGoogleTokenURL     = "https://oauth2.googleapis.com/token"
	defaultGmailIMAPHost      = "imap.gmail.com"
	defaultGmailIMAPPort      = 993
	defaultGmailInboxFolder   = "INBOX"
	defaultGmailJunkFolder    = "[Gmail]/Spam"
	providerResponseBodyLimit = 2 << 20
	imapProxyHandshakeTimeout = 15 * time.Second
)

type GmailCredentialSecret struct {
	ClientID             string     `json:"client_id,omitempty"`
	ClientSecret         string     `json:"client_secret,omitempty"`
	RefreshToken         string     `json:"refresh_token,omitempty"`
	AccessToken          string     `json:"access_token,omitempty"`
	AccessTokenExpiresAt *time.Time `json:"access_token_expires_at,omitempty"`

	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	UseTLS      *bool  `json:"use_tls,omitempty"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	InboxFolder string `json:"inbox_folder,omitempty"`
	JunkFolder  string `json:"junk_folder,omitempty"`
}

type gmailIMAPFetchFunc func(context.Context, GmailCredentialSecret, domain.MessageQuery) ([]domain.Message, error)

// GmailAdapter supports the Gmail JSON API for OAuth credentials and IMAP for
// app-password credentials. Credential plaintext exists only inside a call.
type GmailAdapter struct {
	Configured    bool
	Broker        ports.SecretBroker
	HTTPClient    *http.Client
	APIBase       string
	TokenEndpoint string

	imapFetch gmailIMAPFetchFunc
}

func NewGmailAdapter(broker ports.SecretBroker, client *http.Client) GmailAdapter {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return GmailAdapter{Configured: broker != nil, Broker: broker, HTTPClient: client}
}

func (a GmailAdapter) Descriptor(context.Context) domain.ProviderDescriptor {
	return domain.ProviderDescriptor{
		Key:         domain.ProviderGmail,
		DisplayName: "Google Gmail",
		Configured:  a.configured(),
		Capabilities: domain.ProviderCapabilities{
			ProvisionMailbox: false,
			ManageAliases:    false,
			RefreshTokens:    true,
			RetrievalMethods: []domain.RetrievalMethod{domain.RetrievalGmailAPI, domain.RetrievalIMAPPassword},
		},
	}
}

func (a GmailAdapter) NormalizeAddress(address string) (string, error) {
	return normalizeAddress(address)
}

func (a GmailAdapter) Provision(context.Context, domain.ProvisionMailboxRequest) (domain.ProvisionMailboxResult, error) {
	return domain.ProvisionMailboxResult{}, fmt.Errorf("%w: Gmail mailboxes are connected rather than provisioned", domain.ErrInvalid)
}

func (a GmailAdapter) RetrievalMethods() []domain.RetrievalMethod {
	return []domain.RetrievalMethod{domain.RetrievalGmailAPI, domain.RetrievalIMAPPassword}
}

func (a GmailAdapter) Retrieve(ctx context.Context, mailbox domain.Mailbox, credential domain.MailboxCredential, query domain.MessageQuery) ([]domain.Message, error) {
	if !a.configured() {
		return nil, notConfigured(domain.ProviderGmail, "retrieve")
	}
	secret, err := a.openCredential(ctx, credential)
	if err != nil {
		return nil, err
	}
	if query.RecipientAddress == "" {
		query.RecipientAddress = mailbox.NormalizedAddress
	}
	switch credential.Kind {
	case domain.CredentialGmailOAuth:
		if strings.TrimSpace(secret.AccessToken) == "" || tokenExpiresSoon(secret.AccessTokenExpiresAt) {
			secret, _, err = a.refreshOAuthSecret(ctx, secret)
			if err != nil {
				return nil, err
			}
		}
		return a.retrieveGmailAPI(ctx, secret.AccessToken, query)
	case domain.CredentialIMAPPassword:
		fetch := a.imapFetch
		if fetch == nil {
			fetch = retrieveGmailIMAP
		}
		return fetch(ctx, secret, query)
	default:
		return nil, fmt.Errorf("%w: credential kind %q is not supported by Gmail", domain.ErrInvalid, credential.Kind)
	}
}

func (a GmailAdapter) Refresh(ctx context.Context, _ domain.Mailbox, credential domain.MailboxCredential) (domain.RefreshedCredential, error) {
	if !a.configured() {
		return domain.RefreshedCredential{}, notConfigured(domain.ProviderGmail, "refresh")
	}
	if credential.Kind != domain.CredentialGmailOAuth {
		return domain.RefreshedCredential{}, fmt.Errorf("%w: credential kind %q does not use OAuth refresh", domain.ErrInvalid, credential.Kind)
	}
	secret, err := a.openCredential(ctx, credential)
	if err != nil {
		return domain.RefreshedCredential{}, err
	}
	secret, expiresAt, err := a.refreshOAuthSecret(ctx, secret)
	if err != nil {
		return domain.RefreshedCredential{}, err
	}
	plaintext, err := json.Marshal(secret)
	if err != nil {
		return domain.RefreshedCredential{}, fmt.Errorf("encode refreshed Gmail credential: %w", err)
	}
	sealed, keyVersion, err := a.Broker.Seal(ctx, plaintext)
	if err != nil {
		return domain.RefreshedCredential{}, fmt.Errorf("seal refreshed Gmail credential: %w", err)
	}
	refreshAfter := expiresAt.Add(-5 * time.Minute)
	return domain.RefreshedCredential{
		EncryptedSecret: sealed,
		KeyVersion:      keyVersion,
		ExpiresAt:       &expiresAt,
		RefreshAfter:    &refreshAfter,
	}, nil
}

func (a GmailAdapter) configured() bool {
	return a.Broker != nil && (a.Configured || a.Broker != nil)
}

func (a GmailAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (a GmailAdapter) apiBase() string {
	if value := strings.TrimRight(strings.TrimSpace(a.APIBase), "/"); value != "" {
		return value
	}
	return defaultGmailAPIBase
}

func (a GmailAdapter) tokenURL() string {
	if value := strings.TrimSpace(a.TokenEndpoint); value != "" {
		return value
	}
	return defaultGoogleTokenURL
}

func (a GmailAdapter) openCredential(ctx context.Context, credential domain.MailboxCredential) (GmailCredentialSecret, error) {
	plaintext, err := a.Broker.Open(ctx, credential.EncryptedSecret, credential.KeyVersion)
	if err != nil {
		return GmailCredentialSecret{}, fmt.Errorf("open Gmail credential: %w", err)
	}
	defer clear(plaintext)
	var secret GmailCredentialSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		return GmailCredentialSecret{}, fmt.Errorf("decode Gmail credential: %w", err)
	}
	return secret, nil
}

func tokenExpiresSoon(expiresAt *time.Time) bool {
	return expiresAt == nil || !expiresAt.After(time.Now().UTC().Add(time.Minute))
}

func (a GmailAdapter) refreshOAuthSecret(ctx context.Context, secret GmailCredentialSecret) (GmailCredentialSecret, time.Time, error) {
	if strings.TrimSpace(secret.ClientID) == "" || strings.TrimSpace(secret.RefreshToken) == "" {
		return GmailCredentialSecret{}, time.Time{}, fmt.Errorf("%w: Gmail client id and refresh token are required", domain.ErrInvalid)
	}
	form := url.Values{
		"client_id":     {secret.ClientID},
		"refresh_token": {secret.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	if secret.ClientSecret != "" {
		form.Set("client_secret", secret.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return GmailCredentialSecret{}, time.Time{}, fmt.Errorf("create Google token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.client().Do(request)
	if err != nil {
		return GmailCredentialSecret{}, time.Time{}, fmt.Errorf("Google token refresh failed: %w", err)
	}
	defer response.Body.Close()
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, providerResponseBodyLimit)).Decode(&payload); err != nil {
		return GmailCredentialSecret{}, time.Time{}, fmt.Errorf("Google token endpoint returned an invalid response (status %d)", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.AccessToken == "" {
		message := sanitizeProviderMessage(payload.Description, secret.RefreshToken, secret.ClientSecret)
		return GmailCredentialSecret{}, time.Time{}, fmt.Errorf("Google token refresh rejected (status %d, %s): %s", response.StatusCode, payload.Error, message)
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	secret.AccessToken = payload.AccessToken
	secret.AccessTokenExpiresAt = &expiresAt
	if payload.RefreshToken != "" {
		secret.RefreshToken = payload.RefreshToken
	}
	return secret, expiresAt, nil
}

type gmailListResponse struct {
	Messages      []gmailMessageRef `json:"messages"`
	NextPageToken string            `json:"nextPageToken"`
}

type gmailMessageRef struct {
	ID string `json:"id"`
}

type gmailAPIMessage struct {
	ID           string       `json:"id"`
	ThreadID     string       `json:"threadId"`
	LabelIDs     []string     `json:"labelIds"`
	InternalDate string       `json:"internalDate"`
	Payload      gmailPayload `json:"payload"`
}

type gmailPayload struct {
	MIMEType string         `json:"mimeType"`
	Headers  []gmailHeader  `json:"headers"`
	Body     gmailBody      `json:"body"`
	Parts    []gmailPayload `json:"parts"`
}

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailBody struct {
	Data         string `json:"data"`
	AttachmentID string `json:"attachmentId"`
}

func (a GmailAdapter) retrieveGmailAPI(ctx context.Context, accessToken string, query domain.MessageQuery) ([]domain.Message, error) {
	limit, pageSize, maxPages := normalizeMessageQuery(query)
	pageToken := ""
	result := make([]domain.Message, 0, limit)
	for page := 0; page < maxPages && len(result) < limit; page++ {
		values := url.Values{"maxResults": {strconv.Itoa(pageSize)}, "labelIds": {gmailLabel(query.Folder)}}
		search := make([]string, 0, 3)
		if query.After != nil {
			search = append(search, "after:"+strconv.FormatInt(query.After.UTC().Unix(), 10))
		}
		if query.Before != nil {
			search = append(search, "before:"+strconv.FormatInt(query.Before.UTC().Unix(), 10))
		}
		if query.Unread {
			search = append(search, "is:unread")
		}
		if len(search) > 0 {
			values.Set("q", strings.Join(search, " "))
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		var list gmailListResponse
		if err := a.gmailJSON(ctx, accessToken, "/users/me/messages?"+values.Encode(), &list); err != nil {
			return nil, err
		}
		for _, ref := range list.Messages {
			var raw gmailAPIMessage
			path := "/users/me/messages/" + url.PathEscape(ref.ID) + "?format=full"
			if err := a.gmailJSON(ctx, accessToken, path, &raw); err != nil {
				return nil, err
			}
			if err := a.hydrateGmailPayload(ctx, accessToken, raw.ID, &raw.Payload); err != nil {
				return nil, err
			}
			message := normalizeGmailAPIMessage(raw)
			if query.After != nil && message.ReceivedAt.Before(query.After.UTC()) {
				continue
			}
			if query.Before != nil && !message.ReceivedAt.Before(query.Before.UTC()) {
				continue
			}
			if query.RecipientAddress != "" && !MessageMatchesRecipient(message, query.RecipientAddress) {
				continue
			}
			result = append(result, message)
			if len(result) == limit {
				break
			}
		}
		pageToken = list.NextPageToken
		if pageToken == "" {
			break
		}
	}
	sortMessages(result)
	return result, nil
}

func (a GmailAdapter) hydrateGmailPayload(ctx context.Context, accessToken, messageID string, payload *gmailPayload) error {
	if payload.Body.Data == "" && payload.Body.AttachmentID != "" {
		var body gmailBody
		path := "/users/me/messages/" + url.PathEscape(messageID) + "/attachments/" + url.PathEscape(payload.Body.AttachmentID)
		if err := a.gmailJSON(ctx, accessToken, path, &body); err != nil {
			return err
		}
		payload.Body.Data = body.Data
	}
	for index := range payload.Parts {
		if err := a.hydrateGmailPayload(ctx, accessToken, messageID, &payload.Parts[index]); err != nil {
			return err
		}
	}
	return nil
}

func (a GmailAdapter) gmailJSON(ctx context.Context, accessToken, path string, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiBase()+path, nil)
	if err != nil {
		return fmt.Errorf("create Gmail API request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := a.client().Do(request)
	if err != nil {
		return fmt.Errorf("Gmail API request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, providerResponseBodyLimit))
		return fmt.Errorf("Gmail API request rejected (status %d)", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, providerResponseBodyLimit)).Decode(output); err != nil {
		return fmt.Errorf("Gmail API returned an invalid response: %w", err)
	}
	return nil
}

func normalizeGmailAPIMessage(raw gmailAPIMessage) domain.Message {
	headers := make(map[string][]string)
	decoder := new(mime.WordDecoder)
	for _, header := range raw.Payload.Headers {
		name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(header.Name))
		if name == "" {
			continue
		}
		value := header.Value
		if decoded, err := decoder.DecodeHeader(value); err == nil {
			value = decoded
		}
		headers[name] = append(headers[name], value)
	}
	to := extractAddresses(strings.Join(headers["To"], ","))
	cc := extractAddresses(strings.Join(headers["Cc"], ","))
	from := ""
	if values := extractAddresses(strings.Join(headers["From"], ",")); len(values) > 0 {
		from = values[0]
	}
	receivedAt := time.Time{}
	if milliseconds, err := strconv.ParseInt(raw.InternalDate, 10, 64); err == nil {
		receivedAt = time.UnixMilli(milliseconds).UTC()
	}
	if receivedAt.IsZero() && len(headers["Date"]) > 0 {
		receivedAt, _ = netmail.ParseDate(headers["Date"][0])
	}
	textBody, htmlBody := gmailPayloadBodies(raw.Payload)
	message := domain.Message{
		ID:                raw.ID,
		InternetMessageID: firstHeader(headers, "Message-Id"),
		From:              from,
		To:                to,
		Cc:                cc,
		Subject:           firstHeader(headers, "Subject"),
		Text:              textBody,
		HTML:              htmlBody,
		ReceivedAt:        receivedAt,
		Unread:            containsFold(raw.LabelIDs, "UNREAD"),
		Headers:           headers,
	}
	message.RecipientAddresses = ExtractRecipientAddresses(message.To, message.Cc, message.Headers)
	return message
}

func gmailPayloadBodies(payload gmailPayload) (string, string) {
	var textParts, htmlParts []string
	inlineImages := make(map[string]string)
	var walk func(gmailPayload)
	walk = func(part gmailPayload) {
		if part.Body.Data != "" {
			if decoded, err := decodeBase64URL(part.Body.Data); err == nil {
				contentType := strings.ToLower(strings.TrimSpace(part.MIMEType))
				switch contentType {
				case "text/plain":
					textParts = append(textParts, string(decoded))
				case "text/html":
					htmlParts = append(htmlParts, string(decoded))
				default:
					if strings.HasPrefix(contentType, "image/") {
						contentID := strings.Trim(strings.TrimSpace(gmailPayloadHeader(part, "Content-ID")), "<>")
						if contentID != "" {
							inlineImages[contentID] = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(decoded)
						}
					}
				}
			}
		}
		for _, child := range part.Parts {
			walk(child)
		}
	}
	walk(payload)
	return strings.Join(textParts, "\n"), replaceCIDImages(strings.Join(htmlParts, "\n"), inlineImages)
}

func gmailPayloadHeader(payload gmailPayload, name string) string {
	for _, header := range payload.Headers {
		if strings.EqualFold(strings.TrimSpace(header.Name), name) {
			return header.Value
		}
	}
	return ""
}

func decodeBase64URL(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

func retrieveGmailIMAP(ctx context.Context, secret GmailCredentialSecret, query domain.MessageQuery) ([]domain.Message, error) {
	if strings.TrimSpace(secret.Username) == "" || strings.TrimSpace(secret.Password) == "" {
		return nil, fmt.Errorf("%w: IMAP username and app password are required", domain.ErrInvalid)
	}
	host := strings.TrimSpace(secret.Host)
	if host == "" {
		host = defaultGmailIMAPHost
	}
	port := secret.Port
	if port <= 0 {
		port = defaultGmailIMAPPort
	}
	useTLS := true
	if secret.UseTLS != nil {
		useTLS = *secret.UseTLS
	}
	dialer, err := imapDialer(secret.ProxyURL)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	var client *imapclient.Client
	if useTLS {
		client, err = imapclient.DialWithDialerTLS(dialer, address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		client, err = imapclient.DialWithDialer(dialer, address)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to Gmail IMAP: %w", err)
	}
	client.ErrorLog = log.New(io.Discard, "", 0)
	client.Timeout = 25 * time.Second
	defer func() { _ = client.Logout() }()
	if err := client.Login(secret.Username, secret.Password); err != nil {
		return nil, fmt.Errorf("Gmail IMAP authentication failed")
	}
	folder := gmailIMAPFolder(secret, query.Folder)
	if _, err := client.Select(folder, true); err != nil {
		return nil, fmt.Errorf("select Gmail IMAP folder %q: %w", folder, err)
	}
	criteria := imap.NewSearchCriteria()
	if query.After != nil {
		criteria.Since = query.After.UTC()
	}
	if query.Before != nil {
		criteria.Before = query.Before.UTC()
	}
	if query.Unread {
		criteria.WithoutFlags = []string{imap.SeenFlag}
	}
	uids, err := client.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("search Gmail IMAP messages: %w", err)
	}
	limit, pageSize, maxPages := normalizeMessageQuery(query)
	scanLimit := pageSize * maxPages
	if scanLimit > len(uids) {
		scanLimit = len(uids)
	}
	selected := append([]uint32(nil), uids[len(uids)-scanLimit:]...)
	sort.Slice(selected, func(i, j int) bool { return selected[i] > selected[j] })
	if len(selected) == 0 {
		return []domain.Message{}, nil
	}
	sequence := new(imap.SeqSet)
	sequence.AddNum(selected...)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}
	fetched := make(chan *imap.Message, len(selected))
	done := make(chan error, 1)
	go func() { done <- client.UidFetch(sequence, items, fetched) }()
	result := make([]domain.Message, 0, limit)
	for item := range fetched {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body := item.GetBody(section)
		if body == nil {
			continue
		}
		message, err := normalizeIMAPMessage(item.Uid, item.Flags, item.InternalDate, body)
		if err != nil {
			continue
		}
		if query.After != nil && message.ReceivedAt.Before(query.After.UTC()) {
			continue
		}
		if query.Before != nil && !message.ReceivedAt.Before(query.Before.UTC()) {
			continue
		}
		if query.RecipientAddress != "" && !MessageMatchesRecipient(message, query.RecipientAddress) {
			continue
		}
		result = append(result, message)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("fetch Gmail IMAP messages: %w", err)
	}
	sortMessages(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func normalizeIMAPMessage(uid uint32, flags []string, internalDate time.Time, body io.Reader) (domain.Message, error) {
	reader, err := mailmessage.CreateReader(io.LimitReader(body, 8<<20))
	if err != nil && !message.IsUnknownCharset(err) {
		return domain.Message{}, err
	}
	headers := make(map[string][]string)
	fields := reader.Header.Fields()
	for fields.Next() {
		name := textproto.CanonicalMIMEHeaderKey(fields.Key())
		value, decodeErr := fields.Text()
		if decodeErr != nil {
			value = fields.Value()
		}
		headers[name] = append(headers[name], value)
	}
	to := extractAddresses(strings.Join(headers["To"], ","))
	cc := extractAddresses(strings.Join(headers["Cc"], ","))
	from := ""
	if values := extractAddresses(strings.Join(headers["From"], ",")); len(values) > 0 {
		from = values[0]
	}
	receivedAt := internalDate.UTC()
	if parsed, dateErr := reader.Header.Date(); dateErr == nil {
		receivedAt = parsed.UTC()
	}
	var textParts, htmlParts []string
	inlineImages := make(map[string]string)
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil && !message.IsUnknownCharset(partErr) {
			break
		}
		contentType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		content, readErr := io.ReadAll(io.LimitReader(part.Body, providerResponseBodyLimit))
		if readErr != nil {
			continue
		}
		switch strings.ToLower(contentType) {
		case "text/plain":
			textParts = append(textParts, string(content))
		case "text/html":
			htmlParts = append(htmlParts, string(content))
		default:
			contentID := strings.Trim(strings.TrimSpace(part.Header.Get("Content-ID")), "<>")
			if contentID != "" && strings.HasPrefix(strings.ToLower(contentType), "image/") {
				inlineImages[contentID] = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(content)
			}
		}
	}
	subject, _ := reader.Header.Subject()
	msg := domain.Message{
		ID:                "imap:" + strconv.FormatUint(uint64(uid), 10),
		InternetMessageID: firstHeader(headers, "Message-Id"),
		From:              from,
		To:                to,
		Cc:                cc,
		Subject:           subject,
		Text:              strings.Join(textParts, "\n"),
		HTML:              replaceCIDImages(strings.Join(htmlParts, "\n"), inlineImages),
		ReceivedAt:        receivedAt,
		Unread:            !containsFold(flags, imap.SeenFlag),
		Headers:           headers,
	}
	msg.RecipientAddresses = ExtractRecipientAddresses(msg.To, msg.Cc, msg.Headers)
	return msg, nil
}

func replaceCIDImages(htmlBody string, images map[string]string) string {
	for contentID, dataURL := range images {
		htmlBody = strings.ReplaceAll(htmlBody, "cid:"+contentID, dataURL)
		htmlBody = strings.ReplaceAll(htmlBody, "cid:<"+contentID+">", dataURL)
	}
	return htmlBody
}

func gmailLabel(folder domain.MessageFolder) string {
	if folder == domain.MessageFolderJunk {
		return "SPAM"
	}
	return "INBOX"
}

func gmailIMAPFolder(secret GmailCredentialSecret, folder domain.MessageFolder) string {
	if folder == domain.MessageFolderJunk {
		if value := strings.TrimSpace(secret.JunkFolder); value != "" {
			return value
		}
		return defaultGmailJunkFolder
	}
	if value := strings.TrimSpace(secret.InboxFolder); value != "" {
		return value
	}
	return defaultGmailInboxFolder
}

func normalizeMessageQuery(query domain.MessageQuery) (limit, pageSize, maxPages int) {
	limit = query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	pageSize = query.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	maxPages = query.MaxPages
	if maxPages <= 0 {
		maxPages = 10
	}
	if maxPages > 50 {
		maxPages = 50
	}
	return limit, pageSize, maxPages
}

func firstHeader(headers map[string][]string, name string) string {
	values := headers[textproto.CanonicalMIMEHeaderKey(name)]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func sortMessages(messages []domain.Message) {
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].ReceivedAt.Equal(messages[j].ReceivedAt) {
			return messages[i].ID > messages[j].ID
		}
		return messages[i].ReceivedAt.After(messages[j].ReceivedAt)
	})
}

func imapDialer(rawProxyURL string) (imapclient.Dialer, error) {
	base := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	rawProxyURL = strings.TrimSpace(rawProxyURL)
	if rawProxyURL == "" {
		return base, nil
	}
	parsed, err := url.Parse(rawProxyURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid IMAP proxy URL", domain.ErrInvalid)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(parsed, base)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid SOCKS proxy configuration", domain.ErrInvalid)
		}
		return dialer, nil
	case "http", "https":
		return &httpConnectDialer{proxyURL: parsed, base: base}, nil
	default:
		return nil, fmt.Errorf("%w: IMAP proxy scheme must be http, https, socks5, or socks5h", domain.ErrInvalid)
	}
}

type httpConnectDialer struct {
	proxyURL         *url.URL
	base             imapclient.Dialer
	proxyTLSConfig   *tls.Config
	handshakeTimeout time.Duration
}

func (d *httpConnectDialer) Dial(network, address string) (net.Conn, error) {
	if d == nil || d.proxyURL == nil {
		return nil, fmt.Errorf("%w: IMAP proxy configuration is required", domain.ErrInvalid)
	}
	proxyAddress := d.proxyURL.Host
	if d.proxyURL.Port() == "" {
		if strings.EqualFold(d.proxyURL.Scheme, "https") {
			proxyAddress = net.JoinHostPort(d.proxyURL.Hostname(), "443")
		} else {
			proxyAddress = net.JoinHostPort(d.proxyURL.Hostname(), "80")
		}
	}
	base := d.base
	if base == nil {
		base = &net.Dialer{Timeout: imapProxyHandshakeTimeout, KeepAlive: 30 * time.Second}
	}
	conn, err := base.Dial(network, proxyAddress)
	if err != nil {
		return nil, fmt.Errorf("connect to IMAP proxy: %w", err)
	}
	handshakeTimeout := imapProxyHandshakeTimeout
	if d.handshakeTimeout > 0 {
		handshakeTimeout = d.handshakeTimeout
	} else if netDialer, ok := base.(*net.Dialer); ok && netDialer.Timeout > 0 {
		handshakeTimeout = netDialer.Timeout
	}
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set IMAP proxy handshake deadline: %w", err)
	}
	if strings.EqualFold(d.proxyURL.Scheme, "https") {
		config := &tls.Config{ServerName: d.proxyURL.Hostname(), MinVersion: tls.VersionTLS12}
		if d.proxyTLSConfig != nil {
			config = d.proxyTLSConfig.Clone()
			if config.ServerName == "" {
				config.ServerName = d.proxyURL.Hostname()
			}
			if config.MinVersion < tls.VersionTLS12 {
				config.MinVersion = tls.VersionTLS12
			}
		}
		tlsConn := tls.Client(conn, config)
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("secure IMAP proxy connection: %w", err)
		}
		conn = tlsConn
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if d.proxyURL.User != nil {
		password, _ := d.proxyURL.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(d.proxyURL.User.Username() + ":" + password))
		request.Header.Set("Proxy-Authorization", "Basic "+credentials)
	}
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write IMAP proxy CONNECT request: %w", err)
	}
	responseReader := bufio.NewReader(conn)
	response, err := http.ReadResponse(responseReader, request)
	if err != nil {
		_ = conn.Close()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("read IMAP proxy CONNECT response: proxy closed before completing HTTP headers: %w", err)
		}
		return nil, fmt.Errorf("read IMAP proxy CONNECT response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("IMAP proxy CONNECT rejected (status %d)", response.StatusCode)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clear IMAP proxy handshake deadline: %w", err)
	}
	return &bufferedNetConn{Conn: conn, reader: responseReader}, nil
}

// http.ReadResponse may read tunnel bytes beyond the CONNECT headers. Keep its
// reader attached to the returned connection so those bytes reach IMAP/TLS.
type bufferedNetConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedNetConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

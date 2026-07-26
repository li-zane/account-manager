package providers

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

const (
	defaultMicrosoftIMAPHost        = "outlook.office365.com"
	defaultMicrosoftIMAPPort        = 993
	defaultMicrosoftIMAPInboxFolder = "INBOX"
	defaultMicrosoftIMAPJunkFolder  = "Junk"
)

type microsoftIMAPFetchFunc func(context.Context, domain.MicrosoftCredentialSecret, domain.MessageQuery) ([]domain.Message, error)

// retrieveMicrosoftIMAP uses implicit TLS and the XOAUTH2 SASL mechanism.
// It scans a bounded newest-UID window; exact recipient filtering is applied
// by the adapter after normalization so injected and network executors share
// the same isolation rule.
func retrieveMicrosoftIMAP(ctx context.Context, secret domain.MicrosoftCredentialSecret, query domain.MessageQuery) ([]domain.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	username := strings.TrimSpace(secret.IMAPUsername)
	accessToken := secret.IMAPAccessToken
	if username == "" || strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("%w: Microsoft IMAP username and access token are required", domain.ErrInvalid)
	}
	host := strings.TrimSpace(secret.IMAPHost)
	if host == "" {
		host = defaultMicrosoftIMAPHost
	}
	port := secret.IMAPPort
	if port == 0 {
		port = defaultMicrosoftIMAPPort
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: invalid Microsoft IMAP port", domain.ErrInvalid)
	}
	dialer, err := imapDialer(secret.IMAPProxyURL)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := imapclient.DialWithDialerTLS(dialer, address, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to Microsoft IMAP: %w", err)
	}
	client.ErrorLog = log.New(io.Discard, "", 0)
	client.Timeout = 25 * time.Second
	defer func() { _ = client.Logout() }()

	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Terminate()
		case <-watchDone:
		}
	}()

	if err := client.Authenticate(&microsoftXOAUTH2Client{username: username, accessToken: accessToken}); err != nil {
		return nil, fmt.Errorf("%w: Microsoft IMAP authentication failed", domain.ErrUnauthorized)
	}
	if _, err := selectMicrosoftIMAPFolder(client, secret, query.Folder); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("search Microsoft IMAP messages: %w", err)
	}
	_, pageSize, maxPages := normalizeMicrosoftIMAPQuery(query)
	scanLimit := pageSize * maxPages
	if scanLimit > len(uids) {
		scanLimit = len(uids)
	}
	if scanLimit == 0 {
		return []domain.Message{}, nil
	}
	selected := append([]uint32(nil), uids[len(uids)-scanLimit:]...)
	sequence := new(imap.SeqSet)
	sequence.AddNum(selected...)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}
	fetched := make(chan *imap.Message, len(selected))
	done := make(chan error, 1)
	go func() { done <- client.UidFetch(sequence, items, fetched) }()

	result := make([]domain.Message, 0, len(selected))
	for item := range fetched {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body := item.GetBody(section)
		if body == nil {
			continue
		}
		message, normalizeErr := normalizeIMAPMessage(item.Uid, item.Flags, item.InternalDate, body)
		if normalizeErr != nil {
			continue
		}
		result = append(result, message)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("fetch Microsoft IMAP messages: %w", err)
	}
	return result, nil
}

func selectMicrosoftIMAPFolder(client *imapclient.Client, secret domain.MicrosoftCredentialSecret, folder domain.MessageFolder) (string, error) {
	name, _, err := selectMicrosoftIMAPFolderStatus(client, secret, folder)
	return name, err
}

func selectMicrosoftIMAPFolderStatus(client *imapclient.Client, secret domain.MicrosoftCredentialSecret, folder domain.MessageFolder) (string, *imap.MailboxStatus, error) {
	var candidates []string
	switch folder {
	case "", domain.MessageFolderInbox:
		candidates = []string{firstNonEmpty(secret.IMAPInboxFolder, defaultMicrosoftIMAPInboxFolder)}
	case domain.MessageFolderJunk:
		candidates = []string{firstNonEmpty(secret.IMAPJunkFolder, defaultMicrosoftIMAPJunkFolder)}
		if secret.IMAPJunkFolder == "" && candidates[0] != "Junk Email" {
			candidates = append(candidates, "Junk Email")
		}
	default:
		return "", nil, fmt.Errorf("%w: unsupported Microsoft IMAP folder %q", domain.ErrInvalid, folder)
	}
	for _, candidate := range candidates {
		if status, err := client.Select(candidate, true); err == nil {
			return candidate, status, nil
		}
	}
	return "", nil, fmt.Errorf("Microsoft IMAP folder selection failed")
}

func syncMicrosoftIMAP(ctx context.Context, secret domain.MicrosoftCredentialSecret, request domain.MessageSyncRequest) (domain.MessageSyncResult, error) {
	username := strings.TrimSpace(secret.IMAPUsername)
	if username == "" || strings.TrimSpace(secret.IMAPAccessToken) == "" {
		return domain.MessageSyncResult{}, fmt.Errorf("%w: Microsoft IMAP username and access token are required", domain.ErrInvalid)
	}
	host := firstNonEmpty(secret.IMAPHost, defaultMicrosoftIMAPHost)
	port := secret.IMAPPort
	if port == 0 {
		port = defaultMicrosoftIMAPPort
	}
	dialer, err := imapDialer(secret.IMAPProxyURL)
	if err != nil {
		return domain.MessageSyncResult{}, err
	}
	client, err := imapclient.DialWithDialerTLS(dialer, net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return domain.MessageSyncResult{}, fmt.Errorf("connect to Microsoft IMAP: %w", err)
	}
	client.ErrorLog = log.New(io.Discard, "", 0)
	client.Timeout = 25 * time.Second
	defer func() { _ = client.Logout() }()
	doneWatch := make(chan struct{})
	defer close(doneWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Terminate()
		case <-doneWatch:
		}
	}()
	if err := client.Authenticate(&microsoftXOAUTH2Client{username: username, accessToken: secret.IMAPAccessToken}); err != nil {
		return domain.MessageSyncResult{}, fmt.Errorf("%w: Microsoft IMAP authentication failed", domain.ErrUnauthorized)
	}
	_, status, err := selectMicrosoftIMAPFolderStatus(client, secret, request.Folder)
	if err != nil {
		return domain.MessageSyncResult{}, err
	}
	uidValidity := status.UidValidity
	highestUID := request.HighestUID
	if request.UIDValidity != 0 && request.UIDValidity != uidValidity {
		highestUID = 0
	}
	criteria := imap.NewSearchCriteria()
	if highestUID > 0 {
		set := new(imap.SeqSet)
		set.AddRange(highestUID+1, 0)
		criteria.Uid = set
	}
	uids, err := client.UidSearch(criteria)
	if err != nil {
		return domain.MessageSyncResult{}, fmt.Errorf("search Microsoft IMAP messages: %w", err)
	}
	limit := request.Limit
	if limit <= 0 || limit > maxMicrosoftMessageLimit {
		limit = maxMicrosoftMessageLimit
	}
	complete := len(uids) <= limit
	if len(uids) > limit {
		uids = uids[:limit]
	}
	result := domain.MessageSyncResult{UIDValidity: uidValidity, HighestUID: highestUID, Complete: complete}
	if len(uids) == 0 {
		return result, nil
	}
	sequence := new(imap.SeqSet)
	sequence.AddNum(uids...)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}
	fetched := make(chan *imap.Message, len(uids))
	fetchDone := make(chan error, 1)
	go func() { fetchDone <- client.UidFetch(sequence, items, fetched) }()
	for item := range fetched {
		if item.Uid > result.HighestUID {
			result.HighestUID = item.Uid
		}
		body := item.GetBody(section)
		if body == nil {
			continue
		}
		message, normalizeErr := normalizeIMAPMessage(item.Uid, item.Flags, item.InternalDate, body)
		if normalizeErr == nil {
			result.Messages = append(result.Messages, message)
		}
	}
	if err := <-fetchDone; err != nil {
		return domain.MessageSyncResult{}, fmt.Errorf("fetch Microsoft IMAP messages: %w", err)
	}
	return result, nil
}

type microsoftXOAUTH2Client struct {
	username    string
	accessToken string
	challenged  bool
}

func (c *microsoftXOAUTH2Client) Start() (string, []byte, error) {
	if strings.TrimSpace(c.username) == "" || strings.TrimSpace(c.accessToken) == "" ||
		strings.ContainsAny(c.username, "\x00\x01\r\n") || strings.ContainsAny(c.accessToken, "\x00\x01\r\n") {
		return "", nil, fmt.Errorf("%w: invalid Microsoft XOAUTH2 credential", domain.ErrInvalid)
	}
	initial := "user=" + c.username + "\x01auth=Bearer " + c.accessToken + "\x01\x01"
	return "XOAUTH2", []byte(initial), nil
}

func (c *microsoftXOAUTH2Client) Next([]byte) ([]byte, error) {
	if c.challenged {
		return nil, errors.New("Microsoft XOAUTH2 authentication challenge repeated")
	}
	c.challenged = true
	// Microsoft sends a JSON challenge only on authentication failure. An
	// empty response completes that exchange without reflecting its content.
	return []byte{}, nil
}

func microsoftRetrievalMethod(kind domain.CredentialKind, requested domain.RetrievalMethod) (domain.RetrievalMethod, error) {
	switch kind {
	case domain.CredentialMicrosoftGraphOAuth:
		if requested == "" || requested == domain.RetrievalMicrosoftGraph {
			return domain.RetrievalMicrosoftGraph, nil
		}
	case domain.CredentialMicrosoftIMAPOAuth:
		if requested == "" || requested == domain.RetrievalIMAPOAuth {
			return domain.RetrievalIMAPOAuth, nil
		}
	case domain.CredentialMicrosoftDualToken:
		if requested == "" || requested == domain.RetrievalMicrosoftGraph {
			return domain.RetrievalMicrosoftGraph, nil
		}
		if requested == domain.RetrievalIMAPOAuth {
			return domain.RetrievalIMAPOAuth, nil
		}
	default:
		return "", fmt.Errorf("%w: unsupported Microsoft credential kind %q", domain.ErrInvalid, kind)
	}
	return "", fmt.Errorf("%w: retrieval method %q is incompatible with Microsoft credential kind %q", domain.ErrInvalid, requested, kind)
}

func imapAccessToken(secret domain.MicrosoftCredentialSecret) (string, *time.Time) {
	if strings.TrimSpace(secret.IMAPAccessToken) != "" {
		return secret.IMAPAccessToken, secret.IMAPTokenExpiresAt
	}
	if secret.AccessTokenMethod == domain.RetrievalIMAPOAuth && strings.TrimSpace(secret.AccessToken) != "" {
		return secret.AccessToken, secret.AccessTokenExpiresAt
	}
	return "", nil
}

func (a MicrosoftAdapter) validateAccessToken(token string, methodExpiresAt, credentialExpiresAt *time.Time, label string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("%w: %s access token is missing; refresh is required", domain.ErrUnauthorized, label)
	}
	expiresAt := methodExpiresAt
	if expiresAt == nil {
		expiresAt = credentialExpiresAt
	}
	if expiresAt != nil && !expiresAt.After(a.clock()()) {
		return fmt.Errorf("%w: %s access token has expired; refresh is required", domain.ErrUnauthorized, label)
	}
	return nil
}

func filterMicrosoftIMAPMessages(messages []domain.Message, query domain.MessageQuery) []domain.Message {
	limit, _, _ := normalizeMicrosoftIMAPQuery(query)
	filtered := make([]domain.Message, 0, min(len(messages), limit))
	for _, message := range messages {
		if query.After != nil && message.ReceivedAt.Before(query.After.UTC()) {
			continue
		}
		if query.Unread && !message.Unread {
			continue
		}
		if query.RecipientAddress != "" && !MessageMatchesRecipient(message, query.RecipientAddress) {
			continue
		}
		filtered = append(filtered, message)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].ReceivedAt.Equal(filtered[j].ReceivedAt) {
			return filtered[i].ID > filtered[j].ID
		}
		return filtered[i].ReceivedAt.After(filtered[j].ReceivedAt)
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

func normalizeMicrosoftIMAPQuery(query domain.MessageQuery) (limit, pageSize, maxPages int) {
	limit = query.Limit
	if limit <= 0 {
		limit = defaultMicrosoftPageSize
	}
	if limit > maxMicrosoftMessageLimit {
		limit = maxMicrosoftMessageLimit
	}
	pageSize = query.PageSize
	if pageSize <= 0 {
		pageSize = defaultMicrosoftPageSize
	}
	if pageSize > maxMicrosoftPageSize {
		pageSize = maxMicrosoftPageSize
	}
	maxPages = query.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMicrosoftMaxPages
	}
	if maxPages > maxMicrosoftPages {
		maxPages = maxMicrosoftPages
	}
	return limit, pageSize, maxPages
}

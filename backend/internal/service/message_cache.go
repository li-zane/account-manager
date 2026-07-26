package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const (
	defaultCachedMessageLimit = 50
	maxCachedMessageLimit     = 200
	messageSyncOverlap        = time.Second
	fallbackBeforeCursor      = "time-before:"
)

type MessageCacheService struct {
	repository ports.MessageCacheRepository
	mailboxes  ports.MailboxRepository
	retrieval  *MessageRetrievalService
	clock      func() time.Time
	locks      sync.Map
}

type CachedMessagesInput struct {
	MailboxID       string
	AliasID         string
	Folder          domain.MessageFolder
	RetrievalMethod domain.RetrievalMethod
	Limit           int
	Offset          int
}

type CachedMessagesResult struct {
	Messages []domain.CachedMessage   `json:"messages"`
	Count    int                      `json:"count"`
	Sync     *domain.MessageSyncState `json:"sync,omitempty"`
	NewCount int                      `json:"new_count,omitempty"`
	Complete bool                     `json:"complete"`
}

type PurgeCachedMessagesInput struct {
	MailboxID   string
	Folder      domain.MessageFolder
	Before      *time.Time
	Limit       int
	ResetCursor bool
}

type ManageCachedMessagesInput struct {
	MailboxID string
	Folder    domain.MessageFolder
	After     *time.Time
	Before    *time.Time
	Search    string
	Limit     int
	Offset    int
}

type ManagedCachedMessagesResult struct {
	Messages []domain.CachedMessage `json:"messages"`
	Count    int64                  `json:"count"`
}

func (s *MessageCacheService) RestoreRange(ctx context.Context, input ManageCachedMessagesInput) (int, error) {
	if strings.TrimSpace(input.MailboxID) == "" {
		return 0, fmt.Errorf("%w: mailbox id is required", domain.ErrInvalid)
	}
	if err := validateCacheRange(input.After, input.Before, true); err != nil {
		return 0, err
	}
	mailbox, err := s.mailboxes.GetMailbox(ctx, strings.TrimSpace(input.MailboxID))
	if err != nil {
		return 0, err
	}
	folder, err := normalizeMessageFolder(input.Folder)
	if err != nil {
		return 0, err
	}
	messages, err := s.retrieveCompleteRange(ctx, mailbox.ID, folder, input.After.UTC(), input.Before.UTC(), 0)
	if err != nil {
		return 0, err
	}
	now := s.clock().UTC()
	cached := make([]domain.CachedMessage, 0, len(messages))
	for _, retrieved := range messages {
		item, ok := cachedMessageFrom(mailbox.ID, folder, retrieved.method, retrieved.message, now)
		if ok {
			cached = append(cached, item)
		}
	}
	return s.repository.UpsertCachedMessages(ctx, cached)
}

type rangeRetrievedMessage struct {
	message domain.Message
	method  domain.RetrievalMethod
}

func (s *MessageCacheService) retrieveCompleteRange(ctx context.Context, mailboxID string, folder domain.MessageFolder, after, before time.Time, depth int) ([]rangeRetrievedMessage, error) {
	const rangeLimit = 500
	items, method, err := s.retrieval.RetrieveWithMethod(ctx, RetrieveMessagesInput{MailboxID: mailboxID, AllRecipients: true, Query: domain.MessageQuery{
		Folder: folder, After: &after, Before: &before, Limit: rangeLimit, PageSize: 50, MaxPages: 20,
	}})
	if err != nil {
		return nil, err
	}
	retrieved := make([]rangeRetrievedMessage, 0, len(items))
	for _, item := range items {
		retrieved = append(retrieved, rangeRetrievedMessage{message: item, method: method})
	}
	if len(items) < rangeLimit || depth >= 20 || before.Sub(after) <= time.Second {
		return retrieved, nil
	}
	middle := after.Add(before.Sub(after) / 2)
	older, err := s.retrieveCompleteRange(ctx, mailboxID, folder, after, middle, depth+1)
	if err != nil {
		return nil, err
	}
	newer, err := s.retrieveCompleteRange(ctx, mailboxID, folder, middle, before, depth+1)
	if err != nil {
		return nil, err
	}
	return append(older, newer...), nil
}

func (s *MessageCacheService) QueryManaged(ctx context.Context, input ManageCachedMessagesInput) (ManagedCachedMessagesResult, error) {
	if err := validateCacheRange(input.After, input.Before, false); err != nil {
		return ManagedCachedMessagesResult{}, err
	}
	items, count, err := s.repository.QueryCachedMessages(ctx, ports.MessageCacheFilter{
		MailboxID: strings.TrimSpace(input.MailboxID), Folder: input.Folder, After: input.After, Before: input.Before, Search: input.Search,
	}, ports.ListOptions{Limit: input.Limit, Offset: input.Offset})
	return ManagedCachedMessagesResult{Messages: items, Count: count}, err
}

func (s *MessageCacheService) DeleteManaged(ctx context.Context, input ManageCachedMessagesInput) (int, error) {
	if err := validateCacheRange(input.After, input.Before, true); err != nil {
		return 0, err
	}
	return s.repository.DeleteCachedMessagesRange(ctx, ports.MessageCacheFilter{
		MailboxID: strings.TrimSpace(input.MailboxID), Folder: input.Folder, After: input.After, Before: input.Before,
	}, input.Limit)
}

func validateCacheRange(after, before *time.Time, required bool) error {
	if required && (after == nil || before == nil) {
		return fmt.Errorf("%w: cache deletion requires after and before", domain.ErrInvalid)
	}
	if after != nil && before != nil && !after.Before(*before) {
		return fmt.Errorf("%w: cache range after must be earlier than before", domain.ErrInvalid)
	}
	return nil
}

func (s *MessageCacheService) Purge(ctx context.Context, input PurgeCachedMessagesInput) (int, error) {
	mailboxID := strings.TrimSpace(input.MailboxID)
	if mailboxID == "" {
		return 0, fmt.Errorf("%w: mailbox id is required", domain.ErrInvalid)
	}
	if _, err := s.mailboxes.GetMailbox(ctx, mailboxID); err != nil {
		return 0, err
	}
	folder := input.Folder
	if folder != "" {
		var err error
		folder, err = normalizeMessageFolder(folder)
		if err != nil {
			return 0, err
		}
	}
	deleted, err := s.repository.PurgeCachedMessages(ctx, mailboxID, folder, input.Before, input.Limit)
	if err != nil {
		return 0, err
	}
	if input.ResetCursor {
		if folder == "" {
			for _, item := range []domain.MessageFolder{domain.MessageFolderInbox, domain.MessageFolderJunk} {
				if err := s.repository.ResetMessageSyncState(ctx, mailboxID, item); err != nil {
					return deleted, err
				}
			}
		} else if err := s.repository.ResetMessageSyncState(ctx, mailboxID, folder); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func NewMessageCacheService(repository ports.MessageCacheRepository, mailboxes ports.MailboxRepository, retrieval *MessageRetrievalService) (*MessageCacheService, error) {
	if repository == nil || mailboxes == nil || retrieval == nil {
		return nil, fmt.Errorf("%w: message cache dependencies are required", domain.ErrInvalid)
	}
	return &MessageCacheService{repository: repository, mailboxes: mailboxes, retrieval: retrieval, clock: time.Now}, nil
}

func (s *MessageCacheService) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

func (s *MessageCacheService) LastMessageAt(ctx context.Context, targetID string) (*time.Time, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return nil, fmt.Errorf("%w: message cache target id is required", domain.ErrInvalid)
	}
	var latest *time.Time
	for _, folder := range []domain.MessageFolder{domain.MessageFolderInbox, domain.MessageFolderJunk} {
		state, err := s.repository.GetMessageSyncState(ctx, targetID, folder)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if state.LastMessageAt != nil && (latest == nil || state.LastMessageAt.After(*latest)) {
			value := state.LastMessageAt.UTC()
			latest = &value
		}
	}
	return latest, nil
}

func (s *MessageCacheService) List(ctx context.Context, input CachedMessagesInput) (CachedMessagesResult, error) {
	target, err := s.resolveTarget(ctx, input)
	if err != nil {
		return CachedMessagesResult{}, err
	}
	folder, err := normalizeMessageFolder(input.Folder)
	if err != nil {
		return CachedMessagesResult{}, err
	}
	options := ports.ListOptions{Limit: input.Limit, Offset: input.Offset}.Normalize(defaultCachedMessageLimit, maxCachedMessageLimit)
	messages, err := s.repository.ListCachedMessages(ctx, target.mailboxID, folder, target.recipientAddress, options)
	if err != nil {
		return CachedMessagesResult{}, err
	}
	state, err := s.repository.GetMessageSyncState(ctx, target.targetID, folder)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return CachedMessagesResult{}, err
	}
	result := CachedMessagesResult{Messages: messages, Count: len(messages)}
	if err == nil {
		result.Sync = &state
	}
	return result, nil
}

func (s *MessageCacheService) MarkViewed(ctx context.Context, mailboxID, messageID string) error {
	mailboxID = strings.TrimSpace(mailboxID)
	messageID = strings.TrimSpace(messageID)
	if mailboxID == "" || messageID == "" {
		return fmt.Errorf("%w: mailbox id and message id are required", domain.ErrInvalid)
	}
	if _, err := s.mailboxes.GetMailbox(ctx, mailboxID); err != nil {
		return err
	}
	return s.repository.MarkCachedMessageViewed(ctx, mailboxID, messageID, s.clock().UTC())
}

func (s *MessageCacheService) Sync(ctx context.Context, input CachedMessagesInput) (CachedMessagesResult, error) {
	target, err := s.resolveTarget(ctx, input)
	if err != nil {
		return CachedMessagesResult{}, err
	}
	folder, err := normalizeMessageFolder(input.Folder)
	if err != nil {
		return CachedMessagesResult{}, err
	}
	lockValue, _ := s.locks.LoadOrStore(target.targetID+"\x00"+string(folder), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	now := s.clock().UTC()
	state, stateErr := s.repository.GetMessageSyncState(ctx, target.targetID, folder)
	if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
		return CachedMessagesResult{}, stateErr
	}
	if stateErr != nil {
		state = domain.MessageSyncState{TargetID: target.targetID, MailboxID: target.mailboxID, AliasID: target.aliasID, Folder: folder}
	}
	query := domain.MessageQuery{Folder: folder, RetrievalMethod: input.RetrievalMethod, Limit: 500, PageSize: 50, MaxPages: 20}
	var messages []domain.Message
	var deletedProviderIDs []string
	method := input.RetrievalMethod
	complete := false
	incremental, err := s.retrieval.SyncIncremental(ctx, target.mailboxID, domain.MessageSyncRequest{
		Method: method, Folder: folder, Cursor: state.Cursor, UIDValidity: state.UIDValidity,
		HighestUID: state.HighestUID, Limit: 500, PageSize: 50, MaxPages: 20,
	})
	if errors.Is(err, domain.ErrSyncCursorInvalid) {
		state.Cursor, state.UIDValidity, state.HighestUID = "", 0, 0
		incremental, err = s.retrieval.SyncIncremental(ctx, target.mailboxID, domain.MessageSyncRequest{Method: method, Folder: folder, Limit: 500, PageSize: 50, MaxPages: 20})
	}
	if errors.Is(err, domain.ErrNotConfigured) {
		if before, ok := fallbackCursorTime(state.Cursor); ok {
			query.Before = &before
		} else if state.LastMessageAt != nil {
			after := state.LastMessageAt.Add(-messageSyncOverlap)
			query.After = &after
		}
		if target.aliasID != "" {
			query.RecipientAddress = target.recipientAddress
		}
		messages, err = s.retrieval.Retrieve(ctx, RetrieveMessagesInput{MailboxID: target.mailboxID, AllRecipients: target.aliasID == "", Query: query})
		complete = len(messages) < query.Limit
		if complete {
			state.Cursor = ""
		} else if oldest, ok := oldestMessageTime(messages); ok {
			state.Cursor = fallbackBeforeCursor + oldest.UTC().Format(time.RFC3339Nano)
		}
	} else if err == nil {
		messages, deletedProviderIDs, method = incremental.Messages, incremental.DeletedProviderMessageIDs, incremental.Method
		state.Cursor, state.UIDValidity, state.HighestUID = incremental.Cursor, incremental.UIDValidity, incremental.HighestUID
		complete = incremental.Complete
	}
	state.LastSyncedAt = now
	if err != nil {
		state.LastError = messageSyncError(err)
		_ = s.repository.SaveMessageSyncState(context.WithoutCancel(ctx), state)
		return CachedMessagesResult{}, err
	}
	cached := make([]domain.CachedMessage, 0, len(messages))
	for _, message := range messages {
		if target.aliasID != "" && !cachedMessageMatchesRecipient(message, target.recipientAddress) {
			continue
		}
		item, ok := cachedMessageFrom(target.mailboxID, folder, method, message, now)
		if !ok {
			continue
		}
		cached = append(cached, item)
		if state.LastMessageAt == nil || item.ReceivedAt.After(*state.LastMessageAt) {
			receivedAt := item.ReceivedAt
			state.LastMessageAt = &receivedAt
		}
	}
	newCount, err := s.repository.UpsertCachedMessages(ctx, cached)
	if err != nil {
		return CachedMessagesResult{}, err
	}
	if _, err := s.repository.DeleteCachedMessages(ctx, target.mailboxID, folder, method, deletedProviderIDs); err != nil {
		return CachedMessagesResult{}, err
	}
	state.RetrievalMethod = method
	state.LastError = ""
	if err := s.repository.SaveMessageSyncState(ctx, state); err != nil {
		return CachedMessagesResult{}, err
	}
	result, err := s.List(ctx, input)
	result.NewCount = newCount
	result.Complete = complete
	return result, err
}

func fallbackCursorTime(cursor string) (time.Time, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(cursor), fallbackBeforeCursor)
	if !ok {
		return time.Time{}, false
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	return value.UTC(), err == nil
}

func oldestMessageTime(messages []domain.Message) (time.Time, bool) {
	var oldest time.Time
	for _, item := range messages {
		if item.ReceivedAt.IsZero() || (!oldest.IsZero() && !item.ReceivedAt.Before(oldest)) {
			continue
		}
		oldest = item.ReceivedAt
	}
	return oldest, !oldest.IsZero()
}

func cachedMessageMatchesRecipient(message domain.Message, recipient string) bool {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	for _, value := range message.RecipientAddresses {
		if strings.EqualFold(strings.TrimSpace(value), recipient) {
			return true
		}
	}
	return false
}

type cacheTarget struct {
	targetID         string
	mailboxID        string
	mailboxInputID   string
	aliasID          string
	recipientAddress string
}

func (s *MessageCacheService) resolveTarget(ctx context.Context, input CachedMessagesInput) (cacheTarget, error) {
	mailboxID := strings.TrimSpace(input.MailboxID)
	aliasID := strings.TrimSpace(input.AliasID)
	if (mailboxID == "") == (aliasID == "") {
		return cacheTarget{}, fmt.Errorf("%w: exactly one mailbox or alias id is required", domain.ErrInvalid)
	}
	if aliasID != "" {
		alias, err := s.mailboxes.GetAlias(ctx, aliasID)
		if err != nil {
			return cacheTarget{}, err
		}
		return cacheTarget{targetID: alias.MailboxID, mailboxID: alias.MailboxID, mailboxInputID: alias.MailboxID, aliasID: alias.ID, recipientAddress: alias.NormalizedAddress}, nil
	}
	if _, err := s.mailboxes.GetMailbox(ctx, mailboxID); err != nil {
		return cacheTarget{}, err
	}
	return cacheTarget{targetID: mailboxID, mailboxID: mailboxID, mailboxInputID: mailboxID}, nil
}

func normalizeMessageFolder(folder domain.MessageFolder) (domain.MessageFolder, error) {
	if folder == "" {
		return domain.MessageFolderInbox, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(folder))) {
	case "inbox":
		return domain.MessageFolderInbox, nil
	case "junk", "junkemail", "spam":
		return domain.MessageFolderJunk, nil
	default:
		return "", fmt.Errorf("%w: message folder must be INBOX or Junk", domain.ErrInvalid)
	}
}

func cachedMessageFrom(mailboxID string, folder domain.MessageFolder, method domain.RetrievalMethod, message domain.Message, now time.Time) (domain.CachedMessage, bool) {
	externalID := strings.TrimSpace(strings.ToValidUTF8(message.InternetMessageID, "\uFFFD"))
	if externalID != "" {
		externalID = strings.ToLower(externalID)
	} else {
		externalID = strings.TrimSpace(strings.ToValidUTF8(message.ID, "\uFFFD"))
	}
	if externalID == "" || message.ReceivedAt.IsZero() {
		return domain.CachedMessage{}, false
	}
	digest := sha256.Sum256([]byte(mailboxID + "\x00" + string(folder) + "\x00" + externalID))
	return domain.CachedMessage{
		ID: "msg_" + hex.EncodeToString(digest[:16]), MailboxID: mailboxID, ExternalID: externalID,
		ProviderMessageID: strings.TrimSpace(validUTF8(message.ID)), RetrievalMethod: method, InternetMessageID: strings.TrimSpace(validUTF8(message.InternetMessageID)),
		Folder: folder, From: strings.TrimSpace(validUTF8(message.From)), To: validUTF8Strings(message.To),
		Cc: validUTF8Strings(message.Cc), RecipientAddresses: normalizedAddresses(message.RecipientAddresses),
		Subject: validUTF8(message.Subject), Text: validUTF8(message.Text), HTML: validUTF8(message.HTML), ReceivedAt: message.ReceivedAt.UTC(),
		Unread: message.Unread, Headers: validUTF8Headers(message.Headers), DiscoveredAt: now, UpdatedAt: now,
	}, true
}

func validUTF8(value string) string { return strings.ToValidUTF8(value, "\uFFFD") }

func validUTF8Strings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, validUTF8(value))
	}
	return result
}

func validUTF8Headers(headers map[string][]string) map[string][]string {
	result := make(map[string][]string, len(headers))
	for name, values := range headers {
		result[validUTF8(name)] = validUTF8Strings(values)
	}
	return result
}

func normalizedAddresses(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(validUTF8(value)))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func messageSyncError(err error) string {
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, domain.ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "sync_failed"
	}
}

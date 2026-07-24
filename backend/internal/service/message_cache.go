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
	if state.LastMessageAt != nil {
		after := state.LastMessageAt.Add(-messageSyncOverlap)
		query.After = &after
	}
	messages, err := s.retrieval.Retrieve(ctx, RetrieveMessagesInput{MailboxID: target.mailboxInputID, AliasID: target.aliasID, Query: query})
	state.LastSyncedAt = now
	if err != nil {
		state.LastError = messageSyncError(err)
		_ = s.repository.SaveMessageSyncState(context.WithoutCancel(ctx), state)
		return CachedMessagesResult{}, err
	}
	cached := make([]domain.CachedMessage, 0, len(messages))
	for _, message := range messages {
		item, ok := cachedMessageFrom(target.mailboxID, folder, message, now)
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
	state.LastError = ""
	if err := s.repository.SaveMessageSyncState(ctx, state); err != nil {
		return CachedMessagesResult{}, err
	}
	result, err := s.List(ctx, input)
	result.NewCount = newCount
	return result, err
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
		return cacheTarget{targetID: alias.ID, mailboxID: alias.MailboxID, aliasID: alias.ID, recipientAddress: alias.NormalizedAddress}, nil
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

func cachedMessageFrom(mailboxID string, folder domain.MessageFolder, message domain.Message, now time.Time) (domain.CachedMessage, bool) {
	externalID := strings.TrimSpace(message.InternetMessageID)
	if externalID != "" {
		externalID = strings.ToLower(externalID)
	} else {
		externalID = strings.TrimSpace(message.ID)
	}
	if externalID == "" || message.ReceivedAt.IsZero() {
		return domain.CachedMessage{}, false
	}
	digest := sha256.Sum256([]byte(mailboxID + "\x00" + string(folder) + "\x00" + externalID))
	return domain.CachedMessage{
		ID: "msg_" + hex.EncodeToString(digest[:16]), MailboxID: mailboxID, ExternalID: externalID,
		ProviderMessageID: strings.TrimSpace(message.ID), InternetMessageID: strings.TrimSpace(message.InternetMessageID),
		Folder: folder, From: strings.TrimSpace(message.From), To: append([]string(nil), message.To...),
		Cc: append([]string(nil), message.Cc...), RecipientAddresses: normalizedAddresses(message.RecipientAddresses),
		Subject: message.Subject, Text: message.Text, HTML: message.HTML, ReceivedAt: message.ReceivedAt.UTC(),
		Unread: message.Unread, Headers: domain.CloneMessageHeaders(message.Headers), DiscoveredAt: now, UpdatedAt: now,
	}, true
}

func normalizedAddresses(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
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

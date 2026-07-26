package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

func (s *Store) UpsertCachedMessages(ctx context.Context, messages []domain.CachedMessage) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := 0
	for _, message := range messages {
		if _, exists := s.mailboxes[message.MailboxID]; !exists {
			return 0, fmt.Errorf("%w: mailbox %q", domain.ErrNotFound, message.MailboxID)
		}
		key := cachedMessageIdentity(message.MailboxID, message.Folder, message.ExternalID)
		current, exists := s.cachedMessages[key]
		if !exists {
			changed++
		} else if current.ViewedAt != nil && message.ViewedAt == nil {
			message.ViewedAt = cloneTime(current.ViewedAt)
		}
		s.cachedMessages[key] = cloneCachedMessage(message)
	}
	return changed, nil
}

func (s *Store) ListCachedMessages(ctx context.Context, mailboxID string, folder domain.MessageFolder, recipientAddress string, options ports.ListOptions) ([]domain.CachedMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	recipientAddress = strings.ToLower(strings.TrimSpace(recipientAddress))
	s.mu.RLock()
	items := make([]domain.CachedMessage, 0)
	for _, message := range s.cachedMessages {
		if message.MailboxID != mailboxID || message.Folder != folder || (recipientAddress != "" && !stringSliceContains(message.RecipientAddresses, recipientAddress)) {
			continue
		}
		items = append(items, cloneCachedMessage(message))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].ReceivedAt.Equal(items[j].ReceivedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].ReceivedAt.After(items[j].ReceivedAt)
	})
	return paginate(items, options), nil
}

func (s *Store) GetMessageSyncState(ctx context.Context, targetID string, folder domain.MessageFolder) (domain.MessageSyncState, error) {
	if err := ctx.Err(); err != nil {
		return domain.MessageSyncState{}, err
	}
	s.mu.RLock()
	state, exists := s.messageSyncStates[messageSyncIdentity(targetID, folder)]
	s.mu.RUnlock()
	if !exists {
		return domain.MessageSyncState{}, fmt.Errorf("%w: message sync state", domain.ErrNotFound)
	}
	state.LastMessageAt = cloneTime(state.LastMessageAt)
	return state, nil
}

func (s *Store) SaveMessageSyncState(ctx context.Context, state domain.MessageSyncState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := messageSyncIdentity(state.TargetID, state.Folder)
	if current, exists := s.messageSyncStates[key]; exists && current.LastMessageAt != nil && (state.LastMessageAt == nil || current.LastMessageAt.After(*state.LastMessageAt)) {
		state.LastMessageAt = cloneTime(current.LastMessageAt)
	}
	state.LastMessageAt = cloneTime(state.LastMessageAt)
	s.messageSyncStates[key] = state
	return nil
}

func cachedMessageIdentity(mailboxID string, folder domain.MessageFolder, externalID string) string {
	return mailboxID + "\x00" + string(folder) + "\x00" + externalID
}

func messageSyncIdentity(targetID string, folder domain.MessageFolder) string {
	return targetID + "\x00" + string(folder)
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func cloneCachedMessage(value domain.CachedMessage) domain.CachedMessage {
	value.To = append([]string(nil), value.To...)
	value.Cc = append([]string(nil), value.Cc...)
	value.RecipientAddresses = append([]string(nil), value.RecipientAddresses...)
	value.Headers = domain.CloneMessageHeaders(value.Headers)
	value.ViewedAt = cloneTime(value.ViewedAt)
	return value
}

func (s *Store) MarkCachedMessageViewed(ctx context.Context, mailboxID, messageID string, viewedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, message := range s.cachedMessages {
		if message.MailboxID != mailboxID || message.ID != messageID {
			continue
		}
		if message.ViewedAt == nil {
			value := viewedAt.UTC()
			message.ViewedAt = &value
			message.UpdatedAt = value
			s.cachedMessages[key] = message
		}
		return nil
	}
	return fmt.Errorf("%w: cached message", domain.ErrNotFound)
}

func (s *Store) DeleteCachedMessages(ctx context.Context, mailboxID string, folder domain.MessageFolder, method domain.RetrievalMethod, providerIDs []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ids := make(map[string]struct{}, len(providerIDs))
	for _, id := range providerIDs {
		ids[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for key, message := range s.cachedMessages {
		_, matchesID := ids[message.ProviderMessageID]
		if message.MailboxID == mailboxID && message.Folder == folder && (method == "" || message.RetrievalMethod == method) && matchesID {
			delete(s.cachedMessages, key)
			deleted++
		}
	}
	return deleted, nil
}

func (s *Store) PurgeCachedMessages(ctx context.Context, mailboxID string, folder domain.MessageFolder, before *time.Time, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	limit = ports.ListOptions{Limit: limit}.Normalize(500, 5000).Limit
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for key, message := range s.cachedMessages {
		if deleted >= limit {
			break
		}
		if message.MailboxID == mailboxID && (folder == "" || message.Folder == folder) && (before == nil || message.ReceivedAt.Before(*before)) {
			delete(s.cachedMessages, key)
			deleted++
		}
	}
	return deleted, nil
}

func (s *Store) CleanupCachedMessages(ctx context.Context, before time.Time, maxPerMailboxFolder, batchSize int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	maxPerMailboxFolder = ports.ListOptions{Limit: maxPerMailboxFolder}.Normalize(5000, 1000000).Limit
	batchSize = ports.ListOptions{Limit: batchSize}.Normalize(1000, 10000).Limit
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := make(map[string][]domain.CachedMessage)
	for _, message := range s.cachedMessages {
		groups[message.MailboxID+"\x00"+string(message.Folder)] = append(groups[message.MailboxID+"\x00"+string(message.Folder)], message)
	}
	deleted := 0
	for _, items := range groups {
		sort.Slice(items, func(i, j int) bool { return items[i].ReceivedAt.After(items[j].ReceivedAt) })
		for index, item := range items {
			if deleted >= batchSize {
				return deleted, nil
			}
			if index >= maxPerMailboxFolder || item.ReceivedAt.Before(before) {
				delete(s.cachedMessages, cachedMessageIdentity(item.MailboxID, item.Folder, item.ExternalID))
				deleted++
			}
		}
	}
	return deleted, nil
}

func (s *Store) ResetMessageSyncState(ctx context.Context, targetID string, folder domain.MessageFolder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.messageSyncStates, messageSyncIdentity(targetID, folder))
	s.mu.Unlock()
	return nil
}

func (s *Store) QueryCachedMessages(ctx context.Context, filter ports.MessageCacheFilter, options ports.ListOptions) ([]domain.CachedMessage, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	search := strings.ToLower(strings.TrimSpace(filter.Search))
	s.mu.RLock()
	items := make([]domain.CachedMessage, 0)
	for _, item := range s.cachedMessages {
		if !messageMatchesCacheFilter(item, filter, search) {
			continue
		}
		items = append(items, cloneCachedMessage(item))
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].ReceivedAt.After(items[j].ReceivedAt) })
	total := int64(len(items))
	return paginate(items, options.Normalize(100, 5000)), total, nil
}

func (s *Store) DeleteCachedMessagesRange(ctx context.Context, filter ports.MessageCacheFilter, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	limit = ports.ListOptions{Limit: limit}.Normalize(1000, 100000).Limit
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for key, item := range s.cachedMessages {
		if deleted == limit {
			break
		}
		if messageMatchesCacheFilter(item, filter, "") {
			delete(s.cachedMessages, key)
			deleted++
		}
	}
	return deleted, nil
}

func messageMatchesCacheFilter(item domain.CachedMessage, filter ports.MessageCacheFilter, search string) bool {
	if filter.MailboxID != "" && item.MailboxID != filter.MailboxID {
		return false
	}
	if filter.Folder != "" && item.Folder != filter.Folder {
		return false
	}
	if filter.After != nil && item.ReceivedAt.Before(*filter.After) {
		return false
	}
	if filter.Before != nil && !item.ReceivedAt.Before(*filter.Before) {
		return false
	}
	return search == "" || strings.Contains(strings.ToLower(item.Subject+"\n"+item.From+"\n"+item.Text), search)
}

func (s *Store) UpsertRetrievalCapability(ctx context.Context, capability domain.MailboxRetrievalCapability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.retrievalCapabilities[capability.MailboxID+"\x00"+string(capability.Method)] = capability
	s.mu.Unlock()
	return nil
}

func (s *Store) GetRetrievalCapability(ctx context.Context, mailboxID string, method domain.RetrievalMethod) (domain.MailboxRetrievalCapability, error) {
	if err := ctx.Err(); err != nil {
		return domain.MailboxRetrievalCapability{}, err
	}
	s.mu.RLock()
	item, ok := s.retrievalCapabilities[mailboxID+"\x00"+string(method)]
	s.mu.RUnlock()
	if !ok {
		return domain.MailboxRetrievalCapability{}, fmt.Errorf("%w: retrieval capability", domain.ErrNotFound)
	}
	return item, nil
}

func (s *Store) ListRetrievalCapabilities(ctx context.Context, mailboxID string) ([]domain.MailboxRetrievalCapability, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.MailboxRetrievalCapability, 0)
	for _, item := range s.retrievalCapabilities {
		if item.MailboxID == mailboxID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Method < items[j].Method })
	return items, nil
}

func (s *Store) ListPendingRetrievalCapabilities(ctx context.Context, limit int) ([]domain.MailboxRetrievalCapability, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit = ports.ListOptions{Limit: limit}.Normalize(100, 500).Limit
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.MailboxRetrievalCapability, 0)
	for _, item := range s.retrievalCapabilities {
		if item.Status == domain.RetrievalCapabilityPending || item.Status == domain.RetrievalCapabilityError {
			items = append(items, item)
			if len(items) == limit {
				break
			}
		}
	}
	return items, nil
}

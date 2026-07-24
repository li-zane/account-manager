package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
		if _, exists := s.cachedMessages[key]; !exists {
			changed++
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
	return value
}

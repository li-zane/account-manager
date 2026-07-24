package postgres

import (
	"context"
	"encoding/json"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

func (s *Store) UpsertCachedMessages(ctx context.Context, messages []domain.CachedMessage) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted := 0
	for _, message := range messages {
		headers, err := json.Marshal(message.Headers)
		if err != nil {
			return 0, err
		}
		var wasInserted bool
		err = tx.QueryRow(ctx, `
			INSERT INTO mailbox_cached_messages
				(id, mailbox_id, external_id, provider_message_id, internet_message_id, folder,
				 from_address, to_addresses, cc_addresses, recipient_addresses, subject,
				 text_content, html_content, received_at, unread, headers, discovered_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (mailbox_id, folder, external_id) DO UPDATE SET
				provider_message_id=EXCLUDED.provider_message_id,
				internet_message_id=EXCLUDED.internet_message_id,
				from_address=EXCLUDED.from_address,
				to_addresses=EXCLUDED.to_addresses,
				cc_addresses=EXCLUDED.cc_addresses,
				recipient_addresses=EXCLUDED.recipient_addresses,
				subject=EXCLUDED.subject,
				text_content=EXCLUDED.text_content,
				html_content=EXCLUDED.html_content,
				received_at=EXCLUDED.received_at,
				unread=EXCLUDED.unread,
				headers=EXCLUDED.headers,
				updated_at=EXCLUDED.updated_at
			RETURNING (xmax = 0)`,
			message.ID, message.MailboxID, message.ExternalID, message.ProviderMessageID,
			message.InternetMessageID, message.Folder, message.From, message.To, message.Cc,
			message.RecipientAddresses, message.Subject, message.Text, message.HTML,
			message.ReceivedAt, message.Unread, headers, message.DiscoveredAt, message.UpdatedAt).Scan(&wasInserted)
		if err != nil {
			return 0, mapError(err)
		}
		if wasInserted {
			inserted++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, mapError(err)
	}
	return inserted, nil
}

func (s *Store) ListCachedMessages(ctx context.Context, mailboxID string, folder domain.MessageFolder, recipientAddress string, options ports.ListOptions) ([]domain.CachedMessage, error) {
	options = options.Normalize(50, 200)
	rows, err := s.pool.Query(ctx, `
		SELECT id, mailbox_id, external_id, provider_message_id, internet_message_id, folder,
			from_address, to_addresses, cc_addresses, recipient_addresses, subject,
			text_content, html_content, received_at, unread, headers, discovered_at, updated_at
		FROM mailbox_cached_messages
		WHERE mailbox_id=$1 AND folder=$2 AND ($3='' OR $3=ANY(recipient_addresses))
		ORDER BY received_at DESC, id LIMIT $4 OFFSET $5`, mailboxID, folder, recipientAddress, options.Limit, options.Offset)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.CachedMessage, 0)
	for rows.Next() {
		item, err := scanCachedMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) GetMessageSyncState(ctx context.Context, targetID string, folder domain.MessageFolder) (domain.MessageSyncState, error) {
	var state domain.MessageSyncState
	var aliasID *string
	err := s.pool.QueryRow(ctx, `
		SELECT target_id, mailbox_id, alias_id, folder, last_message_at, last_synced_at, last_error
		FROM mailbox_message_sync_states WHERE target_id=$1 AND folder=$2`, targetID, folder).Scan(
		&state.TargetID, &state.MailboxID, &aliasID, &state.Folder,
		&state.LastMessageAt, &state.LastSyncedAt, &state.LastError)
	if aliasID != nil {
		state.AliasID = *aliasID
	}
	return state, mapError(err)
}

func (s *Store) SaveMessageSyncState(ctx context.Context, state domain.MessageSyncState) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mailbox_message_sync_states
			(target_id, mailbox_id, alias_id, folder, last_message_at, last_synced_at, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (target_id, folder) DO UPDATE SET
			last_message_at=CASE
				WHEN mailbox_message_sync_states.last_message_at IS NULL THEN EXCLUDED.last_message_at
				WHEN EXCLUDED.last_message_at IS NULL THEN mailbox_message_sync_states.last_message_at
				ELSE GREATEST(mailbox_message_sync_states.last_message_at, EXCLUDED.last_message_at)
			END,
			last_synced_at=EXCLUDED.last_synced_at,
			last_error=EXCLUDED.last_error`,
		state.TargetID, state.MailboxID, nullableText(state.AliasID), state.Folder,
		state.LastMessageAt, state.LastSyncedAt, state.LastError)
	return mapError(err)
}

func scanCachedMessage(row scanner) (domain.CachedMessage, error) {
	var item domain.CachedMessage
	var headers []byte
	err := row.Scan(&item.ID, &item.MailboxID, &item.ExternalID, &item.ProviderMessageID,
		&item.InternetMessageID, &item.Folder, &item.From, &item.To, &item.Cc,
		&item.RecipientAddresses, &item.Subject, &item.Text, &item.HTML, &item.ReceivedAt,
		&item.Unread, &headers, &item.DiscoveredAt, &item.UpdatedAt)
	if err == nil && len(headers) > 0 {
		err = json.Unmarshal(headers, &item.Headers)
	}
	return item, mapError(err)
}

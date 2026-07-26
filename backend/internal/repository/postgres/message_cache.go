package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"

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
				(id, mailbox_id, external_id, provider_message_id, retrieval_method, internet_message_id, folder,
				 from_address, to_addresses, cc_addresses, recipient_addresses, subject,
				 text_content, html_content, received_at, unread, viewed_at, headers, discovered_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
			ON CONFLICT (mailbox_id, folder, external_id) DO UPDATE SET
				provider_message_id=EXCLUDED.provider_message_id,
				retrieval_method=EXCLUDED.retrieval_method,
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
				viewed_at=COALESCE(mailbox_cached_messages.viewed_at, EXCLUDED.viewed_at),
				headers=EXCLUDED.headers,
				updated_at=EXCLUDED.updated_at
			RETURNING (xmax = 0)`,
			message.ID, message.MailboxID, message.ExternalID, message.ProviderMessageID, message.RetrievalMethod,
			message.InternetMessageID, message.Folder, message.From, message.To, message.Cc,
			message.RecipientAddresses, message.Subject, message.Text, message.HTML,
			message.ReceivedAt, message.Unread, message.ViewedAt, headers, message.DiscoveredAt, message.UpdatedAt).Scan(&wasInserted)
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
		SELECT id, mailbox_id, external_id, provider_message_id, retrieval_method, internet_message_id, folder,
			from_address, to_addresses, cc_addresses, recipient_addresses, subject,
			text_content, html_content, received_at, unread, viewed_at, headers, discovered_at, updated_at
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
		SELECT target_id, mailbox_id, alias_id, folder, last_message_at, last_synced_at, last_error, retrieval_method, cursor, uid_validity, highest_uid
		FROM mailbox_message_sync_states WHERE target_id=$1 AND folder=$2`, targetID, folder).Scan(
		&state.TargetID, &state.MailboxID, &aliasID, &state.Folder,
		&state.LastMessageAt, &state.LastSyncedAt, &state.LastError, &state.RetrievalMethod, &state.Cursor, &state.UIDValidity, &state.HighestUID)
	if aliasID != nil {
		state.AliasID = *aliasID
	}
	return state, mapError(err)
}

func (s *Store) SaveMessageSyncState(ctx context.Context, state domain.MessageSyncState) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mailbox_message_sync_states
			(target_id, mailbox_id, alias_id, folder, last_message_at, last_synced_at, last_error, retrieval_method, cursor, uid_validity, highest_uid)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (target_id, folder) DO UPDATE SET
			last_message_at=CASE
				WHEN mailbox_message_sync_states.last_message_at IS NULL THEN EXCLUDED.last_message_at
				WHEN EXCLUDED.last_message_at IS NULL THEN mailbox_message_sync_states.last_message_at
				ELSE GREATEST(mailbox_message_sync_states.last_message_at, EXCLUDED.last_message_at)
			END,
			last_synced_at=EXCLUDED.last_synced_at,
			last_error=EXCLUDED.last_error,
			retrieval_method=EXCLUDED.retrieval_method, cursor=EXCLUDED.cursor,
			uid_validity=EXCLUDED.uid_validity, highest_uid=EXCLUDED.highest_uid`,
		state.TargetID, state.MailboxID, nullableText(state.AliasID), state.Folder,
		state.LastMessageAt, state.LastSyncedAt, state.LastError, state.RetrievalMethod, state.Cursor, state.UIDValidity, state.HighestUID)
	return mapError(err)
}

func scanCachedMessage(row scanner) (domain.CachedMessage, error) {
	var item domain.CachedMessage
	var headers []byte
	err := row.Scan(&item.ID, &item.MailboxID, &item.ExternalID, &item.ProviderMessageID, &item.RetrievalMethod,
		&item.InternetMessageID, &item.Folder, &item.From, &item.To, &item.Cc,
		&item.RecipientAddresses, &item.Subject, &item.Text, &item.HTML, &item.ReceivedAt,
		&item.Unread, &item.ViewedAt, &headers, &item.DiscoveredAt, &item.UpdatedAt)
	if err == nil && len(headers) > 0 {
		err = json.Unmarshal(headers, &item.Headers)
	}
	return item, mapError(err)
}

func (s *Store) DeleteCachedMessages(ctx context.Context, mailboxID string, folder domain.MessageFolder, method domain.RetrievalMethod, providerIDs []string) (int, error) {
	if len(providerIDs) == 0 {
		return 0, nil
	}
	result, err := s.pool.Exec(ctx, `DELETE FROM mailbox_cached_messages
		WHERE mailbox_id=$1 AND folder=$2 AND ($3='' OR retrieval_method=$3) AND provider_message_id = ANY($4)`, mailboxID, folder, method, providerIDs)
	return int(result.RowsAffected()), mapError(err)
}

func (s *Store) PurgeCachedMessages(ctx context.Context, mailboxID string, folder domain.MessageFolder, before *time.Time, limit int) (int, error) {
	limit = ports.ListOptions{Limit: limit}.Normalize(500, 5000).Limit
	result, err := s.pool.Exec(ctx, `DELETE FROM mailbox_cached_messages WHERE id IN (
		SELECT id FROM mailbox_cached_messages
		WHERE mailbox_id=$1 AND ($2='' OR folder=$2) AND ($3::timestamptz IS NULL OR received_at < $3)
		ORDER BY received_at ASC, id LIMIT $4)`, mailboxID, folder, before, limit)
	return int(result.RowsAffected()), mapError(err)
}

func (s *Store) CleanupCachedMessages(ctx context.Context, before time.Time, maxPerMailboxFolder, batchSize int) (int, error) {
	maxPerMailboxFolder = ports.ListOptions{Limit: maxPerMailboxFolder}.Normalize(5000, 1000000).Limit
	batchSize = ports.ListOptions{Limit: batchSize}.Normalize(1000, 10000).Limit
	result, err := s.pool.Exec(ctx, `WITH ranked AS (
		SELECT id, received_at, row_number() OVER (PARTITION BY mailbox_id, folder ORDER BY received_at DESC, id DESC) AS rank
		FROM mailbox_cached_messages
	), doomed AS (SELECT id FROM ranked WHERE received_at < $1 OR rank > $2 LIMIT $3)
	DELETE FROM mailbox_cached_messages WHERE id IN (SELECT id FROM doomed)`, before, maxPerMailboxFolder, batchSize)
	return int(result.RowsAffected()), mapError(err)
}

func (s *Store) ResetMessageSyncState(ctx context.Context, targetID string, folder domain.MessageFolder) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM mailbox_message_sync_states WHERE target_id=$1 AND folder=$2`, targetID, folder)
	return mapError(err)
}

func (s *Store) QueryCachedMessages(ctx context.Context, filter ports.MessageCacheFilter, options ports.ListOptions) ([]domain.CachedMessage, int64, error) {
	options = options.Normalize(100, 5000)
	search := strings.TrimSpace(filter.Search)
	const where = ` WHERE ($1='' OR mailbox_id=$1) AND ($2='' OR folder=$2)
		AND ($3::timestamptz IS NULL OR received_at >= $3) AND ($4::timestamptz IS NULL OR received_at < $4)
		AND ($5='' OR subject ILIKE '%' || $5 || '%' OR from_address ILIKE '%' || $5 || '%' OR text_content ILIKE '%' || $5 || '%')`
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM mailbox_cached_messages`+where,
		filter.MailboxID, filter.Folder, filter.After, filter.Before, search).Scan(&total); err != nil {
		return nil, 0, mapError(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, mailbox_id, external_id, provider_message_id, retrieval_method, internet_message_id, folder,
		from_address, to_addresses, cc_addresses, recipient_addresses, subject, text_content, html_content, received_at, unread, viewed_at, headers, discovered_at, updated_at
		FROM mailbox_cached_messages`+where+` ORDER BY received_at DESC, id LIMIT $6 OFFSET $7`,
		filter.MailboxID, filter.Folder, filter.After, filter.Before, search, options.Limit, options.Offset)
	if err != nil {
		return nil, 0, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.CachedMessage, 0)
	for rows.Next() {
		item, scanErr := scanCachedMessage(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, item)
	}
	return items, total, mapError(rows.Err())
}

func (s *Store) MarkCachedMessageViewed(ctx context.Context, mailboxID, messageID string, viewedAt time.Time) error {
	result, err := s.pool.Exec(ctx, `UPDATE mailbox_cached_messages
		SET viewed_at=COALESCE(viewed_at, $3), updated_at=GREATEST(updated_at, $3)
		WHERE mailbox_id=$1 AND id=$2`, mailboxID, messageID, viewedAt.UTC())
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteCachedMessagesRange(ctx context.Context, filter ports.MessageCacheFilter, limit int) (int, error) {
	limit = ports.ListOptions{Limit: limit}.Normalize(1000, 100000).Limit
	result, err := s.pool.Exec(ctx, `DELETE FROM mailbox_cached_messages WHERE id IN (
		SELECT id FROM mailbox_cached_messages WHERE ($1='' OR mailbox_id=$1) AND ($2='' OR folder=$2)
		AND ($3::timestamptz IS NULL OR received_at >= $3) AND ($4::timestamptz IS NULL OR received_at < $4)
		ORDER BY received_at ASC, id LIMIT $5)`, filter.MailboxID, filter.Folder, filter.After, filter.Before, limit)
	return int(result.RowsAffected()), mapError(err)
}

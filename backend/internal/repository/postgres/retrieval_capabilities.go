package postgres

import (
	"context"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

func (s *Store) UpsertRetrievalCapability(ctx context.Context, capability domain.MailboxRetrievalCapability) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO mailbox_retrieval_capabilities
		(mailbox_id, method, status, preferred, token_expires_at, checked_at, error_code, error_message)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (mailbox_id, method) DO UPDATE SET status=EXCLUDED.status, preferred=EXCLUDED.preferred,
		token_expires_at=EXCLUDED.token_expires_at, checked_at=EXCLUDED.checked_at,
		error_code=EXCLUDED.error_code, error_message=EXCLUDED.error_message`,
		capability.MailboxID, capability.Method, capability.Status, capability.Preferred,
		capability.TokenExpiresAt, capability.CheckedAt, capability.ErrorCode, capability.ErrorMessage)
	return mapError(err)
}

func (s *Store) GetRetrievalCapability(ctx context.Context, mailboxID string, method domain.RetrievalMethod) (domain.MailboxRetrievalCapability, error) {
	return scanRetrievalCapability(s.pool.QueryRow(ctx, `SELECT mailbox_id, method, status, preferred,
		token_expires_at, checked_at, error_code, error_message FROM mailbox_retrieval_capabilities WHERE mailbox_id=$1 AND method=$2`, mailboxID, method))
}

func (s *Store) ListRetrievalCapabilities(ctx context.Context, mailboxID string) ([]domain.MailboxRetrievalCapability, error) {
	rows, err := s.pool.Query(ctx, `SELECT mailbox_id, method, status, preferred, token_expires_at,
		checked_at, error_code, error_message FROM mailbox_retrieval_capabilities WHERE mailbox_id=$1 ORDER BY method`, mailboxID)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.MailboxRetrievalCapability, 0)
	for rows.Next() {
		item, scanErr := scanRetrievalCapability(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (s *Store) ListPendingRetrievalCapabilities(ctx context.Context, limit int) ([]domain.MailboxRetrievalCapability, error) {
	limit = ports.ListOptions{Limit: limit}.Normalize(100, 500).Limit
	rows, err := s.pool.Query(ctx, `SELECT mailbox_id, method, status, preferred, token_expires_at,
		checked_at, error_code, error_message FROM mailbox_retrieval_capabilities
		WHERE status IN ('pending','error') ORDER BY checked_at NULLS FIRST, mailbox_id LIMIT $1`, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	items := make([]domain.MailboxRetrievalCapability, 0)
	for rows.Next() {
		item, scanErr := scanRetrievalCapability(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

type retrievalCapabilityScanner interface{ Scan(...any) error }

func scanRetrievalCapability(row retrievalCapabilityScanner) (domain.MailboxRetrievalCapability, error) {
	var item domain.MailboxRetrievalCapability
	err := row.Scan(&item.MailboxID, &item.Method, &item.Status, &item.Preferred, &item.TokenExpiresAt,
		&item.CheckedAt, &item.ErrorCode, &item.ErrorMessage)
	return item, mapError(err)
}

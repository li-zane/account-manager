package postgres

import (
	"context"
	"hash/fnv"

	"github.com/li-zane/account-manager/backend/internal/ports"
)

func (s *Store) AcquireCredentialRefreshLock(ctx context.Context, key string) (func(), error) {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(key))
	lockID := int64(hash.Sum64())
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		connection.Release()
		return nil, err
	}
	return func() {
		_, _ = connection.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
		connection.Release()
	}, nil
}

var _ ports.CredentialRefreshLocker = (*Store)(nil)

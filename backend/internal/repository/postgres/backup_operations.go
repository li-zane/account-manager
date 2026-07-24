package postgres

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// This session-level advisory lock serializes pg_dump and pg_restore across
// every application instance connected to the same PostgreSQL cluster.
const backupOperationLockID int64 = 665176841447883933

func (s *Store) TryAcquireBackupOperation(ctx context.Context) (func(), bool, error) {
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire backup operation connection: %w", err)
	}
	var acquired bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, backupOperationLockID).Scan(&acquired); err != nil {
		connection.Release()
		return nil, false, fmt.Errorf("acquire backup operation lock: %w", err)
	}
	if !acquired {
		connection.Release()
		return nil, false, nil
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := connection.Exec(releaseCtx, `SELECT pg_advisory_unlock($1)`, backupOperationLockID); err != nil {
				raw := connection.Hijack()
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer closeCancel()
				_ = raw.Close(closeCtx)
				return
			}
			connection.Release()
		})
	}
	return release, true, nil
}

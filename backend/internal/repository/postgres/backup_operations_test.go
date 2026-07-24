package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBackupOperationAdvisoryLockIntegration(t *testing.T) {
	databaseURL := os.Getenv("BACKUP_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BACKUP_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConns < 2 {
		config.MaxConns = 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := New(pool)

	releaseFirst, acquired, err := store.TryAcquireBackupOperation(ctx)
	if err != nil || !acquired {
		t.Fatalf("first advisory lock: acquired=%v err=%v", acquired, err)
	}
	defer releaseFirst()
	if releaseSecond, acquired, err := store.TryAcquireBackupOperation(ctx); err != nil {
		t.Fatal(err)
	} else if acquired {
		releaseSecond()
		t.Fatal("second advisory lock unexpectedly acquired")
	}

	releaseFirst()
	releaseThird, acquired, err := store.TryAcquireBackupOperation(ctx)
	if err != nil || !acquired {
		t.Fatalf("advisory lock after release: acquired=%v err=%v", acquired, err)
	}
	releaseThird()
}

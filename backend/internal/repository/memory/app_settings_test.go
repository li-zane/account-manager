package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
)

func TestAppSettingConcurrentCASAllowsOneWinner(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	current, err := store.GetAppSetting(ctx, domain.AppSettingKeyTokenRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 1 {
		t.Fatalf("seed version = %d, want 1", current.Version)
	}

	const writers = 32
	start := make(chan struct{})
	var group sync.WaitGroup
	var successes atomic.Int32
	errorsFound := make(chan error, writers)
	for writer := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			candidate := current
			candidate.Value = json.RawMessage(fmt.Sprintf(`{"enabled":true,"lead_time_minutes":%d}`, writer%30+1))
			candidate.UpdatedAt = time.Now().UTC()
			err := store.SaveAppSetting(ctx, candidate, current.Version)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, domain.ErrConflict) {
				errorsFound <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent update: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful concurrent updates = %d, want 1", got)
	}
	updated, err := store.GetAppSetting(ctx, domain.AppSettingKeyTokenRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 {
		t.Fatalf("updated version = %d, want 2", updated.Version)
	}
}

func TestAppSettingCASCreatesMissingKeyAtVersionOne(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	setting := domain.AppSetting{
		Key: "fixture.setting", Value: json.RawMessage(`{"enabled":true}`), UpdatedAt: time.Now().UTC(),
	}
	if err := store.SaveAppSetting(ctx, setting, 0); err != nil {
		t.Fatal(err)
	}
	created, err := store.GetAppSetting(ctx, setting.Key)
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 {
		t.Fatalf("created version = %d, want 1", created.Version)
	}
	if err := store.SaveAppSetting(ctx, setting, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate create error = %v, want conflict", err)
	}
}

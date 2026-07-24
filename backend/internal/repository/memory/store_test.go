package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/li-zane/account-manager/backend/internal/repository/memory"
)

func TestStoreSupportsConcurrentMailboxWritesAndReads(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	const count = 100
	start := make(chan struct{})
	var group sync.WaitGroup
	errors := make(chan error, count)
	for index := 0; index < count; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			address := fmt.Sprintf("user-%03d@example.com", index)
			id, err := domain.NewMailboxID(string(domain.ProviderMicrosoft), address)
			if err != nil {
				errors <- err
				return
			}
			now := time.Now().UTC()
			if err := store.CreateMailbox(ctx, domain.Mailbox{
				ID: id, Provider: domain.ProviderMicrosoft, Address: address,
				NormalizedAddress: address, Status: domain.MailboxStatusActive,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				errors <- err
				return
			}
			if _, err := store.ListMailboxes(ctx, ports.ListOptions{Limit: 500}); err != nil {
				errors <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	items, err := store.ListMailboxes(ctx, ports.ListOptions{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != count {
		t.Fatalf("mailbox count = %d, want %d", len(items), count)
	}
}

func TestBackupTargetNamesMatchPostgresUniqueness(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	first := domain.BackupTarget{ID: "btarget_one", Name: "daily", Kind: domain.BackupTargetS3}
	second := domain.BackupTarget{ID: "btarget_two", Name: "daily", Kind: domain.BackupTargetWebDAV}
	if err := store.CreateBackupTarget(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBackupTarget(ctx, second); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate backup target name error = %v, want conflict", err)
	}
}

func TestConcurrentBackupRunsAllowOneActiveRunPerTarget(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	now := time.Now().UTC()
	target := domain.BackupTarget{ID: "btarget_concurrent", Name: "concurrent", Kind: domain.BackupTargetS3, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	const callers = 24
	start := make(chan struct{})
	var group sync.WaitGroup
	var created atomic.Int64
	errorsFound := make(chan error, callers)
	for index := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			run := domain.BackupRun{
				ID: fmt.Sprintf("brun_%02d", index), TargetID: target.ID,
				State: domain.BackupRunPending, CreatedAt: now, UpdatedAt: now,
			}
			if err := store.CreateBackupRun(ctx, run); err == nil {
				created.Add(1)
			} else if !errors.Is(err, domain.ErrConflict) {
				errorsFound <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if created.Load() != 1 {
		t.Fatalf("created active runs = %d, want 1", created.Load())
	}
}

func TestCredentialUpsertRejectsStaleVersion(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	now := time.Now().UTC()
	mailbox := domain.Mailbox{
		ID: "mbx_microsoft_fixture", Provider: domain.ProviderMicrosoft,
		Address: "owner@example.com", NormalizedAddress: "owner@example.com",
		Status: domain.MailboxStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateMailbox(ctx, mailbox); err != nil {
		t.Fatal(err)
	}
	credential := domain.MailboxCredential{
		ID: "credential_one", MailboxID: mailbox.ID, Kind: domain.CredentialMicrosoftGraphOAuth,
		EncryptedSecret: []byte("first"), KeyVersion: "v1", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetCredential(ctx, mailbox.ID, credential.Kind)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 1 {
		t.Fatalf("credential version = %d, want 1", current.Version)
	}
	current.EncryptedSecret = []byte("second")
	if err := store.UpsertCredential(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCredential(ctx, current); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale credential update error = %v, want conflict", err)
	}
}

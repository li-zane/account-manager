package backup

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
)

const maxRememberedRestoreOperations = 100

type restoreStarter interface {
	StartRestore(ctx context.Context, runID string) (<-chan error, error)
}

// RestoreCoordinator owns request-independent restore lifetimes and exposes a
// small status surface for HTTP polling. Restore records intentionally remain
// process-local because pg_restore replaces the database containing them.
type RestoreCoordinator struct {
	ctx        context.Context
	starter    restoreStarter
	repository ports.BackupRepository
	clock      func() time.Time
	newID      func(string) (string, error)

	mu         sync.RWMutex
	operations map[string]domain.BackupRestoreOperation
	order      []string
	wait       sync.WaitGroup
}

func NewRestoreCoordinator(ctx context.Context, starter restoreStarter, repository ports.BackupRepository) (*RestoreCoordinator, error) {
	if ctx == nil || starter == nil || repository == nil {
		return nil, fmt.Errorf("%w: restore coordinator dependencies are required", domain.ErrInvalid)
	}
	return &RestoreCoordinator{
		ctx: ctx, starter: starter, repository: repository,
		clock: time.Now, newID: domain.NewRandomID,
		operations: make(map[string]domain.BackupRestoreOperation),
	}, nil
}

func (c *RestoreCoordinator) SetClock(clock func() time.Time) {
	if clock != nil {
		c.clock = clock
	}
}

func (c *RestoreCoordinator) SetIDGenerator(generator func(string) (string, error)) {
	if generator != nil {
		c.newID = generator
	}
}

func (c *RestoreCoordinator) Start(ctx context.Context, runID string) (domain.BackupRestoreOperation, error) {
	if err := ctx.Err(); err != nil {
		return domain.BackupRestoreOperation{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.BackupRestoreOperation{}, fmt.Errorf("%w: backup run is required", domain.ErrInvalid)
	}
	run, err := c.repository.GetBackupRun(ctx, runID)
	if err != nil {
		return domain.BackupRestoreOperation{}, err
	}
	if run.State != domain.BackupRunSucceeded || strings.TrimSpace(run.ObjectKey) == "" || strings.TrimSpace(run.Checksum) == "" {
		return domain.BackupRestoreOperation{}, fmt.Errorf("%w: backup run is not restorable", domain.ErrInvalid)
	}
	id, err := c.newID("brestore")
	if err != nil {
		return domain.BackupRestoreOperation{}, err
	}
	result, err := c.starter.StartRestore(c.ctx, run.ID)
	if err != nil {
		return domain.BackupRestoreOperation{}, err
	}
	if result == nil {
		return domain.BackupRestoreOperation{}, fmt.Errorf("%w: restore starter returned no completion signal", domain.ErrInvalid)
	}
	now := c.clock().UTC()
	operation := domain.BackupRestoreOperation{
		ID: id, RunID: run.ID, TargetID: run.TargetID, State: domain.BackupRestoreRunning,
		StartedAt: now, UpdatedAt: now,
	}
	c.mu.Lock()
	c.pruneLocked()
	c.operations[id] = operation
	c.order = append(c.order, id)
	c.mu.Unlock()

	c.wait.Add(1)
	go c.observe(operation.ID, result)
	return operation, nil
}

func (c *RestoreCoordinator) Get(ctx context.Context, id string) (domain.BackupRestoreOperation, error) {
	if err := ctx.Err(); err != nil {
		return domain.BackupRestoreOperation{}, err
	}
	id = strings.TrimSpace(id)
	c.mu.RLock()
	operation, exists := c.operations[id]
	c.mu.RUnlock()
	if !exists {
		return domain.BackupRestoreOperation{}, fmt.Errorf("%w: restore operation %q", domain.ErrNotFound, id)
	}
	return operation, nil
}

func (c *RestoreCoordinator) Wait() {
	c.wait.Wait()
}

func (c *RestoreCoordinator) observe(id string, result <-chan error) {
	defer c.wait.Done()
	err := <-result
	finished := c.clock().UTC()
	c.mu.Lock()
	operation, exists := c.operations[id]
	if exists {
		operation.State = domain.BackupRestoreSucceeded
		if err != nil {
			operation.State = domain.BackupRestoreFailed
			operation.Error = boundedRunError(err)
		}
		operation.FinishedAt = &finished
		operation.UpdatedAt = finished
		c.operations[id] = operation
	}
	c.mu.Unlock()
}

func (c *RestoreCoordinator) pruneLocked() {
	if len(c.operations) < maxRememberedRestoreOperations {
		return
	}
	kept := c.order[:0]
	for _, id := range c.order {
		operation, exists := c.operations[id]
		if !exists {
			continue
		}
		if len(c.operations) >= maxRememberedRestoreOperations && operation.State != domain.BackupRestoreRunning {
			delete(c.operations, id)
			continue
		}
		kept = append(kept, id)
	}
	c.order = kept
}

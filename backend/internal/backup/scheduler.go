package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/li-zane/account-manager/backend/internal/domain"
	"github.com/li-zane/account-manager/backend/internal/ports"
	"github.com/robfig/cron/v3"
)

const defaultSchedulerInterval = 30 * time.Second

type schedulerRepository interface {
	ListBackupTargets(ctx context.Context, options ports.ListOptions) ([]domain.BackupTarget, error)
	ListBackupRuns(ctx context.Context, targetID string, options ports.ListOptions) ([]domain.BackupRun, error)
	CreateScheduledBackupRun(ctx context.Context, run domain.BackupRun, dueAt time.Time) (bool, error)
}

type Scheduler struct {
	repository schedulerRepository
	logger     *slog.Logger
	clock      func() time.Time
	newID      func(string) (string, error)
	interval   time.Duration
}

func NewScheduler(repository schedulerRepository, logger *slog.Logger) (*Scheduler, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: backup scheduler repository is required", domain.ErrInvalid)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Scheduler{
		repository: repository,
		logger:     logger,
		clock:      time.Now,
		newID:      domain.NewRandomID,
		interval:   defaultSchedulerInterval,
	}, nil
}

func (s *Scheduler) SetClock(clock func() time.Time) {
	if clock != nil {
		s.clock = clock
	}
}

func (s *Scheduler) SetIDGenerator(generator func(string) (string, error)) {
	if generator != nil {
		s.newID = generator
	}
}

func (s *Scheduler) SetInterval(interval time.Duration) {
	if interval > 0 {
		s.interval = interval
	}
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Error("schedule backup runs", "error", err)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("schedule backup runs", "error", err)
			}
		}
	}
}

func (s *Scheduler) RunOnce(ctx context.Context) error {
	now := s.clock().UTC()
	targets, err := s.listTargets(ctx)
	if err != nil {
		return err
	}
	var scheduleErrors []error
	for _, target := range targets {
		if !target.Enabled || strings.TrimSpace(target.Schedule) == "" {
			continue
		}
		schedule, err := ParseSchedule(target.Schedule)
		if err != nil {
			scheduleErrors = append(scheduleErrors, fmt.Errorf("backup target %q: %w", target.ID, err))
			continue
		}
		anchor := target.CreatedAt.UTC()
		runs, err := s.repository.ListBackupRuns(ctx, target.ID, ports.ListOptions{Limit: 1})
		if err != nil {
			scheduleErrors = append(scheduleErrors, fmt.Errorf("list runs for backup target %q: %w", target.ID, err))
			continue
		}
		if len(runs) > 0 && runs[0].CreatedAt.After(anchor) {
			anchor = runs[0].CreatedAt.UTC()
		}
		dueAt := schedule.Next(anchor)
		if dueAt.IsZero() || dueAt.After(now) {
			continue
		}
		runID, err := s.newID("brun")
		if err != nil {
			scheduleErrors = append(scheduleErrors, fmt.Errorf("create run id for backup target %q: %w", target.ID, err))
			continue
		}
		run := domain.BackupRun{
			ID: runID, TargetID: target.ID, State: domain.BackupRunPending,
			CreatedAt: now, UpdatedAt: now,
		}
		created, err := s.repository.CreateScheduledBackupRun(ctx, run, dueAt)
		if err != nil {
			scheduleErrors = append(scheduleErrors, fmt.Errorf("queue scheduled run for backup target %q: %w", target.ID, err))
			continue
		}
		if created {
			s.logger.Info("scheduled backup run", "target_id", target.ID, "run_id", run.ID, "due_at", dueAt)
		}
	}
	return errors.Join(scheduleErrors...)
}

func (s *Scheduler) listTargets(ctx context.Context) ([]domain.BackupTarget, error) {
	const pageSize = 500
	var targets []domain.BackupTarget
	for offset := 0; ; offset += pageSize {
		page, err := s.repository.ListBackupTargets(ctx, ports.ListOptions{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		targets = append(targets, page...)
		if len(page) < pageSize {
			return targets, nil
		}
	}
}

var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

func ParseSchedule(value string) (cron.Schedule, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "daily":
		value = "15 2 * * *"
	case "weekly":
		value = "15 2 * * 0"
	case "six-hours", "six_hours":
		value = "0 */6 * * *"
	}
	schedule, err := scheduleParser.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid backup schedule: %v", domain.ErrInvalid, err)
	}
	return schedule, nil
}

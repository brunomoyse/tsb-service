package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"tsb-service/internal/modules/hubrise/domain"
)

// InventoryJobsConfig tunes the periodic inventory jobs; zero values take the defaults of the plan.
type InventoryJobsConfig struct {
	// Holder identifies this process in the lease table.
	Holder string
	// PollInterval is the cadence of the full inventory read that backs up the callbacks (30 minutes).
	PollInterval time.Duration
	// SweepInterval is the cadence of the sold-out expiry sweeper (1 minute).
	SweepInterval time.Duration
}

func (c InventoryJobsConfig) withDefaults() InventoryJobsConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 30 * time.Minute
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = time.Minute
	}
	if c.Holder == "" {
		c.Holder = uuid.NewString()
	}
	return c
}

// InventoryJobsDeps are the collaborators of the periodic inventory jobs.
type InventoryJobsDeps struct {
	Inventory *InventoryService
	Leases    domain.LeaseRepository
	Clock     domain.Clock
	Config    InventoryJobsConfig
}

// InventoryJobs runs the two periodic inventory jobs, each by whichever replica holds its lease: the
// 30 minute full read (a background call for the rate limiter) that catches the events HubRise lost,
// and the 1 minute sweeper that gives back the sold-out entries whose expiry passed.
type InventoryJobs struct {
	poll, sweep periodicJob
}

// NewInventoryJobs returns the jobs.
func NewInventoryJobs(d InventoryJobsDeps) *InventoryJobs {
	cfg := d.Config.withDefaults()
	// Another process syncing right now is not a failure: it does the work this tick would have done.
	idle := func(err error) error {
		if errors.Is(err, ErrInventoryBusy) {
			return nil
		}
		return err
	}
	return &InventoryJobs{
		poll: periodicJob{
			name: LeaseInventoryPoll, holder: cfg.Holder, interval: cfg.PollInterval, leases: d.Leases, clock: d.Clock,
			run: func(ctx context.Context) error {
				_, err := d.Inventory.Sync(ctx, true)
				return idle(err)
			},
		},
		sweep: periodicJob{
			name: LeaseInventorySweeper, holder: cfg.Holder, interval: cfg.SweepInterval, leases: d.Leases, clock: d.Clock,
			run: func(ctx context.Context) error {
				_, err := d.Inventory.ReleaseExpired(ctx)
				return idle(err)
			},
		},
	}
}

// PollOnce runs the full read once if this process can take the poll lease.
func (j *InventoryJobs) PollOnce(ctx context.Context) (bool, error) { return j.poll.tick(ctx) }

// SweepOnce runs the expiry sweeper once if this process can take the sweeper lease.
func (j *InventoryJobs) SweepOnce(ctx context.Context) (bool, error) { return j.sweep.tick(ctx) }

// Run runs both jobs until ctx ends.
func (j *InventoryJobs) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, job := range []periodicJob{j.poll, j.sweep} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job.loop(ctx)
		}()
	}
	wg.Wait()
}

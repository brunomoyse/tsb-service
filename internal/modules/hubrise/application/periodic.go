package application

import (
	"context"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/logging"
)

// periodicJob is a job run on a cadence by whichever replica takes its lease. The lease is kept (not
// released) for just under an interval, so the replicas share the cadence instead of each running
// every tick.
type periodicJob struct {
	name     string
	holder   string
	interval time.Duration
	leases   domain.LeaseRepository
	clock    domain.Clock
	run      func(ctx context.Context) error
}

// tick runs the job once if this process can take the lease.
func (j periodicJob) tick(ctx context.Context) (ran bool, err error) {
	ok, err := j.leases.Acquire(ctx, j.name, j.holder, j.clock.Now(), j.interval-j.interval/10)
	if err != nil || !ok {
		return false, err
	}
	return true, j.run(ctx)
}

// loop ticks until ctx ends, starting with a tick at once.
func (j periodicJob) loop(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		if _, err := j.tick(ctx); err != nil {
			logging.FromContext(ctx).Error("hubrise: periodic job failed", zap.String("job", j.name), zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

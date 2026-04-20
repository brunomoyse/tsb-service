package infrastructure

import (
	"context"
	"time"

	"tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/db"
)

const overrideColumns = `date, closed, schedule, note, created_at, updated_at`

type ScheduleOverrideRepository struct {
	pool *db.DBPool
}

func NewScheduleOverrideRepository(pool *db.DBPool) domain.ScheduleOverrideRepository {
	return &ScheduleOverrideRepository{pool: pool}
}

func (r *ScheduleOverrideRepository) List(ctx context.Context, from, to time.Time) ([]*domain.ScheduleOverride, error) {
	var out []*domain.ScheduleOverride
	err := r.pool.ForContext(ctx).SelectContext(ctx, &out,
		`SELECT `+overrideColumns+`
		 FROM restaurant_schedule_overrides
		 WHERE date >= $1 AND date <= $2
		 ORDER BY date ASC`, from, to)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ScheduleOverrideRepository) ListFromDate(ctx context.Context, from time.Time) ([]*domain.ScheduleOverride, error) {
	var out []*domain.ScheduleOverride
	err := r.pool.ForContext(ctx).SelectContext(ctx, &out,
		`SELECT `+overrideColumns+`
		 FROM restaurant_schedule_overrides
		 WHERE date >= $1
		 ORDER BY date ASC`, from)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ScheduleOverrideRepository) Get(ctx context.Context, date time.Time) (*domain.ScheduleOverride, error) {
	var ov domain.ScheduleOverride
	err := r.pool.ForContext(ctx).GetContext(ctx, &ov,
		`SELECT `+overrideColumns+` FROM restaurant_schedule_overrides WHERE date = $1`, date)
	if err != nil {
		return nil, err
	}
	return &ov, nil
}

func (r *ScheduleOverrideRepository) Upsert(ctx context.Context, ov *domain.ScheduleOverride) (*domain.ScheduleOverride, error) {
	var out domain.ScheduleOverride
	err := r.pool.ForContext(ctx).GetContext(ctx, &out,
		`INSERT INTO restaurant_schedule_overrides (date, closed, schedule, note, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, NOW(), NOW())
		 ON CONFLICT (date) DO UPDATE
		 SET closed = EXCLUDED.closed,
		     schedule = EXCLUDED.schedule,
		     note = EXCLUDED.note,
		     updated_at = NOW()
		 RETURNING `+overrideColumns,
		ov.Date, ov.Closed, ov.Schedule, ov.Note)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *ScheduleOverrideRepository) Delete(ctx context.Context, date time.Time) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx,
		`DELETE FROM restaurant_schedule_overrides WHERE date = $1`, date)
	return err
}

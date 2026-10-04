package infrastructure

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/db"
)

func closedPool(t *testing.T) *db.DBPool {
	t.Helper()
	conn := testhelpers.ClosedDB(t)
	return &db.DBPool{Customer: conn, Admin: conn}
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestRestaurantRepository(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	repo := NewRestaurantRepository(&db.DBPool{Customer: tdb.DB, Admin: tdb.DB})
	ctx := t.Context()

	t.Run("GetConfig returns the seeded single row; unset ordering hours read as JSON null", func(t *testing.T) {
		cfg, err := repo.GetConfig(ctx)
		require.NoError(t, err)
		assert.True(t, cfg.OrderingEnabled)
		assert.JSONEq(t, `null`, string(cfg.OrderingHours))
		hours, err := cfg.GetOpeningHours()
		require.NoError(t, err)
		require.NotNil(t, hours["monday"])
		assert.Nil(t, hours["wednesday"], "the seeded default closes Wednesday")
		assert.Positive(t, cfg.PreparationMinutes)
	})

	t.Run("each update changes only its own column and returns the new row", func(t *testing.T) {
		before, err := repo.GetConfig(ctx)
		require.NoError(t, err)

		off, err := repo.UpdateOrderingEnabled(ctx, false)
		require.NoError(t, err)
		assert.False(t, off.OrderingEnabled)
		assert.JSONEq(t, string(before.OpeningHours), string(off.OpeningHours))
		assert.Equal(t, before.PreparationMinutes, off.PreparationMinutes)

		opening, err := repo.UpdateOpeningHours(ctx, json.RawMessage(`{"monday":{"open":"10:00","close":"15:00"},"tuesday":null}`))
		require.NoError(t, err)
		assert.JSONEq(t, `{"monday":{"open":"10:00","close":"15:00"},"tuesday":null}`, string(opening.OpeningHours))
		assert.False(t, opening.OrderingEnabled, "the previous update persists")

		ordering, err := repo.UpdateOrderingHours(ctx, json.RawMessage(`{"monday":{"open":"11:00","close":"14:00"}}`))
		require.NoError(t, err)
		assert.JSONEq(t, `{"monday":{"open":"11:00","close":"14:00"}}`, string(ordering.OrderingHours))
		assert.JSONEq(t, string(opening.OpeningHours), string(ordering.OpeningHours))

		prep, err := repo.UpdatePreparationMinutes(ctx, 45)
		require.NoError(t, err)
		assert.Equal(t, 45, prep.PreparationMinutes)

		again, err := repo.GetConfig(ctx)
		require.NoError(t, err)
		assert.Equal(t, 45, again.PreparationMinutes)
		assert.True(t, !again.UpdatedAt.Before(before.UpdatedAt))
	})

	t.Run("a closed connection is reported by every method", func(t *testing.T) {
		closed := NewRestaurantRepository(closedPool(t))
		_, err := closed.GetConfig(ctx)
		assert.Error(t, err)
		_, err = closed.UpdateOrderingEnabled(ctx, true)
		assert.Error(t, err)
		_, err = closed.UpdateOpeningHours(ctx, json.RawMessage(`{}`))
		assert.Error(t, err)
		_, err = closed.UpdateOrderingHours(ctx, json.RawMessage(`{}`))
		assert.Error(t, err)
		_, err = closed.UpdatePreparationMinutes(ctx, 1)
		assert.Error(t, err)
	})

	t.Run("invalid JSON is rejected by the database", func(t *testing.T) {
		_, err := repo.UpdateOpeningHours(ctx, json.RawMessage(`{not json`))
		assert.Error(t, err)
	})
}

func TestScheduleOverrideRepository(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	repo := NewScheduleOverrideRepository(&db.DBPool{Customer: tdb.DB, Admin: tdb.DB})
	ctx := t.Context()
	note := "Holiday"

	t.Run("Upsert creates a special-hours day and a closed day stored as JSON null", func(t *testing.T) {
		special, err := repo.Upsert(ctx, &domain.ScheduleOverride{Date: day(2026, 12, 24), Schedule: json.RawMessage(`{"open":"10:00","close":"16:00"}`), Note: &note})
		require.NoError(t, err)
		assert.Equal(t, "2026-12-24", special.DateKey())
		assert.False(t, special.Closed)
		assert.Equal(t, "Holiday", *special.Note)
		parsed, err := special.ParsedSchedule()
		require.NoError(t, err)
		assert.Equal(t, "16:00", parsed.Close)

		closed, err := repo.Upsert(ctx, &domain.ScheduleOverride{Date: day(2026, 12, 25), Closed: true})
		require.NoError(t, err)
		assert.True(t, closed.Closed)
		assert.JSONEq(t, `null`, string(closed.Schedule))
		assert.Nil(t, closed.Note)
	})

	t.Run("Upsert on an existing date replaces it", func(t *testing.T) {
		updated, err := repo.Upsert(ctx, &domain.ScheduleOverride{Date: day(2026, 12, 24), Closed: true})
		require.NoError(t, err)
		assert.True(t, updated.Closed)
		assert.Nil(t, updated.Note, "the note is replaced too")
		got, err := repo.Get(ctx, day(2026, 12, 24))
		require.NoError(t, err)
		assert.True(t, got.Closed)
		assert.True(t, !got.UpdatedAt.Before(got.CreatedAt))
	})

	t.Run("an open day without a schedule is stored with JSON null hours", func(t *testing.T) {
		// scheduleParam stores JSON null, which satisfies "schedule IS NOT NULL"; the day is then
		// open with no hours, which ParsedSchedule reports as nil (treated as closed by callers).
		ov, err := repo.Upsert(ctx, &domain.ScheduleOverride{Date: day(2026, 12, 26)})
		require.NoError(t, err)
		s, err := ov.ParsedSchedule()
		require.NoError(t, err)
		assert.Nil(t, s)
	})

	t.Run("a row whose schedule is SQL NULL is read as JSON null instead of failing the list", func(t *testing.T) {
		_, err := tdb.DB.ExecContext(ctx, `INSERT INTO restaurant_schedule_overrides (date, closed, schedule) VALUES ('2026-12-27', true, NULL)`)
		require.NoError(t, err)
		got, err := repo.Get(ctx, day(2026, 12, 27))
		require.NoError(t, err)
		assert.JSONEq(t, `null`, string(got.Schedule))
		all, err := repo.ListFromDate(ctx, day(2026, 12, 1))
		require.NoError(t, err)
		assert.Len(t, all, 4)
	})

	t.Run("List is inclusive, ordered by date; ListFromDate is open-ended", func(t *testing.T) {
		got, err := repo.List(ctx, day(2026, 12, 25), day(2026, 12, 26))
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "2026-12-25", got[0].DateKey())
		assert.Equal(t, "2026-12-26", got[1].DateKey())

		none, err := repo.List(ctx, day(2027, 1, 1), day(2027, 1, 31))
		require.NoError(t, err)
		assert.Empty(t, none)

		from, err := repo.ListFromDate(ctx, day(2026, 12, 26))
		require.NoError(t, err)
		require.Len(t, from, 2)
		assert.Equal(t, "2026-12-26", from[0].DateKey())
		assert.Equal(t, "2026-12-27", from[1].DateKey())
	})

	t.Run("Get reports a missing day as sql.ErrNoRows", func(t *testing.T) {
		_, err := repo.Get(ctx, day(2030, 1, 1))
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("Delete removes one day and is a no-op for an unknown one", func(t *testing.T) {
		require.NoError(t, repo.Delete(ctx, day(2026, 12, 25)))
		_, err := repo.Get(ctx, day(2026, 12, 25))
		assert.ErrorIs(t, err, sql.ErrNoRows)
		_, err = repo.Get(ctx, day(2026, 12, 24))
		assert.NoError(t, err)
		require.NoError(t, repo.Delete(ctx, day(2031, 1, 1)))
	})

	t.Run("a closed connection is reported by every method", func(t *testing.T) {
		closed := NewScheduleOverrideRepository(closedPool(t))
		_, err := closed.List(ctx, day(2026, 1, 1), day(2026, 1, 2))
		assert.Error(t, err)
		_, err = closed.ListFromDate(ctx, day(2026, 1, 1))
		assert.Error(t, err)
		_, err = closed.Get(ctx, day(2026, 1, 1))
		assert.Error(t, err)
		_, err = closed.Upsert(ctx, &domain.ScheduleOverride{Date: day(2026, 1, 1), Closed: true})
		assert.Error(t, err)
		assert.Error(t, closed.Delete(ctx, day(2026, 1, 1)))
	})
}

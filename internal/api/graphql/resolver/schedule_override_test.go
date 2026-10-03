package resolver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tsb-service/internal/api/graphql/model"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/pubsub"
)

// overrideRecorder records the dates the override mutations hand to the
// service, which writes them to a DATE column as given.
type overrideRecorder struct {
	restaurantApplication.RestaurantService
	upserted []time.Time
	deleted  []time.Time
}

func (f *overrideRecorder) UpsertOverride(_ context.Context, date time.Time, closed bool, schedule json.RawMessage, note *string) (*restaurantDomain.ScheduleOverride, error) {
	f.upserted = append(f.upserted, date)
	return &restaurantDomain.ScheduleOverride{Date: date, Closed: closed, Schedule: schedule, Note: note}, nil
}

func (f *overrideRecorder) DeleteOverride(_ context.Context, date time.Time) error {
	f.deleted = append(f.deleted, date)
	return nil
}

func (f *overrideRecorder) ListOverrides(context.Context, time.Time, time.Time) ([]*restaurantDomain.ScheduleOverride, error) {
	return nil, nil
}

func (f *overrideRecorder) GetConfig(context.Context) (*restaurantDomain.RestaurantConfig, error) {
	return &restaurantDomain.RestaurantConfig{}, nil
}

func TestScheduleOverrideDateIsBrusselsCalendarDay(t *testing.T) {
	tests := []struct {
		name string
		sent string
		want string
	}{
		// Dashboard before the fix: local midnight through toISOString().
		{"Brussels midnight as UTC, summer time", "2026-10-04T22:00:00Z", "2026-10-05"},
		{"Brussels midnight as UTC, winter time", "2026-12-04T23:00:00Z", "2026-12-05"},
		// Dashboard after the fix, tsb-mcp, and dates read back from the API.
		{"UTC midnight", "2026-10-05T00:00:00Z", "2026-10-05"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sent, err := time.Parse(time.RFC3339, tt.sent)
			if err != nil {
				t.Fatal(err)
			}
			svc := &overrideRecorder{}
			m := &mutationResolver{&Resolver{RestaurantService: svc, Broker: pubsub.NewBroker()}}

			ov, err := m.UpsertScheduleOverride(context.Background(), model.ScheduleOverrideInput{Date: sent, Closed: true})
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if _, err := m.DeleteScheduleOverride(context.Background(), sent); err != nil {
				t.Fatalf("delete: %v", err)
			}

			for op, got := range map[string]time.Time{"upsert": svc.upserted[0], "delete": svc.deleted[0], "returned": ov.Date} {
				if d := got.UTC().Format(time.RFC3339); d != tt.want+"T00:00:00Z" {
					t.Errorf("%s date = %s, want %sT00:00:00Z", op, d, tt.want)
				}
			}
		})
	}
}

package timezone

import (
	"testing"
	"time"
)

func TestDate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"Brussels midnight, summer time", "2026-10-05T00:00:00+02:00", "2026-10-05"},
		{"Brussels midnight sent as UTC, summer time", "2026-10-04T22:00:00Z", "2026-10-05"},
		{"Brussels midnight sent as UTC, winter time", "2026-12-04T23:00:00Z", "2026-12-05"},
		{"UTC midnight", "2026-10-05T00:00:00Z", "2026-10-05"},
		{"late evening in Brussels", "2026-10-05T21:59:00Z", "2026-10-05"},
		{"Shanghai midnight is still the day before in Brussels", "2026-10-05T00:00:00+08:00", "2026-10-04"},
		{"DST change day", "2026-10-24T22:00:00Z", "2026-10-25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := time.Parse(time.RFC3339, tt.in)
			if err != nil {
				t.Fatal(err)
			}
			got := Date(in)
			if got.Location() != time.UTC || got.Hour() != 0 || got.Minute() != 0 {
				t.Errorf("Date(%s) = %s, want UTC midnight", tt.in, got)
			}
			if d := got.Format(time.DateOnly); d != tt.want {
				t.Errorf("Date(%s) = %s, want %s", tt.in, d, tt.want)
			}
		})
	}
}

func TestInConvertsTheWallClockButNotTheInstant(t *testing.T) {
	utc := time.Date(2026, 7, 1, 10, 30, 0, 0, time.UTC) // summer: Brussels is UTC+2
	got := In(utc)
	if !got.Equal(utc) {
		t.Fatalf("In changed the instant: %s != %s", got, utc)
	}
	if got.Location() != Location {
		t.Errorf("location = %s, want %s", got.Location(), Location)
	}
	if got.Format("15:04") != "12:30" {
		t.Errorf("summer wall clock = %s, want 12:30", got.Format("15:04"))
	}

	winter := In(time.Date(2026, 12, 1, 10, 30, 0, 0, time.UTC))
	if winter.Format("15:04") != "11:30" {
		t.Errorf("winter wall clock = %s, want 11:30", winter.Format("15:04"))
	}
}

func TestRestaurantLocationIsBrussels(t *testing.T) {
	if RestaurantTZ != "Europe/Brussels" || Location.String() != RestaurantTZ {
		t.Fatalf("Location = %v, want %s (is tzdata installed?)", Location, RestaurantTZ)
	}
}

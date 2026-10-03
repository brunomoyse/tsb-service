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

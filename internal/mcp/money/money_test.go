package money

import (
	"errors"
	"strings"
	"testing"
)

func TestToCents(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"12.5", 1250, false},
		{"12.50", 1250, false},
		{"0", 0, false},
		{"4", 400, false},
		{"0.30", 30, false},
		{"14.99", 1499, false},
		{"1.005", 0, true},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := ToCents(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("ToCents(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("ToCents(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestFromCents(t *testing.T) {
	tests := map[int64]string{1250: "12.50", 0: "0.00", 5: "0.05", 1450: "14.50", 100000: "1000.00"}
	for in, want := range tests {
		if got := FromCents(in); got != want {
			t.Errorf("FromCents(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckPriceChange(t *testing.T) {
	tests := []struct {
		name            string
		current, newVal int64
		maxPct          int
		wantErr         bool
		wantInMsg       string
	}{
		{"same price", 1000, 1000, 50, false, ""},
		{"+50% exactly", 1000, 1500, 50, false, ""},
		{"-50% exactly", 1000, 500, 50, false, ""},
		{"over +50%", 1000, 1501, 50, true, "between 5.00 EUR and 15.00 EUR"},
		{"under -50%", 1000, 499, 50, true, "between 5.00 EUR and 15.00 EUR"},
		{"zero", 1000, 0, 50, true, "greater than 0"},
		{"negative", 1000, -100, 50, true, "greater than 0"},
		{"odd cents bound", 999, 1498, 50, false, ""},
		{"odd cents just over", 999, 1499, 50, true, "14.98 EUR"},
		{"custom 10%", 1000, 1150, 10, true, "±10%"},
		{"no current price", 0, 1200, 50, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPriceChange(tt.current, tt.newVal, tt.maxPct)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrOutOfBounds) {
					t.Errorf("error does not wrap ErrOutOfBounds: %v", err)
				}
				if !strings.Contains(err.Error(), tt.wantInMsg) {
					t.Errorf("error %q does not contain %q", err, tt.wantInMsg)
				}
			}
		})
	}
}

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

func TestMustCents(t *testing.T) {
	tests := map[string]int64{"12.5": 1250, "0": 0, "": 0, "abc": 0, "1.005": 0, "4": 400, "-0.50": -50, "1e2": 10000}
	for in, want := range tests {
		if got := MustCents(in); got != want {
			t.Errorf("MustCents(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestToCentsEdgeCases(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr string
	}{
		{"-5.25", -525, ""},
		{"0.00", 0, ""},
		{"1000000.99", 100000099, ""},
		{"1.50000", 150, ""}, // trailing zeros are not extra precision
		{"0.001", 0, "more than two decimals"},
		{"12,5", 0, "invalid amount"},
		{" 12.5", 0, "invalid amount"},
	}
	for _, tt := range tests {
		got, err := ToCents(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ToCents(%q) err = %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ToCents(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
}

func TestFormatAndFromCentsRoundTrip(t *testing.T) {
	if Format(1250) != "12.50 EUR" || Format(5) != "0.05 EUR" || Format(0) != "0.00 EUR" {
		t.Error("Format")
	}
	if FromCents(-5) != "-0.05" {
		t.Errorf("FromCents(-5) = %s", FromCents(-5))
	}
	for _, c := range []int64{0, 1, 99, 100, 1450, 123456789} {
		if got, err := ToCents(FromCents(c)); err != nil || got != c {
			t.Errorf("round trip of %d: %d %v", c, got, err)
		}
	}
}

func TestBounds(t *testing.T) {
	tests := []struct {
		current        int64
		pct            int
		wantLo, wantHi int64
	}{
		{1000, 50, 500, 1500},
		{999, 50, 500, 1498}, // the delta is truncated: 499.5 -> 499
		{1, 50, 1, 1},        // a delta below a cent is no delta
		{1, 100, 1, 2},       // the lower bound never reaches 0
		{100, 100, 1, 200},
		{100, 1000, 1, 1100},
		{450, 10, 405, 495},
		{0, 50, 1, 0}, // no current price: callers skip the check
	}
	for _, tt := range tests {
		lo, hi := Bounds(tt.current, tt.pct)
		if lo != tt.wantLo || hi != tt.wantHi {
			t.Errorf("Bounds(%d, %d) = %d, %d; want %d, %d", tt.current, tt.pct, lo, hi, tt.wantLo, tt.wantHi)
		}
	}
}

func TestCheckPriceChangeNeverAllowsAFreeProduct(t *testing.T) {
	// Whatever the percentage, the new price must be at least one cent.
	if err := CheckPriceChange(100, 0, 1000); err == nil {
		t.Error("0 must be refused")
	}
	if err := CheckPriceChange(100, 1, 100); err != nil {
		t.Errorf("1 cent within -100%%: %v", err)
	}
	if err := CheckPriceChange(1, 2, 50); err == nil {
		t.Error("1 cent can only stay 1 cent at ±50%")
	}
	if err := CheckPriceChange(1, 1, 50); err != nil {
		t.Errorf("same price: %v", err)
	}
	if err := CheckPriceChange(-5, 100, 50); err != nil {
		t.Errorf("a negative current price has no reference: %v", err)
	}
}

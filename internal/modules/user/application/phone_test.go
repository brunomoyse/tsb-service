package application

import (
	"errors"
	"testing"
)

func TestNormalizePhoneNumber(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantNil bool
		wantErr error
	}{
		{
			name:    "empty string returns nil without error",
			raw:     "",
			wantNil: true,
		},
		{
			name:    "whitespace returns nil without error",
			raw:     "   ",
			wantNil: true,
		},
		{
			name: "valid BE mobile in E.164 is preserved",
			raw:  "+32470123456",
			want: "+32470123456",
		},
		{
			name: "valid BE mobile typed nationally is normalized to E.164",
			raw:  "0470123456",
			want: "+32470123456",
		},
		{
			name: "valid FR mobile in E.164 is preserved",
			raw:  "+33612345678",
			want: "+33612345678",
		},
		{
			// Regression: libphonenumber-js with default `min` metadata
			// considers this valid (length-only check); the Go library
			// rejects it correctly because BE mobile numbers must be 9
			// digits after +32, not 8.
			name:    "BE mobile with one digit missing is rejected",
			raw:     "+3247929760",
			wantErr: ErrInvalidPhoneNumber,
		},
		{
			name:    "gibberish is rejected",
			raw:     "not-a-number",
			wantErr: ErrInvalidPhoneNumber,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePhoneNumber(tt.raw)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if got != nil {
					t.Fatalf("got = %q, want nil on error", *got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Fatalf("got = %q, want nil", *got)
				}
				return
			}

			if got == nil {
				t.Fatalf("got = nil, want %q", tt.want)
			}
			if *got != tt.want {
				t.Fatalf("got = %q, want %q", *got, tt.want)
			}
		})
	}
}

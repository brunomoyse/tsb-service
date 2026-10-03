package directives

import (
	"context"
	"testing"

	"tsb-service/pkg/utils"
)

func TestAdminAndStaffScopes(t *testing.T) {
	next := func(context.Context) (any, error) { return "ok", nil }

	base := utils.SetUserID(t.Context(), "11111111-1111-1111-1111-111111111111")
	admin := utils.SetIsAdmin(base, true)
	pos := utils.SetIsPOS(base, true)

	cases := []struct {
		name      string
		ctx       context.Context
		wantAdmin bool
		wantStaff bool
	}{
		{"admin", admin, true, true},
		{"pos device", pos, false, true},
		{"customer", base, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Admin(tc.ctx, nil, next)
			if (err == nil) != tc.wantAdmin {
				t.Fatalf("@admin allowed=%v, want %v (err=%v)", err == nil, tc.wantAdmin, err)
			}
			_, err = Staff(tc.ctx, nil, next)
			if (err == nil) != tc.wantStaff {
				t.Fatalf("@staff allowed=%v, want %v (err=%v)", err == nil, tc.wantStaff, err)
			}
		})
	}
}

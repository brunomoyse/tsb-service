package resolver

import (
	"context"
	"encoding/json"
	"testing"

	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	userApplication "tsb-service/internal/modules/user/application"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/utils"
)

// fakeUserService embeds the interface (nil) and only implements the one method
// the review-bypass path calls, so the fake satisfies UserService without
// stubbing every method.
type fakeUserService struct {
	userApplication.UserService
	user *userDomain.User
}

func (f fakeUserService) GetUserByID(_ context.Context, _ string) (*userDomain.User, error) {
	return f.user, nil
}

type fakeRestaurantService struct {
	restaurantApplication.RestaurantService
	cfg *restaurantDomain.RestaurantConfig
}

func (f fakeRestaurantService) GetConfigWithOverrides(_ context.Context) (*restaurantDomain.RestaurantConfig, map[string]*restaurantDomain.ScheduleOverride, error) {
	return f.cfg, nil, nil
}

// allClosedOpeningHours marks every weekday closed so IsOrderingCurrentlyOpen is
// deterministically false and AvailableSlotsToday is empty regardless of when
// the test runs (the resolvers read time.Now() internally).
func allClosedOpeningHours(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(restaurantDomain.OpeningHours{
		"monday": nil, "tuesday": nil, "wednesday": nil, "thursday": nil,
		"friday": nil, "saturday": nil, "sunday": nil,
	})
	if err != nil {
		t.Fatalf("marshal closed hours: %v", err)
	}
	return raw
}

func closedConfigResolver(t *testing.T, u *userDomain.User) *restaurantConfigResolver {
	t.Helper()
	cfg := &restaurantDomain.RestaurantConfig{
		OrderingEnabled:    true,
		OpeningHours:       allClosedOpeningHours(t),
		PreparationMinutes: 30,
	}
	t.Setenv("REVIEW_ZITADEL_SUBS", reviewZitadelSub)
	r := &Resolver{
		RestaurantService: fakeRestaurantService{cfg: cfg},
		UserService:       fakeUserService{user: u},
	}
	return &restaurantConfigResolver{r}
}

// reviewZitadelSub is the store-review account allowlisted by closedConfigResolver.
const reviewZitadelSub = "372000000000000001"

func reviewUser() *userDomain.User {
	sub := reviewZitadelSub
	return &userDomain.User{
		Email:         "abc@privaterelay.appleid.com",
		FirstName:     "John",
		LastName:      "Apple",
		ZitadelUserID: &sub,
	}
}

func normalUser() *userDomain.User {
	sub := "372000000000000002"
	return &userDomain.User{
		Email:         "jane@gmail.com",
		FirstName:     "Jane",
		LastName:      "Doe",
		ZitadelUserID: &sub,
	}
}

// selfStyledReviewer copied the review account's name and relay email into
// their own profile (updateMe) but is not allowlisted.
func selfStyledReviewer() *userDomain.User {
	u := reviewUser()
	sub := "372000000000000003"
	u.ZitadelUserID = &sub
	return u
}

// authedCtx returns a context carrying a userID, as the @auth middleware sets.
func authedCtx() context.Context {
	return utils.SetUserID(context.Background(), "11111111-1111-1111-1111-111111111111")
}

// TestIsOrderingCurrentlyOpen_ReviewBypass: while closed, only a store-review
// account sees ordering reported as open. Real customers stay closed.
func TestIsOrderingCurrentlyOpen_ReviewBypass(t *testing.T) {
	t.Run("review user sees open while closed", func(t *testing.T) {
		res := closedConfigResolver(t, reviewUser())
		open, err := res.IsOrderingCurrentlyOpen(authedCtx(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !open {
			t.Errorf("expected ordering reported open for review user while closed")
		}
	})

	t.Run("normal user stays closed", func(t *testing.T) {
		res := closedConfigResolver(t, normalUser())
		open, err := res.IsOrderingCurrentlyOpen(authedCtx(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if open {
			t.Errorf("expected ordering reported closed for a normal user")
		}
	})

	t.Run("anonymous caller stays closed", func(t *testing.T) {
		res := closedConfigResolver(t, normalUser())
		open, err := res.IsOrderingCurrentlyOpen(context.Background(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if open {
			t.Errorf("expected ordering reported closed for an anonymous caller")
		}
	})
}

// TestAvailableSlotsToday_ReviewBypass: while closed, only a store-review account
// receives synthetic fixed-time slots; real customers receive none.
func TestAvailableSlotsToday_ReviewBypass(t *testing.T) {
	t.Run("review user gets synthetic slots while closed", func(t *testing.T) {
		res := closedConfigResolver(t, reviewUser())
		slots, err := res.AvailableSlotsToday(authedCtx(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(slots) == 0 {
			t.Errorf("expected synthetic slots for review user while closed")
		}
	})

	t.Run("self-styled John Apple gets no slots while closed", func(t *testing.T) {
		res := closedConfigResolver(t, selfStyledReviewer())
		slots, err := res.AvailableSlotsToday(authedCtx(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(slots) != 0 {
			t.Errorf("profile fields alone granted the review bypass (%d slots)", len(slots))
		}
	})

	t.Run("normal user gets no slots while closed", func(t *testing.T) {
		res := closedConfigResolver(t, normalUser())
		slots, err := res.AvailableSlotsToday(authedCtx(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(slots) != 0 {
			t.Errorf("expected no slots for a normal user while closed, got %d", len(slots))
		}
	})

	t.Run("anonymous caller gets no slots while closed", func(t *testing.T) {
		res := closedConfigResolver(t, normalUser())
		slots, err := res.AvailableSlotsToday(context.Background(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(slots) != 0 {
			t.Errorf("expected no slots for an anonymous caller, got %d", len(slots))
		}
	})
}

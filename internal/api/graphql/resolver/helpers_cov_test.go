package resolver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"

	"tsb-service/internal/api/graphql/apperr"
	"tsb-service/internal/api/graphql/model"
	addressDomain "tsb-service/internal/modules/address/domain"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
)

func TestNormalizeOrderLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"fr": "fr", "EN": "en", " nl ": "nl", "zh": "zh", "fr-BE": "fr", "nl_BE": "nl", "zh-Hans-CN": "zh",
		"": "", "de": "", "klingon": "", "-fr": "", "  ": "",
	} {
		assert.Equal(t, want, normalizeOrderLanguage(in), "%q", in)
	}
}

func TestChoiceLoadError(t *testing.T) {
	choice, product := uuid.New(), uuid.New()
	gone := choiceLoadError(fmt.Errorf("lookup: %w", sql.ErrNoRows), choice, product)
	appErr, ok := apperr.From(gone)
	require.True(t, ok)
	assert.Equal(t, apperr.CodeSelectionInvalid, appErr.Code)
	assert.Equal(t, product.String(), appErr.Extensions()["productId"])
	assert.NotContains(t, gone.Error(), "sql:", "the driver text never reaches the customer")

	fault := choiceLoadError(errors.New("connection reset"), choice, product)
	_, typed := apperr.From(fault)
	assert.False(t, typed, "any other failure is a server fault, not a stale basket")
	assert.Contains(t, fault.Error(), choice.String())
}

func TestMappersOfSmallTypes(t *testing.T) {
	t.Run("address", func(t *testing.T) {
		box, lat, lng, dur := "B", 50.1, 5.2, 300
		got := ToGQLAddress(&addressDomain.Address{ID: "p", StreetName: "S", HouseNumber: "1", BoxNumber: &box, Postcode: "4000",
			MunicipalityName: "L", Distance: 12, Lat: &lat, Lng: &lng, Duration: &dur})
		assert.Equal(t, &model.Address{ID: "p", StreetName: "S", HouseNumber: "1", BoxNumber: &box, Postcode: "4000",
			MunicipalityName: "L", Distance: 12, Lat: &lat, Lng: &lng, Duration: &dur}, got)
	})

	t.Run("translations both ways", func(t *testing.T) {
		desc := "d"
		got := ToGQLTranslation(&productDomain.Translation{Language: "fr", Name: "n", Description: &desc})
		assert.Equal(t, "fr", got.Language)
		assert.Equal(t, &desc, got.Description)
		assert.Nil(t, toDomainTranslations(nil))
		in := []*model.TranslationInput{{Language: "en", Name: "x", Description: &desc}}
		assert.Equal(t, []productDomain.Translation{{Language: "en", Name: "x", Description: &desc}}, toDomainTranslations(in))
	})

	t.Run("day schedules", func(t *testing.T) {
		assert.Nil(t, toGQLDaySchedule(nil))
		assert.Equal(t, &model.DaySchedule{Open: "11:00", Close: "14:00"}, toGQLDaySchedule(&restaurantDomain.DaySchedule{Open: "11:00", Close: "14:00"}))
		got := toGQLDaySchedule(&restaurantDomain.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"})
		require.NotNil(t, got.DinnerOpen)
		assert.Equal(t, "18:00", *got.DinnerOpen)
		assert.Equal(t, "22:00", *got.DinnerClose)
		assert.Nil(t, toScheduleMap(nil))
		assert.Equal(t, map[string]string{"open": "1", "close": "2"}, toScheduleMap(&model.DayScheduleInput{Open: "1", Close: "2"}))
	})

	t.Run("opening hours input keeps closed days as null", func(t *testing.T) {
		raw, err := marshalOpeningHoursInput(model.OpeningHoursInput{Monday: &model.DayScheduleInput{Open: "11:00", Close: "14:00"}})
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Len(t, got, 7)
		assert.Equal(t, map[string]any{"open": "11:00", "close": "14:00"}, got["monday"])
		assert.Nil(t, got["sunday"])
	})

	t.Run("a restaurant config with null ordering hours has none", func(t *testing.T) {
		got := toGQLRestaurantConfig(&restaurantDomain.RestaurantConfig{OpeningHours: json.RawMessage(`{"monday":null}`), OrderingHours: json.RawMessage(`null`), PreparationMinutes: 20})
		assert.Nil(t, got.OrderingHours)
		assert.Equal(t, 20, got.PreparationMinutes)
		got = toGQLRestaurantConfig(&restaurantDomain.RestaurantConfig{OpeningHours: json.RawMessage(`{}`)})
		assert.Nil(t, got.OrderingHours)
		got = toGQLRestaurantConfig(&restaurantDomain.RestaurantConfig{OpeningHours: json.RawMessage(`{}`), OrderingHours: json.RawMessage(`{"friday":null}`)})
		assert.NotNil(t, got.OrderingHours)
	})

	t.Run("ordering policy without excluded postcodes lists none, not null", func(t *testing.T) {
		got := toGQLOrderingPolicy(restaurantDomain.OrderingPolicy{PickupDiscountRate: decimal.New(10, -2)})
		assert.NotNil(t, got.ExcludedPostcodes)
		assert.Empty(t, got.ExcludedPostcodes)
		assert.InDelta(t, 0.1, got.PickupDiscountRate, 1e-9)
	})

	t.Run("a schedule override with a broken schedule has none", func(t *testing.T) {
		d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		got := toGQLScheduleOverride(&restaurantDomain.ScheduleOverride{Date: d, Schedule: json.RawMessage(`{not json`)})
		assert.Nil(t, got.Schedule)
		got = toGQLScheduleOverride(&restaurantDomain.ScheduleOverride{Date: d, Schedule: json.RawMessage(`{"open":"10:00","close":"12:00"}`)})
		require.NotNil(t, got.Schedule)
		assert.Equal(t, "10:00", got.Schedule.Open)
	})

	t.Run("pointers with defaults", func(t *testing.T) {
		s, f := "x", 1.5
		assert.Equal(t, "x", derefOrEmpty(&s))
		assert.Equal(t, "", derefOrEmpty(nil))
		assert.Equal(t, 1.5, derefFloatOrZero(&f))
		assert.Equal(t, 0.0, derefFloatOrZero(nil))
	})

	t.Run("the e-mail context ends by itself", func(t *testing.T) {
		ctx, cancel := emailContext()
		defer cancel()
		dl, ok := ctx.Deadline()
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(30*time.Second), dl, 5*time.Second)
	})

	t.Run("an address rebuilt from an order", func(t *testing.T) {
		assert.Nil(t, addressFromOrder(&orderDomain.Order{}), "a pickup order has no address")
		street, house, post, city, place, box := "Rue", "2", "4000", "Liège", "place-1", "C"
		lat, dist := 50.5, 1200.0
		got := addressFromOrder(&orderDomain.Order{StreetName: &street, HouseNumber: &house, Postcode: &post, MunicipalityName: &city,
			AddressPlaceID: &place, AddressDistance: &dist, BoxNumber: &box, AddressLat: &lat})
		assert.Equal(t, &addressDomain.Address{ID: "place-1", StreetName: "Rue", HouseNumber: "2", BoxNumber: &box, Postcode: "4000",
			MunicipalityName: "Liège", Distance: 1200, Lat: &lat}, got)
		bare := addressFromOrder(&orderDomain.Order{StreetName: &street, HouseNumber: &house, Postcode: &post, MunicipalityName: &city})
		assert.Empty(t, bare.ID)
		assert.Zero(t, bare.Distance)
	})
}

func TestParseHHMMToMinutes(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "11:30": 690, "23:59": 1439} {
		got, ok := parseHHMMToMinutes(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "1130", "25:00", "ab:cd", "11:60"} {
		_, ok := parseHHMMToMinutes(bad)
		assert.False(t, ok, bad)
	}
}

func TestIsSlotInAllowedInterval(t *testing.T) {
	s := &restaurantDomain.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"}
	for mins, want := range map[int]bool{
		11*60 + 15: false, // inside the first 30 minutes after opening
		11*60 + 30: true, 14 * 60: true, 14*60 + 15: false,
		16 * 60: false, 18*60 + 30: true, 22 * 60: true, 22*60 + 15: false,
	} {
		assert.Equal(t, want, isSlotInAllowedInterval(mins, s), "%d", mins)
	}
	broken := &restaurantDomain.DaySchedule{Open: "later", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "20:00"}
	assert.True(t, isSlotInAllowedInterval(19*60, broken), "an unreadable interval is skipped, the other still counts")
	assert.False(t, isSlotInAllowedInterval(12*60, broken))
}

func TestResolveSchedule(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) // a Monday
	hours := func(v string) *restaurantDomain.RestaurantConfig {
		return &restaurantDomain.RestaurantConfig{OpeningHours: json.RawMessage(v)}
	}
	cfg := hours(`{"monday":{"open":"11:00","close":"14:00"}}`)
	assert.Equal(t, "11:00", resolveSchedule(cfg, nil, now).Open)
	assert.Nil(t, resolveSchedule(hours(`{"monday":null}`), nil, now))
	assert.Nil(t, resolveSchedule(hours(`{}`), nil, now))
	assert.Nil(t, resolveSchedule(hours(`not json`), nil, now))

	ordering := &restaurantDomain.RestaurantConfig{
		OpeningHours: json.RawMessage(`{"monday":{"open":"11:00","close":"14:00"}}`), OrderingHours: json.RawMessage(`{"monday":{"open":"12:00","close":"13:00"}}`)}
	assert.Equal(t, "12:00", resolveSchedule(ordering, nil, now).Open, "ordering hours win over opening hours")

	key := "2026-10-05"
	closed := map[string]*restaurantDomain.ScheduleOverride{key: {Closed: true}}
	assert.Nil(t, resolveSchedule(cfg, closed, now))
	custom := map[string]*restaurantDomain.ScheduleOverride{key: {Schedule: json.RawMessage(`{"open":"09:00","close":"10:00"}`)}}
	assert.Equal(t, "09:00", resolveSchedule(cfg, custom, now).Open)
	broken := map[string]*restaurantDomain.ScheduleOverride{key: {Schedule: json.RawMessage(`{`)}}
	assert.Nil(t, resolveSchedule(cfg, broken, now), "an override that cannot be read closes the day")
	empty := map[string]*restaurantDomain.ScheduleOverride{key: nil}
	assert.Equal(t, "11:00", resolveSchedule(cfg, empty, now).Open, "a nil override is no override")
}

func TestValidateDiscount(t *testing.T) {
	for _, c := range []struct {
		typ  couponDomain.DiscountType
		val  string
		fail bool
	}{
		{couponDomain.DiscountTypeFixed, "5", false}, {couponDomain.DiscountTypePercentage, "100", false},
		{couponDomain.DiscountTypePercentage, "100.01", true}, {couponDomain.DiscountTypeFixed, "0", true},
		{couponDomain.DiscountTypeFixed, "-1", true}, {"other", "5", true}, {"", "5", true},
	} {
		err := validateDiscount(c.typ, decimal.RequireFromString(c.val))
		assert.Equal(t, c.fail, err != nil, "%s %s", c.typ, c.val)
	}
}

func TestCouponCheckFailure(t *testing.T) {
	cause := errors.New("db down")
	got, ok := couponCheckFailure(fmt.Errorf("wrapped: %w", &couponDomain.CheckFailedError{Err: cause}))
	require.True(t, ok)
	assert.Equal(t, apperr.CodeCouponCheckFailed, got.Code)
	_, ok = couponCheckFailure(errors.New("invalid or expired coupon"))
	assert.False(t, ok)
}

func TestIsUniqueViolations(t *testing.T) {
	assert.False(t, isUniqueViolation(errors.New("x")))
	assert.False(t, isActiveCouponOrderConflict(errors.New("x")))
	assert.False(t, isActiveCouponOrderConflict(nil))
}

func TestErrorPresenterBranches(t *testing.T) {
	op := func(ctx context.Context) context.Context {
		return graphql.WithOperationContext(ctx, &graphql.OperationContext{
			OperationName: "Op", RawQuery: "query Op { me { id } }", Operation: &ast.OperationDefinition{Operation: ast.Query},
		})
	}
	t.Run("a typed user error keeps its code and parameters and its message", func(t *testing.T) {
		got := ErrorPresenter(op(t.Context()), apperr.New(apperr.CodeUserError, "nope").With("field", "x"))
		assert.Equal(t, "nope", got.Message)
		assert.Equal(t, "USER_ERROR", got.Extensions["code"])
		assert.Equal(t, "x", got.Extensions["field"])
	})
	t.Run("an unexpected error is hidden from the client", func(t *testing.T) {
		got := ErrorPresenter(op(t.Context()), errors.New("pq: relation \"orders\" does not exist"))
		assert.Equal(t, "Internal server error", got.Message)
	})
	t.Run("a client disconnect is not a fault and keeps its text", func(t *testing.T) {
		got := ErrorPresenter(op(t.Context()), fmt.Errorf("query: %w", context.Canceled))
		assert.Contains(t, got.Message, "context canceled")
		got = ErrorPresenter(op(t.Context()), errors.New("pq: canceling statement due to user request"))
		assert.Contains(t, got.Message, "canceling statement")
	})
	t.Run("a request rejected before execution is client noise", func(t *testing.T) {
		got := ErrorPresenter(op(t.Context()), errors.New("input: variable \"id\" is required"))
		assert.Contains(t, got.Message, "variable")
		noOperation := graphql.WithOperationContext(t.Context(), &graphql.OperationContext{RawQuery: "{"})
		got = ErrorPresenter(noOperation, errors.New("parse error"))
		assert.Contains(t, got.Message, "parse error")
	})
}

package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/model"
	addressDomain "tsb-service/internal/modules/address/domain"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	userDomain "tsb-service/internal/modules/user/domain"

	"github.com/shopspring/decimal"
)

// Map applies fn to every element of in, returning a new slice.
func Map[T any, U any](in []T, fn func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = fn(v)
	}
	return out
}

// decimalPtrStr returns a pointer to the string form of d, or nil if d is nil.
func decimalPtrStr(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// ToGQLProduct converts a domain.Product into the GraphQL model.Product.
func ToGQLProduct(p *productDomain.Product, lang string) *model.Product {
	return &model.Product{
		ID:             p.ID,
		CreatedAt:      p.CreatedAt,
		Price:          p.Price.String(),
		Code:           p.Code,
		Slug:           *p.Slug,
		PieceCount:     p.PieceCount,
		IsVisible:      p.IsVisible,
		IsAvailable:    p.IsAvailable,
		IsHalal:        p.IsHalal,
		IsSpicy:        p.IsSpicy,
		IsDiscountable: p.IsDiscountable,
		IsVegetarian:   p.IsVegetarian,
		VatCategory:    string(p.VatCategory),
		Name:           p.GetTranslationFor(lang).Name,
		Description:    p.GetTranslationFor(lang).Description,
	}
}

// ToGQLProductCategory converts a domain.Category into the GraphQL model.ProductCategory.
func ToGQLProductCategory(c *productDomain.Category, lang string) *model.ProductCategory {
	return &model.ProductCategory{
		ID:    c.ID,
		Name:  c.GetTranslationFor(lang).Name,
		Order: c.Order,
	}
}

func ToGQLUser(u *userDomain.User) *model.User {
	return &model.User{
		ID:                  u.ID,
		Email:               u.Email,
		FirstName:           u.FirstName,
		LastName:            u.LastName,
		PhoneNumber:         u.PhoneNumber,
		NotifyMarketing:     u.NotifyMarketing,
		NotifyOrderUpdates:  u.NotifyOrderUpdates,
		DeletionRequestedAt: u.DeletionRequestedAt,
		DefaultPlaceID:      u.DefaultPlaceID,
	}
}

func ToGQLOrder(o *orderDomain.Order) *model.Order {
	// orderExtra is persisted as a JSON array (e.g. [{"name":"chopsticks"},
	// {"name":"sauce","options":["both"]}]). Unmarshalling into a map would
	// silently fail, leaving the field null on the wire.
	var orderExtra []any
	if len(o.OrderExtra) > 0 {
		if err := json.Unmarshal(o.OrderExtra, &orderExtra); err != nil {
			zap.L().Warn("failed to unmarshal order_extra JSON",
				zap.String("order_id", o.ID.String()),
				zap.Error(err),
			)
		}
	}

	deliveryFeeStr := decimalPtrStr(o.DeliveryFee)

	var transactionFeeStr *string
	if o.TransactionFee.GreaterThan(decimal.Zero) {
		transactionFeeStr = decimalPtrStr(&o.TransactionFee)
	}

	cashPaymentAmountStr := decimalPtrStr(o.CashPaymentAmount)

	isManual := o.IsManualAddress

	return &model.Order{
		ID:                 o.ID,
		CreatedAt:          o.CreatedAt,
		UpdatedAt:          o.UpdatedAt,
		Status:             o.OrderStatus,
		Type:               model.OrderTypeEnum(o.OrderType),
		IsOnlinePayment:    o.IsOnlinePayment,
		DiscountAmount:     o.DiscountAmount().String(),
		DeliveryFee:        deliveryFeeStr,
		TransactionFee:     transactionFeeStr,
		TotalPrice:         o.TotalPrice.String(),
		PreferredReadyTime: o.PreferredReadyTime,
		EstimatedReadyTime: o.EstimatedReadyTime,
		AddressExtra:       o.AddressExtra,
		OrderNote:          o.OrderNote,
		OrderExtra:         orderExtra,
		CouponCode:         o.CouponCode,
		// Denormalized address fields for Address() resolver
		AddressID:          o.AddressID,
		StreetName:         o.StreetName,
		HouseNumber:        o.HouseNumber,
		BoxNumber:          o.BoxNumber,
		MunicipalityName:   o.MunicipalityName,
		Postcode:           o.Postcode,
		AddressDistance:    o.AddressDistance,
		IsManualAddr:       &isManual,
		CancellationReason: o.CancellationReason,
		CashPaymentAmount:  cashPaymentAmountStr,
	}
}

func ToGQLOrderItem(oi *orderDomain.OrderProductRaw) *model.OrderItem {
	return &model.OrderItem{
		ProductID:      oi.ProductID,
		Quantity:       int(oi.Quantity),
		UnitPrice:      oi.UnitPrice.String(),
		TotalPrice:     oi.TotalPrice.String(),
		VatRateApplied: oi.VatRateApplied.StringFixed(2),
		ChoiceID:       oi.ProductChoiceID,
	}
}

func ToGQLPayment(p *paymentDomain.MolliePayment) *model.Payment {
	var links map[string]any
	_ = json.Unmarshal(p.Links, &links)

	return &model.Payment{
		ID:        p.ID,
		CreatedAt: p.CreatedAt,
		OrderID:   p.OrderID,
		Status:    string(p.Status),
		Links:     links,
	}
}

func ToGQLAddress(a *addressDomain.Address) *model.Address {
	return &model.Address{
		ID:               a.ID,
		StreetName:       a.StreetName,
		HouseNumber:      a.HouseNumber,
		BoxNumber:        a.BoxNumber,
		Postcode:         a.Postcode,
		MunicipalityName: a.MunicipalityName,
		Distance:         a.Distance,
		Lat:              a.Lat,
		Lng:              a.Lng,
		Duration:         a.Duration,
	}
}

func ToGQLTranslation(s *productDomain.Translation) *model.Translation {
	return &model.Translation{
		Language:    s.Language,
		Name:        s.Name,
		Description: s.Description,
	}
}

func toDomainTranslations(in []*model.TranslationInput) []productDomain.Translation {
	if in == nil {
		return nil
	}
	out := make([]productDomain.Translation, len(in))
	for i, t := range in {
		out[i] = productDomain.Translation{
			Language:    t.Language,
			Name:        t.Name,
			Description: t.Description,
		}
	}
	return out
}

func toGQLRestaurantConfig(c *restaurantDomain.RestaurantConfig) *model.RestaurantConfig {
	var openingHours map[string]any
	_ = json.Unmarshal(c.OpeningHours, &openingHours)

	var orderingHours map[string]any
	if len(c.OrderingHours) > 0 && string(c.OrderingHours) != "null" {
		_ = json.Unmarshal(c.OrderingHours, &orderingHours)
	}

	return &model.RestaurantConfig{
		OrderingEnabled:    c.OrderingEnabled,
		OpeningHours:       openingHours,
		OrderingHours:      orderingHours,
		PreparationMinutes: c.PreparationMinutes,
		UpdatedAt:          c.UpdatedAt,
	}
}

func toGQLScheduleOverride(ov *restaurantDomain.ScheduleOverride) *model.ScheduleOverride {
	out := &model.ScheduleOverride{
		Date:      ov.Date,
		Closed:    ov.Closed,
		Note:      ov.Note,
		UpdatedAt: ov.UpdatedAt,
	}
	if s, err := ov.ParsedSchedule(); err == nil && s != nil {
		out.Schedule = toGQLDaySchedule(s)
	}
	return out
}

func toGQLDaySchedule(s *restaurantDomain.DaySchedule) *model.DaySchedule {
	if s == nil {
		return nil
	}
	out := &model.DaySchedule{
		Open:  s.Open,
		Close: s.Close,
	}
	if s.DinnerOpen != "" {
		v := s.DinnerOpen
		out.DinnerOpen = &v
	}
	if s.DinnerClose != "" {
		v := s.DinnerClose
		out.DinnerClose = &v
	}
	return out
}

func toGQLTimeSlots(slots []restaurantDomain.TimeSlot) []*model.TimeSlot {
	out := make([]*model.TimeSlot, len(slots))
	for i, s := range slots {
		out[i] = &model.TimeSlot{Label: s.Label, Value: s.Value}
	}
	return out
}

// marshalOpeningHoursInput converts the GraphQL input into the JSONB shape
// stored in restaurant_config.opening_hours / ordering_hours.
func marshalOpeningHoursInput(hours model.OpeningHoursInput) (json.RawMessage, error) {
	hoursMap := map[string]any{
		"monday":    toScheduleMap(hours.Monday),
		"tuesday":   toScheduleMap(hours.Tuesday),
		"wednesday": toScheduleMap(hours.Wednesday),
		"thursday":  toScheduleMap(hours.Thursday),
		"friday":    toScheduleMap(hours.Friday),
		"saturday":  toScheduleMap(hours.Saturday),
		"sunday":    toScheduleMap(hours.Sunday),
	}
	b, err := json.Marshal(hoursMap)
	if err != nil {
		return nil, fmt.Errorf("marshal hours: %w", err)
	}
	return b, nil
}

// publishScheduleOverridesUpdated republishes the next batch of overrides
// (today + 1 year lookahead) so dashboard subscribers refresh after a change.
func (r *Resolver) publishScheduleOverridesUpdated(ctx context.Context) {
	overrides, err := r.RestaurantService.ListOverrides(ctx, time.Now(), time.Now().AddDate(1, 0, 0))
	if err != nil {
		return
	}
	out := make([]*model.ScheduleOverride, len(overrides))
	for i, ov := range overrides {
		out[i] = toGQLScheduleOverride(ov)
	}
	r.Broker.Publish("scheduleOverridesUpdated", out)
}

// publishRestaurantConfigUpdated republishes the config so `isCurrentlyOpen`,
// `availableSlotsToday`, etc. subscribers refresh after an override change.
func (r *Resolver) publishRestaurantConfigUpdated(ctx context.Context) {
	config, err := r.RestaurantService.GetConfig(ctx)
	if err != nil {
		return
	}
	r.Broker.Publish("restaurantConfigUpdated", toGQLRestaurantConfig(config))
}

func toScheduleMap(s *model.DayScheduleInput) any {
	if s == nil {
		return nil
	}
	m := map[string]string{
		"open":  s.Open,
		"close": s.Close,
	}
	if s.DinnerOpen != nil {
		m["dinnerOpen"] = *s.DinnerOpen
	}
	if s.DinnerClose != nil {
		m["dinnerClose"] = *s.DinnerClose
	}
	return m
}

func ToGQLCoupon(c *couponDomain.Coupon) *model.Coupon {
	var minOrderAmount *string
	if c.MinOrderAmount != nil {
		s := c.MinOrderAmount.String()
		minOrderAmount = &s
	}

	return &model.Coupon{
		ID:             c.ID,
		Code:           c.Code,
		DiscountType:   strings.ToUpper(string(c.DiscountType)),
		DiscountValue:  c.DiscountValue.String(),
		MinOrderAmount: minOrderAmount,
		MaxUses:        c.MaxUses,
		MaxUsesPerUser: c.MaxUsesPerUser,
		UsedCount:      c.UsedCount,
		IsActive:       c.IsActive,
		ValidFrom:      c.ValidFrom,
		ValidUntil:     c.ValidUntil,
		CreatedAt:      c.CreatedAt,
	}
}

// emailContext returns a background context with a 30-second timeout for async email operations.
func emailContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// deliveryFeeFromDistance computes the delivery fee based on distance in meters.
func deliveryFeeFromDistance(distance float64) decimal.Decimal {
	var dFee int64
	switch {
	case distance < 4000:
		dFee = 0
	case distance < 5000:
		dFee = 1
	case distance < 6000:
		dFee = 2
	case distance < 7000:
		dFee = 3
	case distance < 8000:
		dFee = 4
	case distance < 9000:
		dFee = 5
	default:
		dFee = 10
	}
	return decimal.NewFromInt(dFee)
}

// addressFromOrder constructs an addressDomain.Address from an order's denormalized fields.
func addressFromOrder(o *orderDomain.Order) *addressDomain.Address {
	if o.StreetName == nil {
		return nil
	}
	addr := &addressDomain.Address{
		StreetName:       *o.StreetName,
		MunicipalityName: *o.MunicipalityName,
		Postcode:         *o.Postcode,
		HouseNumber:      *o.HouseNumber,
		BoxNumber:        o.BoxNumber,
		Lat:              o.AddressLat,
		Lng:              o.AddressLng,
	}
	if o.AddressPlaceID != nil {
		addr.ID = *o.AddressPlaceID
	}
	if o.AddressDistance != nil {
		addr.Distance = *o.AddressDistance
	}
	// Duration is not available from order denormalization, leave as nil
	return addr
}

func derefOrEmpty(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}

func derefFloatOrZero(f *float64) float64 {
	if f != nil {
		return *f
	}
	return 0
}

func validatePreferredReadyTime(preferred *time.Time, config *restaurantDomain.RestaurantConfig, overrides map[string]*restaurantDomain.ScheduleOverride, now time.Time, isOpenNow bool) error {
	if preferred == nil {
		if !isOpenNow {
			return fmt.Errorf("fixed time is required while the restaurant is closed")
		}
		return nil
	}

	nowLocal := now.In(now.Location())
	slot := preferred.In(now.Location())

	if slot.Year() != nowLocal.Year() || slot.Month() != nowLocal.Month() || slot.Day() != nowLocal.Day() {
		return fmt.Errorf("preferred ready time must be on the same day")
	}

	prepBuffer := max(time.Duration(config.PreparationMinutes)*time.Minute, 15*time.Minute)
	if slot.Before(nowLocal.Add(prepBuffer)) {
		return fmt.Errorf("preferred ready time is no longer available — it is within the minimum preparation window")
	}

	if slot.Minute()%15 != 0 || slot.Second() != 0 || slot.Nanosecond() != 0 {
		return fmt.Errorf("preferred ready time must be aligned to 15-minute slots")
	}

	// Use ordering hours if set, otherwise fall back to opening hours.
	// resolveDaySchedule is not exported; replicate its priority here:
	// override > weekly. We use IsOrderingCurrentlyOpen already checks this,
	// so here we just need the resolved schedule for slot containment.
	schedule := resolveSchedule(config, overrides, now)
	if schedule == nil {
		return fmt.Errorf("ordering is closed today")
	}

	slotMins := slot.Hour()*60 + slot.Minute()
	if !isSlotInAllowedInterval(slotMins, schedule) {
		return fmt.Errorf("preferred ready time is outside allowed opening slots")
	}

	return nil
}

// resolveSchedule returns the effective schedule for `now`, using the
// same override > ordering hours > opening hours priority as domain resolution.
func resolveSchedule(config *restaurantDomain.RestaurantConfig, overrides map[string]*restaurantDomain.ScheduleOverride, now time.Time) *restaurantDomain.DaySchedule {
	// Override for today wins regardless of weekly config.
	local := now.In(now.Location())
	dateKey := local.Format("2006-01-02")
	if ov, ok := overrides[dateKey]; ok && ov != nil {
		if ov.Closed {
			return nil
		}
		if s, err := ov.ParsedSchedule(); err == nil && s != nil {
			return s
		}
		return nil
	}

	hours, err := config.GetOrderingHours()
	if err != nil || hours == nil {
		hours, err = config.GetOpeningHours()
		if err != nil {
			return nil
		}
	}
	dayName := strings.ToLower(local.Weekday().String())
	schedule, exists := hours[dayName]
	if !exists || schedule == nil {
		return nil
	}
	return schedule
}

func isSlotInAllowedInterval(slotMins int, schedule *restaurantDomain.DaySchedule) bool {
	intervals := make([][2]string, 0, 2)
	intervals = append(intervals, [2]string{schedule.Open, schedule.Close})
	if schedule.DinnerOpen != "" && schedule.DinnerClose != "" {
		intervals = append(intervals, [2]string{schedule.DinnerOpen, schedule.DinnerClose})
	}

	for _, interval := range intervals {
		openMins, okOpen := parseHHMMToMinutes(interval[0])
		closeMins, okClose := parseHHMMToMinutes(interval[1])
		if !okOpen || !okClose {
			continue
		}
		firstAllowed := openMins + 30
		if slotMins >= firstAllowed && slotMins <= closeMins {
			return true
		}
	}

	return false
}

func parseHHMMToMinutes(hhmm string) (int, bool) {
	if _, _, ok := strings.Cut(hhmm, ":"); !ok {
		return 0, false
	}

	hour, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, false
	}

	return hour.Hour()*60 + hour.Minute(), true
}

func ToGQLProductChoice(c *productDomain.ProductChoice, lang string) *model.ProductChoice {
	translations := make([]*model.ChoiceTranslation, len(c.Translations))
	for i, t := range c.Translations {
		translations[i] = &model.ChoiceTranslation{
			Locale: t.Locale,
			Name:   t.Name,
		}
	}
	return &model.ProductChoice{
		ID:            c.ID,
		ProductID:     c.ProductID,
		PriceModifier: c.PriceModifier.String(),
		SortOrder:     c.SortOrder,
		Name:          c.GetTranslationFor(lang),
		Translations:  translations,
	}
}

package resolver

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"tsb-service/internal/api/graphql/model"
	"fmt"

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
		IsVegan:        p.IsVegan,
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
		DeletionRequestedAt: u.DeletionRequestedAt,
	}
}

func ToGQLOrder(o *orderDomain.Order) *model.Order {
	var orderExtra map[string]any
	_ = json.Unmarshal(o.OrderExtra, &orderExtra)

	var deliveryFeeStr *string
	if o.DeliveryFee != nil {
		tmp := o.DeliveryFee.String()
		deliveryFeeStr = &tmp
	}

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
		TotalPrice:         o.TotalPrice.String(),
		PreferredReadyTime: o.PreferredReadyTime,
		EstimatedReadyTime: o.EstimatedReadyTime,
		AddressExtra:       o.AddressExtra,
		OrderNote:          o.OrderNote,
		OrderExtra:         orderExtra,
		CouponCode:         o.CouponCode,
		// Denormalized address fields for Address() resolver
		AddressID:        o.AddressID,
		StreetName:       o.StreetName,
		HouseNumber:      o.HouseNumber,
		BoxNumber:        o.BoxNumber,
		MunicipalityName: o.MunicipalityName,
		Postcode:         o.Postcode,
		AddressDistance:   o.AddressDistance,
		IsManualAddr:     &isManual,
	}
}

func ToGQLOrderItem(oi *orderDomain.OrderProductRaw) *model.OrderItem {
	return &model.OrderItem{
		ProductID:  oi.ProductID,
		Quantity:   int(oi.Quantity),
		UnitPrice:  oi.UnitPrice.String(),
		TotalPrice: oi.TotalPrice.String(),
		ChoiceID:   oi.ProductChoiceID,
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
	}
}

func ToGQLStreet(s *addressDomain.Street) *model.Street {
	return &model.Street{
		ID:               s.ID,
		StreetName:       s.StreetName,
		MunicipalityName: s.MunicipalityName,
		Postcode:         s.Postcode,
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

func toDomainTranslationsPtr(in []*model.TranslationInput) []productDomain.Translation {
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

func toGQLRestaurantConfig(orderingEnabled bool, openingHoursRaw json.RawMessage, orderingHoursRaw json.RawMessage, updatedAt time.Time) *model.RestaurantConfig {
	var openingHours map[string]any
	_ = json.Unmarshal(openingHoursRaw, &openingHours)

	var orderingHours map[string]any
	if len(orderingHoursRaw) > 0 && string(orderingHoursRaw) != "null" {
		_ = json.Unmarshal(orderingHoursRaw, &orderingHours)
	}

	return &model.RestaurantConfig{
		OrderingEnabled: orderingEnabled,
		OpeningHours:    openingHours,
		OrderingHours:   orderingHours,
		UpdatedAt:       updatedAt,
	}
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
	}
	if o.AddressID != nil {
		addr.ID = *o.AddressID
	}
	if o.AddressDistance != nil {
		addr.Distance = *o.AddressDistance
	}
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

func validatePreferredReadyTime(preferred *time.Time, config *restaurantDomain.RestaurantConfig, now time.Time, isOpenNow bool) error {
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

	if slot.Before(nowLocal.Add(1 * time.Hour)) {
		return fmt.Errorf("preferred ready time must be at least 1 hour from now")
	}

	if slot.Minute()%15 != 0 || slot.Second() != 0 || slot.Nanosecond() != 0 {
		return fmt.Errorf("preferred ready time must be aligned to 15-minute slots")
	}

	// Use ordering hours if set, otherwise fall back to opening hours
	hours, err := config.GetOrderingHours()
	if err != nil || hours == nil {
		hours, err = config.GetOpeningHours()
		if err != nil {
			return fmt.Errorf("failed to parse opening hours")
		}
	}

	dayName := strings.ToLower(nowLocal.Weekday().String())
	schedule, exists := hours[dayName]
	if !exists || schedule == nil {
		return fmt.Errorf("ordering is closed today")
	}

	slotMins := slot.Hour()*60 + slot.Minute()
	if !isSlotInAllowedInterval(slotMins, schedule) {
		return fmt.Errorf("preferred ready time is outside allowed opening slots")
	}

	return nil
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
	parts := strings.Split(hhmm, ":")
	if len(parts) != 2 {
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

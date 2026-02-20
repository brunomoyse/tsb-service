package resolver

import (
	"encoding/json"
	"time"
	"tsb-service/internal/api/graphql/model"
	addressDomain "tsb-service/internal/modules/address/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	paymentDomain "tsb-service/internal/modules/payment/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	userDomain "tsb-service/internal/modules/user/domain"
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
		IsDiscountable: p.IsDiscountable,
		IsVegan:        p.IsVegan,
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
		ID:          u.ID,
		Email:       u.Email,
		FirstName:   u.FirstName,
		LastName:    u.LastName,
		PhoneNumber: u.PhoneNumber,
		IsAdmin:     u.IsAdmin,
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

	return &model.Order{
		ID:                 o.ID,
		CreatedAt:          o.CreatedAt,
		UpdatedAt:          o.UpdatedAt,
		Status:             o.OrderStatus,
		Type:               model.OrderTypeEnum(o.OrderType),
		IsOnlinePayment:    o.IsOnlinePayment,
		DiscountAmount:     o.DiscountAmount.String(),
		DeliveryFee:        deliveryFeeStr,
		TotalPrice:         o.TotalPrice.String(),
		PreferredReadyTime: o.PreferredReadyTime,
		EstimatedReadyTime: o.EstimatedReadyTime,
		AddressExtra:       o.AddressExtra,
		OrderNote:          o.OrderNote,
		OrderExtra:         orderExtra,
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

func toGQLRestaurantConfig(orderingEnabled bool, openingHoursRaw json.RawMessage, updatedAt time.Time) *model.RestaurantConfig {
	var openingHours map[string]any
	_ = json.Unmarshal(openingHoursRaw, &openingHours)

	return &model.RestaurantConfig{
		OrderingEnabled: orderingEnabled,
		OpeningHours:    openingHours,
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

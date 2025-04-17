package resolver

import (
	"tsb-service/internal/api/graphql/model"
	orderDomain "tsb-service/internal/modules/order/domain"
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
		ID:          p.ID,
		CreatedAt:   p.CreatedAt,
		Price:       p.Price.String(),
		Code:        p.Code,
		Slug:        *p.Slug,
		PieceCount:  p.PieceCount,
		IsVisible:   p.IsVisible,
		IsAvailable: p.IsAvailable,
		IsHalal:     p.IsHalal,
		IsVegan:     p.IsVegan,
		Name:        p.GetTranslationFor(lang).Name,
		Description: p.GetTranslationFor(lang).Description,
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
	}
}

func ToGQLOrder(o *orderDomain.Order) *model.Order {
	return &model.Order{
		ID:                 o.ID,
		CreatedAt:          o.CreatedAt,
		UpdatedAt:          o.UpdatedAt,
		Status:             model.OrderStatusEnum(o.OrderStatus),
		Type:               model.OrderTypeEnum(o.OrderType),
		IsOnlinePayment:    o.IsOnlinePayment,
		DiscountAmount:     o.DiscountAmount.String(),
		DeliveryFee:        o.DeliveryFee.String(),
		TotalPrice:         o.TotalPrice.String(),
		EstimatedReadyTime: o.EstimatedReadyTime,
		AddressExtra:       o.AddressExtra,
		OrderNote:          o.OrderNote,
		//OrderExtra: o.OrderExtra, // @TODO: Use JSON in GQL schema
	}
}

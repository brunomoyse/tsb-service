package mcp

import (
	"encoding/json"
	"strings"
	"time"

	couponDomain "tsb-service/internal/modules/coupon/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
)

// productOut is the JSON shape returned by product-related tools. We
// keep prices as strings (decimal) to match the GraphQL contract used
// by the dashboard — the LLM can format them for display.
type productOut struct {
	ID             string            `json:"id"`
	CategoryID     string            `json:"categoryId"`
	Code           *string           `json:"code"`
	Slug           *string           `json:"slug"`
	Price          string            `json:"price"`
	PieceCount     *int              `json:"pieceCount"`
	IsVisible      bool              `json:"isVisible"`
	IsAvailable    bool              `json:"isAvailable"`
	IsHalal        bool              `json:"isHalal"`
	IsVegetarian   bool              `json:"isVegetarian"`
	IsSpicy        bool              `json:"isSpicy"`
	IsLunchOnly    bool              `json:"isLunchOnly"`
	IsDiscountable bool              `json:"isDiscountable"`
	VatCategory    string            `json:"vatCategory"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	Translations   []translationOut  `json:"translations"`
}

type translationOut struct {
	Language    string  `json:"language"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

func toProductOut(p *productDomain.Product) productOut {
	out := productOut{
		ID:             p.ID.String(),
		CategoryID:     p.CategoryID.String(),
		Code:           p.Code,
		Slug:           p.Slug,
		Price:          p.Price.StringFixed(2),
		PieceCount:     p.PieceCount,
		IsVisible:      p.IsVisible,
		IsAvailable:    p.IsAvailable,
		IsHalal:        p.IsHalal,
		IsVegetarian:   p.IsVegetarian,
		IsSpicy:        p.IsSpicy,
		IsLunchOnly:    p.IsLunchOnly,
		IsDiscountable: p.IsDiscountable,
		VatCategory:    string(p.VatCategory),
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
		Translations:   make([]translationOut, len(p.Translations)),
	}
	for i, t := range p.Translations {
		out.Translations[i] = translationOut{
			Language:    t.Language,
			Name:        t.Name,
			Description: t.Description,
		}
	}
	return out
}

// categoryOut mirrors the lightweight subset of category data useful
// to the chatbot — full product expansion stays inside list_products.
type categoryOut struct {
	ID           string           `json:"id"`
	Order        int              `json:"order"`
	Translations []translationOut `json:"translations"`
}

func toCategoryOut(c *productDomain.Category) categoryOut {
	out := categoryOut{
		ID:           c.ID.String(),
		Order:        c.Order,
		Translations: make([]translationOut, len(c.Translations)),
	}
	for i, t := range c.Translations {
		out.Translations[i] = translationOut{
			Language:    t.Language,
			Name:        t.Name,
			Description: t.Description,
		}
	}
	return out
}

type choiceTranslationOut struct {
	Locale string `json:"locale"`
	Name   string `json:"name"`
}

type productChoiceGroupOut struct {
	ID            string                 `json:"id"`
	ProductID     string                 `json:"productId"`
	MinSelections int                    `json:"minSelections"`
	MaxSelections int                    `json:"maxSelections"`
	SortOrder     int                    `json:"sortOrder"`
	Translations  []choiceTranslationOut `json:"translations"`
}

func toChoiceGroupOut(g *productDomain.ProductChoiceGroup) productChoiceGroupOut {
	out := productChoiceGroupOut{
		ID:            g.ID.String(),
		ProductID:     g.ProductID.String(),
		MinSelections: g.MinSelections,
		MaxSelections: g.MaxSelections,
		SortOrder:     g.SortOrder,
		Translations:  make([]choiceTranslationOut, len(g.Translations)),
	}
	for i, t := range g.Translations {
		out.Translations[i] = choiceTranslationOut{Locale: t.Locale, Name: t.Name}
	}
	return out
}

type productChoiceOut struct {
	ID            string                 `json:"id"`
	ProductID     string                 `json:"productId"`
	ChoiceGroupID string                 `json:"choiceGroupId"`
	PriceModifier string                 `json:"priceModifier"`
	SortOrder     int                    `json:"sortOrder"`
	Translations  []choiceTranslationOut `json:"translations"`
}

func toChoiceOut(c *productDomain.ProductChoice) productChoiceOut {
	out := productChoiceOut{
		ID:            c.ID.String(),
		ProductID:     c.ProductID.String(),
		ChoiceGroupID: c.ChoiceGroupID.String(),
		PriceModifier: c.PriceModifier.StringFixed(2),
		SortOrder:     c.SortOrder,
		Translations:  make([]choiceTranslationOut, len(c.Translations)),
	}
	for i, t := range c.Translations {
		out.Translations[i] = choiceTranslationOut{Locale: t.Locale, Name: t.Name}
	}
	return out
}

// couponOut exposes the same fields the dashboard reads, with prices
// as strings and the computed Status() included so the LLM can
// describe state ("active", "expired", "exhausted") without re-running
// the logic.
type couponOut struct {
	ID             string     `json:"id"`
	Code           string     `json:"code"`
	DiscountType   string     `json:"discountType"`
	DiscountValue  string     `json:"discountValue"`
	MinOrderAmount *string    `json:"minOrderAmount,omitempty"`
	MaxUses        *int       `json:"maxUses,omitempty"`
	MaxUsesPerUser *int       `json:"maxUsesPerUser,omitempty"`
	UsedCount      int        `json:"usedCount"`
	IsActive       bool       `json:"isActive"`
	Status         string     `json:"status"`
	ValidFrom      *time.Time `json:"validFrom,omitempty"`
	ValidUntil     *time.Time `json:"validUntil,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func toCouponOut(c *couponDomain.Coupon) couponOut {
	out := couponOut{
		ID:             c.ID.String(),
		Code:           c.Code,
		DiscountType:   string(c.DiscountType),
		DiscountValue:  c.DiscountValue.StringFixed(2),
		MaxUses:        c.MaxUses,
		MaxUsesPerUser: c.MaxUsesPerUser,
		UsedCount:      c.UsedCount,
		IsActive:       c.IsActive,
		Status:         strings.ToLower(string(c.Status())),
		ValidFrom:      c.ValidFrom,
		ValidUntil:     c.ValidUntil,
		CreatedAt:      c.CreatedAt,
	}
	if c.MinOrderAmount != nil {
		s := c.MinOrderAmount.StringFixed(2)
		out.MinOrderAmount = &s
	}
	return out
}

// restaurantConfigOut is the snapshot of the global restaurant config.
// Hours come from the DB as raw JSON; we keep them as RawMessage so
// the LLM receives the canonical shape used by the dashboard.
type restaurantConfigOut struct {
	OrderingEnabled    bool            `json:"orderingEnabled"`
	OpeningHours       json.RawMessage `json:"openingHours"`
	OrderingHours      json.RawMessage `json:"orderingHours,omitempty"`
	PreparationMinutes int             `json:"preparationMinutes"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

func toRestaurantConfigOut(c *restaurantDomain.RestaurantConfig) restaurantConfigOut {
	return restaurantConfigOut{
		OrderingEnabled:    c.OrderingEnabled,
		OpeningHours:       c.OpeningHours,
		OrderingHours:      c.OrderingHours,
		PreparationMinutes: c.PreparationMinutes,
		UpdatedAt:          c.UpdatedAt,
	}
}

type scheduleOverrideOut struct {
	Date      string          `json:"date"` // YYYY-MM-DD
	Closed    bool            `json:"closed"`
	Schedule  json.RawMessage `json:"schedule,omitempty"`
	Note      *string         `json:"note,omitempty"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func toScheduleOverrideOut(ov *restaurantDomain.ScheduleOverride) scheduleOverrideOut {
	return scheduleOverrideOut{
		Date:      ov.DateKey(),
		Closed:    ov.Closed,
		Schedule:  ov.Schedule,
		Note:      ov.Note,
		UpdatedAt: ov.UpdatedAt,
	}
}

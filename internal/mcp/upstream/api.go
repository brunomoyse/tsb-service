package upstream

import (
	"context"
	"time"
)

// --- Inputs ------------------------------------------------------------------

// UpdateProductInput mirrors tsb-service's UpdateProductInput. Nil fields are
// left unchanged upstream. Note the upstream spelling `categoryID`.
type UpdateProductInput struct {
	CategoryID       *string       `json:"categoryID,omitempty"`
	Code             *string       `json:"code,omitempty"`
	RemoveBackground *bool         `json:"removeBackground,omitempty"`
	IsAvailable      *bool         `json:"isAvailable,omitempty"`
	IsDiscountable   *bool         `json:"isDiscountable,omitempty"`
	IsHalal          *bool         `json:"isHalal,omitempty"`
	IsLunchOnly      *bool         `json:"isLunchOnly,omitempty"`
	IsSpicy          *bool         `json:"isSpicy,omitempty"`
	IsVegetarian     *bool         `json:"isVegetarian,omitempty"`
	IsVisible        *bool         `json:"isVisible,omitempty"`
	PieceCount       *int          `json:"pieceCount,omitempty"`
	Price            *string       `json:"price,omitempty"`
	VatCategory      *string       `json:"vatCategory,omitempty"`
	Translations     []Translation `json:"translations,omitempty"`
}

// CreateProductInput mirrors tsb-service's CreateProductInput (without image).
type CreateProductInput struct {
	CategoryID     string        `json:"categoryId"`
	Code           *string       `json:"code,omitempty"`
	IsAvailable    bool          `json:"isAvailable"`
	IsDiscountable bool          `json:"isDiscountable"`
	IsHalal        bool          `json:"isHalal"`
	IsLunchOnly    bool          `json:"isLunchOnly"`
	IsSpicy        bool          `json:"isSpicy"`
	IsVegetarian   bool          `json:"isVegetarian"`
	IsVisible      bool          `json:"isVisible"`
	PieceCount     *int          `json:"pieceCount,omitempty"`
	Price          string        `json:"price"`
	VatCategory    string        `json:"vatCategory"`
	Translations   []Translation `json:"translations"`
}

// ChoiceGroupInput covers create (ProductID set) and update.
type ChoiceGroupInput struct {
	ProductID     string              `json:"productId,omitempty"`
	MinSelections *int                `json:"minSelections,omitempty"`
	MaxSelections *int                `json:"maxSelections,omitempty"`
	SortOrder     *int                `json:"sortOrder,omitempty"`
	Translations  []ChoiceTranslation `json:"translations,omitempty"`
}

// ChoiceInput covers create (ProductID/ChoiceGroupID set) and update.
type ChoiceInput struct {
	ProductID     string              `json:"productId,omitempty"`
	ChoiceGroupID string              `json:"choiceGroupId,omitempty"`
	PriceModifier *string             `json:"priceModifier,omitempty"`
	SortOrder     *int                `json:"sortOrder,omitempty"`
	Translations  []ChoiceTranslation `json:"translations,omitempty"`
}

// ScheduleOverrideInput mirrors tsb-service's ScheduleOverrideInput.
type ScheduleOverrideInput struct {
	Date     time.Time    `json:"date"`
	Closed   bool         `json:"closed"`
	Schedule *DaySchedule `json:"schedule,omitempty"`
	Note     *string      `json:"note,omitempty"`
}

// CouponInput covers create and update. Nil fields are omitted.
type CouponInput struct {
	Code           *string    `json:"code,omitempty"`
	DiscountType   *string    `json:"discountType,omitempty"`
	DiscountValue  *string    `json:"discountValue,omitempty"`
	MinOrderAmount *string    `json:"minOrderAmount,omitempty"`
	MaxUses        *int       `json:"maxUses,omitempty"`
	MaxUsesPerUser *int       `json:"maxUsesPerUser,omitempty"`
	IsActive       *bool      `json:"isActive,omitempty"`
	ValidFrom      *time.Time `json:"validFrom,omitempty"`
	ValidUntil     *time.Time `json:"validUntil,omitempty"`
}

// OverrideDate encodes a calendar date for the override DateTime arguments.
// tsb-service writes the UTC date part into a DATE column, so midnight UTC of
// the intended date is unambiguous whatever the database session timezone.
func OverrideDate(date string) (time.Time, error) {
	return time.Parse(time.DateOnly, date)
}

// --- Reads -------------------------------------------------------------------

func (c *Client) Products(ctx context.Context) ([]Product, error) {
	var out struct {
		Products []Product `json:"products"`
	}
	err := c.Do(ctx, "McpProducts", nil, &out)
	return out.Products, err
}

func (c *Client) Product(ctx context.Context, id string) (*Product, error) {
	var out struct {
		Product *Product `json:"product"`
	}
	if err := c.Do(ctx, "McpProduct", map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Product == nil {
		return nil, &Error{Kind: KindNotFound, Message: "This product does not exist."}
	}
	return out.Product, nil
}

func (c *Client) Categories(ctx context.Context) ([]Category, error) {
	var out struct {
		Categories []Category `json:"productCategories"`
	}
	err := c.Do(ctx, "McpCategories", nil, &out)
	return out.Categories, err
}

func (c *Client) RestaurantConfig(ctx context.Context) (*RestaurantConfig, error) {
	var out struct {
		Config RestaurantConfig `json:"restaurantConfig"`
	}
	if err := c.Do(ctx, "McpRestaurantConfig", nil, &out); err != nil {
		return nil, err
	}
	return &out.Config, nil
}

func (c *Client) ScheduleOverrides(ctx context.Context, from, to time.Time) ([]ScheduleOverride, error) {
	var out struct {
		Overrides []ScheduleOverride `json:"scheduleOverrides"`
	}
	err := c.Do(ctx, "McpScheduleOverrides", map[string]any{"from": from, "to": to}, &out)
	return out.Overrides, err
}

func (c *Client) Coupons(ctx context.Context) ([]Coupon, error) {
	var out struct {
		Coupons []Coupon `json:"coupons"`
	}
	err := c.Do(ctx, "McpCoupons", nil, &out)
	return out.Coupons, err
}

func (c *Client) Coupon(ctx context.Context, id string) (*Coupon, error) {
	var out struct {
		Coupon *Coupon `json:"coupon"`
	}
	if err := c.Do(ctx, "McpCoupon", map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Coupon == nil {
		return nil, &Error{Kind: KindNotFound, Message: "This coupon does not exist."}
	}
	return out.Coupon, nil
}

func (c *Client) Orders(ctx context.Context) ([]Order, error) {
	var out struct {
		Orders []Order `json:"orders"`
	}
	err := c.Do(ctx, "McpOrders", nil, &out)
	return out.Orders, err
}

func (c *Client) Order(ctx context.Context, id string) (*Order, error) {
	var out struct {
		Order *Order `json:"order"`
	}
	if err := c.Do(ctx, "McpOrder", map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Order == nil {
		return nil, &Error{Kind: KindNotFound, Message: "This order does not exist."}
	}
	return out.Order, nil
}

func (c *Client) OrderHistory(ctx context.Context, in OrderHistoryInput) (*OrderHistory, error) {
	var out struct {
		History OrderHistory `json:"orderHistory"`
	}
	if err := c.Do(ctx, "McpOrderHistory", map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	return &out.History, nil
}

func (c *Client) CustomerStats(ctx context.Context, in CustomerStatsInput) (*CustomerStats, error) {
	var out struct {
		Stats CustomerStats `json:"customerStats"`
	}
	if err := c.Do(ctx, "McpCustomerStats", map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Stats, nil
}

// --- Writes ------------------------------------------------------------------

func (c *Client) UpdateProduct(ctx context.Context, id string, in UpdateProductInput) (*Product, error) {
	var out struct {
		Product Product `json:"updateProduct"`
	}
	if err := c.Do(ctx, "McpUpdateProduct", map[string]any{"id": id, "input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Product, nil
}

// UpdateProductImage uploads a new image for a product (multipart).
func (c *Client) UpdateProductImage(ctx context.Context, id string, removeBackground bool, filename, contentType string, data []byte) (*Product, error) {
	var out struct {
		Product Product `json:"updateProduct"`
	}
	vars := map[string]any{"id": id, "input": map[string]any{"image": nil, "removeBackground": removeBackground}}
	if err := c.Upload(ctx, "McpUpdateProduct", vars, "variables.input.image", filename, contentType, data, &out); err != nil {
		return nil, err
	}
	return &out.Product, nil
}

func (c *Client) CreateProduct(ctx context.Context, in CreateProductInput) (*Product, error) {
	var out struct {
		Product Product `json:"createProduct"`
	}
	if err := c.Do(ctx, "McpCreateProduct", map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Product, nil
}

func (c *Client) CreateChoiceGroup(ctx context.Context, in ChoiceGroupInput) (*ChoiceGroup, error) {
	var out struct {
		Group ChoiceGroup `json:"createProductChoiceGroup"`
	}
	if err := c.Do(ctx, "McpCreateChoiceGroup", map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Group, nil
}

func (c *Client) UpdateChoiceGroup(ctx context.Context, id string, in ChoiceGroupInput) (*ChoiceGroup, error) {
	in.ProductID = ""
	var out struct {
		Group ChoiceGroup `json:"updateProductChoiceGroup"`
	}
	if err := c.Do(ctx, "McpUpdateChoiceGroup", map[string]any{"id": id, "input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Group, nil
}

func (c *Client) DeleteChoiceGroup(ctx context.Context, id string) error {
	return c.Do(ctx, "McpDeleteChoiceGroup", map[string]any{"id": id}, nil)
}

func (c *Client) CreateChoice(ctx context.Context, in ChoiceInput) (*Choice, error) {
	var out struct {
		Choice Choice `json:"createProductChoice"`
	}
	if err := c.Do(ctx, "McpCreateChoice", map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Choice, nil
}

func (c *Client) UpdateChoice(ctx context.Context, id string, in ChoiceInput) (*Choice, error) {
	in.ProductID, in.ChoiceGroupID = "", ""
	var out struct {
		Choice Choice `json:"updateProductChoice"`
	}
	if err := c.Do(ctx, "McpUpdateChoice", map[string]any{"id": id, "input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Choice, nil
}

func (c *Client) DeleteChoice(ctx context.Context, id string) error {
	return c.Do(ctx, "McpDeleteChoice", map[string]any{"id": id}, nil)
}

func (c *Client) UpdateOrderingEnabled(ctx context.Context, enabled bool) error {
	return c.Do(ctx, "McpUpdateOrderingEnabled", map[string]any{"enabled": enabled}, nil)
}

func (c *Client) UpdateOpeningHours(ctx context.Context, w Week) error {
	return c.Do(ctx, "McpUpdateOpeningHours", map[string]any{"hours": w}, nil)
}

func (c *Client) UpdateOrderingHours(ctx context.Context, w Week) error {
	return c.Do(ctx, "McpUpdateOrderingHours", map[string]any{"hours": w}, nil)
}

func (c *Client) UpdatePreparationMinutes(ctx context.Context, minutes int) error {
	return c.Do(ctx, "McpUpdatePreparationMinutes", map[string]any{"minutes": minutes}, nil)
}

func (c *Client) UpsertScheduleOverride(ctx context.Context, in ScheduleOverrideInput) error {
	return c.Do(ctx, "McpUpsertScheduleOverride", map[string]any{"input": in}, nil)
}

func (c *Client) DeleteScheduleOverride(ctx context.Context, date time.Time) error {
	return c.Do(ctx, "McpDeleteScheduleOverride", map[string]any{"date": date}, nil)
}

func (c *Client) CreateCoupon(ctx context.Context, in CouponInput) (*Coupon, error) {
	var out struct {
		Coupon Coupon `json:"createCoupon"`
	}
	if err := c.Do(ctx, "McpCreateCoupon", map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Coupon, nil
}

func (c *Client) UpdateCoupon(ctx context.Context, id string, in CouponInput) (*Coupon, error) {
	var out struct {
		Coupon Coupon `json:"updateCoupon"`
	}
	if err := c.Do(ctx, "McpUpdateCoupon", map[string]any{"id": id, "input": in}, &out); err != nil {
		return nil, err
	}
	return &out.Coupon, nil
}

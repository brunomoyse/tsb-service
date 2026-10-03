package upstream

import (
	"encoding/json"
	"strings"
	"time"
)

// Translation is a product/category translation.
type Translation struct {
	Language    string  `json:"language"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// ChoiceTranslation is a choice/choice-group translation (note: `locale`).
type ChoiceTranslation struct {
	Locale string `json:"locale"`
	Name   string `json:"name"`
}

// Choice is one option inside a choice group.
type Choice struct {
	ID            string              `json:"id"`
	ProductID     string              `json:"productId,omitempty"`
	ChoiceGroupID string              `json:"choiceGroupId"`
	PriceModifier string              `json:"priceModifier"`
	SortOrder     int                 `json:"sortOrder"`
	Name          string              `json:"name"`
	Translations  []ChoiceTranslation `json:"translations"`
}

// ChoiceGroup is a set of choices with min/max selections.
type ChoiceGroup struct {
	ID            string              `json:"id"`
	ProductID     string              `json:"productId,omitempty"`
	MinSelections int                 `json:"minSelections"`
	MaxSelections int                 `json:"maxSelections"`
	SortOrder     int                 `json:"sortOrder"`
	Name          string              `json:"name"`
	Translations  []ChoiceTranslation `json:"translations"`
	Choices       []Choice            `json:"choices"`
}

// CategoryRef is the category embedded in a product.
type CategoryRef struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Translations []Translation `json:"translations"`
}

// Category is a product category.
type Category struct {
	ID           string        `json:"id"`
	Order        int           `json:"order"`
	Slug         string        `json:"slug"`
	Name         string        `json:"name"`
	Translations []Translation `json:"translations"`
}

// Product is a catalogue product.
type Product struct {
	ID             string        `json:"id"`
	Code           *string       `json:"code"`
	Slug           string        `json:"slug"`
	Name           string        `json:"name"`
	Description    *string       `json:"description"`
	Price          string        `json:"price"`
	VatCategory    string        `json:"vatCategory"`
	PieceCount     *int          `json:"pieceCount"`
	IsAvailable    bool          `json:"isAvailable"`
	IsVisible      bool          `json:"isVisible"`
	IsDiscountable bool          `json:"isDiscountable"`
	IsHalal        bool          `json:"isHalal"`
	IsLunchOnly    bool          `json:"isLunchOnly"`
	IsSpicy        bool          `json:"isSpicy"`
	IsVegetarian   bool          `json:"isVegetarian"`
	Category       CategoryRef   `json:"category"`
	Translations   []Translation `json:"translations"`
	ChoiceGroups   []ChoiceGroup `json:"choiceGroups"`
}

// NameIn returns the translated name for lang, falling back to Name.
func (p *Product) NameIn(lang string) string {
	for _, t := range p.Translations {
		if t.Language == lang && t.Name != "" {
			return t.Name
		}
	}
	return p.Name
}

// DaySchedule is one day of opening hours ("HH:MM").
type DaySchedule struct {
	Open        string `json:"open"`
	Close       string `json:"close"`
	DinnerOpen  string `json:"dinnerOpen,omitempty"`
	DinnerClose string `json:"dinnerClose,omitempty"`
}

// Week maps lower-case day names to schedules; a missing day is closed.
type Week map[string]*DaySchedule

// RestaurantConfig is the subset of restaurantConfig the MCP uses.
type RestaurantConfig struct {
	OrderingEnabled         bool            `json:"orderingEnabled"`
	OpeningHours            json.RawMessage `json:"openingHours"`
	OrderingHours           json.RawMessage `json:"orderingHours"`
	PreparationMinutes      int             `json:"preparationMinutes"`
	IsCurrentlyOpen         bool            `json:"isCurrentlyOpen"`
	IsOrderingCurrentlyOpen bool            `json:"isOrderingCurrentlyOpen"`
	NextOpeningAt           *time.Time      `json:"nextOpeningAt"`
	UpdatedAt               time.Time       `json:"updatedAt"`
}

// ParseWeek decodes an openingHours/orderingHours JSON value. A null value
// yields a nil Week.
func ParseWeek(raw json.RawMessage) (Week, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var w Week
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	for k, v := range w {
		if v == nil || v.Open == "" {
			delete(w, k)
		}
	}
	return w, nil
}

// ScheduleOverride is a date-specific deviation from the weekly hours.
type ScheduleOverride struct {
	Date      time.Time    `json:"date"`
	Closed    bool         `json:"closed"`
	Schedule  *DaySchedule `json:"schedule"`
	Note      *string      `json:"note"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// DateKey is the calendar date of the override (stored as a DATE upstream).
func (o *ScheduleOverride) DateKey() string { return o.Date.UTC().Format(time.DateOnly) }

// Coupon is a discount code.
type Coupon struct {
	ID             string     `json:"id"`
	Code           string     `json:"code"`
	DiscountType   string     `json:"discountType"`
	DiscountValue  string     `json:"discountValue"`
	MinOrderAmount *string    `json:"minOrderAmount"`
	MaxUses        *int       `json:"maxUses"`
	MaxUsesPerUser *int       `json:"maxUsesPerUser"`
	UsedCount      int        `json:"usedCount"`
	IsActive       bool       `json:"isActive"`
	Status         string     `json:"status"`
	ValidFrom      *time.Time `json:"validFrom"`
	ValidUntil     *time.Time `json:"validUntil"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// OrderItem is one line of an order.
type OrderItem struct {
	Quantity   int    `json:"quantity"`
	UnitPrice  string `json:"unitPrice"`
	TotalPrice string `json:"totalPrice"`
	Product    *struct {
		ID           string        `json:"id"`
		Name         string        `json:"name"`
		Category     CategoryRef   `json:"category"`
		Translations []Translation `json:"translations"`
	} `json:"product"`
	Choice *struct {
		Name string `json:"name"`
	} `json:"choice"`
	Selections []struct {
		Quantity int `json:"quantity"`
		Choice   struct {
			Name string `json:"name"`
		} `json:"choice"`
	} `json:"selections"`
}

// Order is an order as listed or detailed.
type Order struct {
	ID                 string     `json:"id"`
	CreatedAt          time.Time  `json:"createdAt"`
	Status             string     `json:"status"`
	Type               string     `json:"type"`
	IsOnlinePayment    bool       `json:"isOnlinePayment"`
	TotalPrice         string     `json:"totalPrice"`
	DiscountAmount     string     `json:"discountAmount"`
	DeliveryFee        *string    `json:"deliveryFee"`
	CouponCode         *string    `json:"couponCode"`
	PreferredReadyTime *time.Time `json:"preferredReadyTime"`
	EstimatedReadyTime *time.Time `json:"estimatedReadyTime"`
	DisplayAddress     string     `json:"displayAddress"`
	AddressExtra       *string    `json:"addressExtra"`
	OrderNote          *string    `json:"orderNote"`
	CancellationReason *string    `json:"cancellationReason"`
	Payment            *struct {
		Status string `json:"status"`
	} `json:"payment"`
	// Customer holds the first name and the last name's initial: the full
	// last name is dropped while decoding, and phone numbers and emails are
	// never requested (see internal/mcp/privacy).
	Customer *struct {
		FirstName   string  `json:"firstName"`
		LastInitial Initial `json:"lastName"`
	} `json:"customer"`
	Items         []OrderItem `json:"items"`
	StatusHistory []struct {
		Status    string    `json:"status"`
		ChangedAt time.Time `json:"changedAt"`
	} `json:"statusHistory"`
}

// OrderHistory is the orderHistory response.
type OrderHistory struct {
	Summary struct {
		TotalOrders  int    `json:"totalOrders"`
		TotalRevenue string `json:"totalRevenue"`
		AverageOrder string `json:"averageOrder"`
	} `json:"summary"`
	Orders []Order `json:"orders"`
}

// OrderHistoryInput filters orderHistory.
type OrderHistoryInput struct {
	StartDate *time.Time `json:"startDate,omitempty"`
	EndDate   *time.Time `json:"endDate,omitempty"`
	Status    *string    `json:"status,omitempty"`
	OrderType *string    `json:"orderType,omitempty"`
	Search    *string    `json:"search,omitempty"`
	First     int        `json:"first,omitempty"`
	Page      int        `json:"page,omitempty"`
}

// CustomerStats is the customerStats response (first names and last-name
// initials only; phone numbers and emails are never requested).
type CustomerStats struct {
	Summary struct {
		TotalCustomers    int    `json:"totalCustomers"`
		TotalRevenue      string `json:"totalRevenue"`
		AverageOrderValue string `json:"averageOrderValue"`
		TotalOrders       int    `json:"totalOrders"`
	} `json:"summary"`
	Customers []struct {
		UserID             string    `json:"userId"`
		FirstName          string    `json:"firstName"`
		LastInitial        Initial   `json:"lastName"`
		RegisteredAt       time.Time `json:"registeredAt"`
		TotalOrders        int       `json:"totalOrders"`
		TotalAmount        string    `json:"totalAmount"`
		AverageOrderAmount string    `json:"averageOrderAmount"`
		FirstOrderDate     time.Time `json:"firstOrderDate"`
		LastOrderDate      time.Time `json:"lastOrderDate"`
		PreferredOrderType string    `json:"preferredOrderType"`
		DeliveryCount      int       `json:"deliveryCount"`
		PickupCount        int       `json:"pickupCount"`
	} `json:"customers"`
}

// CustomerStatsInput filters customerStats.
type CustomerStatsInput struct {
	StartDate *time.Time `json:"startDate,omitempty"`
	EndDate   *time.Time `json:"endDate,omitempty"`
	OrderType *string    `json:"orderType,omitempty"`
	MinOrders *int       `json:"minOrders,omitempty"`
}

// Initial is the first letter of a customer's last name, upper case. It is
// decoded from the full last name, which is never kept: the assistant must
// not see customers' last names.
type Initial string

// UnmarshalJSON keeps the first letter only.
func (i *Initial) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*i = ""
	if s == nil {
		return nil
	}
	for _, r := range strings.TrimSpace(*s) {
		*i = Initial(strings.ToUpper(string(r)))
		break
	}
	return nil
}

// Name is the customer's first name followed by the initial ("Marie D."), or
// the first name alone.
func Name(first string, last Initial) string {
	first = strings.TrimSpace(first)
	if last == "" {
		return first
	}
	if first == "" {
		return string(last) + "."
	}
	return first + " " + string(last) + "."
}

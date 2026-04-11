package domain

// VatCategory classifies a product for Belgian VAT purposes.
// The actual VAT rate and SCE 2.0 code (A/B/C/D/X) depend on both
// this category and the order's service type — resolved by
// VatRatePercent / SceCode below.
type VatCategory string

const (
	// VatCategoryFood applies to restaurant food items.
	// Dine-in → 12% (SCE B), takeaway/delivery → 6% (SCE C).
	VatCategoryFood VatCategory = "food"
	// VatCategoryBeverage applies to drinks (alcoholic and soft).
	// 21% in all service types (SCE A).
	VatCategoryBeverage VatCategory = "beverage"
	// VatCategoryZeroRated applies to items legally exempt (0%, SCE D).
	VatCategoryZeroRated VatCategory = "zero_rated"
	// VatCategoryOutOfScope applies to items not subject to SCE 2.0 (SCE X).
	VatCategoryOutOfScope VatCategory = "out_of_scope"
)

// ServiceType mirrors the HubRise order service type. The POS uses
// it to resolve VAT codes at checkout.
type ServiceType string

const (
	ServiceTypeDineIn   ServiceType = "dine_in"
	ServiceTypeTakeaway ServiceType = "takeaway"
	ServiceTypeDelivery ServiceType = "delivery"
)

// IsValid returns true if the VatCategory is one of the four known
// values.
func (c VatCategory) IsValid() bool {
	switch c {
	case VatCategoryFood, VatCategoryBeverage, VatCategoryZeroRated, VatCategoryOutOfScope:
		return true
	default:
		return false
	}
}

// VatRatePercent returns the Belgian VAT rate as a percentage for
// the given VatCategory and service type.
//
// Reference: SPF Finances VAT rate schedule (2024):
//   - Restaurant food dine-in: 12%
//   - Restaurant food takeaway/delivery: 6%
//   - Beverages (alcohol + soft drinks): 21%
//   - Zero-rated items: 0%
//   - Out-of-scope items: 0 (informational — not billable via SCE)
func (c VatCategory) VatRatePercent(svc ServiceType) float64 {
	switch c {
	case VatCategoryBeverage:
		return 21.0
	case VatCategoryFood:
		if svc == ServiceTypeDineIn {
			return 12.0
		}
		return 6.0
	case VatCategoryZeroRated:
		return 0.0
	case VatCategoryOutOfScope:
		return 0.0
	default:
		return 0.0
	}
}

// SceCode returns the Belgian SCE 2.0 VAT code letter used by the
// POS fiscal module at checkout time.
func (c VatCategory) SceCode(svc ServiceType) string {
	switch c {
	case VatCategoryBeverage:
		return "A"
	case VatCategoryFood:
		if svc == ServiceTypeDineIn {
			return "B"
		}
		return "C"
	case VatCategoryZeroRated:
		return "D"
	case VatCategoryOutOfScope:
		return "X"
	default:
		return "X"
	}
}

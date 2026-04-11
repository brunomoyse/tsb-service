package domain

import (
	"fmt"

	orderDomain "tsb-service/internal/modules/order/domain"
)

// HubriseCreateOrderRequest is the JSON body for POST /location/orders.
//
// We populate a minimal subset sufficient for the POS to display and
// the kitchen to prepare the order. Full customer + delivery details
// are added progressively as the webshop flow matures.
type HubriseCreateOrderRequest struct {
	Status        string                 `json:"status"`
	Ref           string                 `json:"ref,omitempty"`
	PrivateRef    string                 `json:"private_ref,omitempty"`
	ServiceType   string                 `json:"service_type,omitempty"`
	ExpectedTime  string                 `json:"expected_time,omitempty"`
	Asap          bool                   `json:"asap"`
	CustomerNotes string                 `json:"customer_notes,omitempty"`
	Items         []HubriseOrderItem     `json:"items"`
	Payments      []HubriseOrderPayment  `json:"payments,omitempty"`
	Charges       []HubriseOrderCharge   `json:"charges,omitempty"`
	Customer      *HubriseOrderCustomer  `json:"customer,omitempty"`
	CouponCodes   []string               `json:"coupon_codes,omitempty"`
}

type HubriseOrderItem struct {
	ProductName string `json:"product_name"`
	SkuName     string `json:"sku_name,omitempty"`
	SkuRef      string `json:"sku_ref,omitempty"`
	Price       string `json:"price"`
	Quantity    string `json:"quantity"`
}

type HubriseOrderPayment struct {
	Name   string `json:"name,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Amount string `json:"amount"`
}

type HubriseOrderCharge struct {
	Name  string `json:"name"`
	Ref   string `json:"ref,omitempty"`
	Price string `json:"price"`
}

type HubriseOrderCustomer struct {
	Email     string `json:"email,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Address1  string `json:"address_1,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	City      string `json:"city,omitempty"`
	Country   string `json:"country,omitempty"`
}

// MapOrderToHubrise turns an internal order into a HubRise create-order
// request. The caller is responsible for mapping each order_product
// row to a HubriseOrderItem using a name+price pair it already has.
// This is a thin skeleton — refine field mapping as the order module
// matures.
func MapOrderToHubrise(o *orderDomain.Order, items []HubriseOrderItem, serviceType string) *HubriseCreateOrderRequest {
	req := &HubriseCreateOrderRequest{
		Status:      "new",
		PrivateRef:  o.ID.String(),
		ServiceType: serviceType,
		Asap:        true,
		Items:       items,
		Payments: []HubriseOrderPayment{
			{
				Name:   "Mollie",
				Ref:    "mollie",
				Amount: fmt.Sprintf("%s EUR", o.TotalPrice.String()),
			},
		},
	}
	if o.DeliveryFee != nil {
		req.Charges = append(req.Charges, HubriseOrderCharge{
			Name:  "Delivery",
			Ref:   "delivery",
			Price: fmt.Sprintf("%s EUR", o.DeliveryFee.String()),
		})
	}
	return req
}

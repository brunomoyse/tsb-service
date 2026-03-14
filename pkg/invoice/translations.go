package invoice

type labels struct {
	FilePrefix       string
	InvoiceTitle     string
	Date             string
	OrderRef         string
	Customer         string
	OrderType        string
	TypeDelivery     string
	TypePickup       string
	TypeDineIn       string
	DeliveryAddress  string
	Product          string
	Qty              string
	UnitPrice        string
	Total            string
	Subtotal         string
	TakeawayDiscount string
	CouponDiscount   string
	DeliveryFee      string
	ThankYou         string
	CompanyNumber    string
	Phone            string
	Email            string
	VATIncluded      string
}

var translations = map[string]labels{
	"fr": {
		FilePrefix:       "facture",
		InvoiceTitle:     "Facture",
		Date:             "Date",
		OrderRef:         "Réf. commande",
		Customer:         "Client",
		OrderType:        "Type de commande",
		TypeDelivery:     "Livraison",
		TypePickup:       "À emporter",
		TypeDineIn:       "Sur place",
		DeliveryAddress:  "Adresse de livraison",
		Product:          "Produit",
		Qty:              "Qté",
		UnitPrice:        "Prix unit.",
		Total:            "Total",
		Subtotal:         "Sous-total",
		TakeawayDiscount: "Remise emporter (-10%)",
		CouponDiscount:   "Coupon",
		DeliveryFee:      "Frais de livraison",
		ThankYou:         "Merci pour votre commande !",
		CompanyNumber:    "N° d'entreprise",
		Phone:            "Tél",
		Email:            "Email",
		VATIncluded:      "TVA comprise",
	},
	"en": {
		FilePrefix:       "invoice",
		InvoiceTitle:     "Invoice",
		Date:             "Date",
		OrderRef:         "Order ref.",
		Customer:         "Customer",
		OrderType:        "Order type",
		TypeDelivery:     "Delivery",
		TypePickup:       "Pickup",
		TypeDineIn:       "Dine-in",
		DeliveryAddress:  "Delivery address",
		Product:          "Product",
		Qty:              "Qty",
		UnitPrice:        "Unit price",
		Total:            "Total",
		Subtotal:         "Subtotal",
		TakeawayDiscount: "Takeaway discount (-10%)",
		CouponDiscount:   "Coupon",
		DeliveryFee:      "Delivery fee",
		ThankYou:         "Thank you for your order!",
		CompanyNumber:    "Company no.",
		Phone:            "Phone",
		Email:            "Email",
		VATIncluded:      "VAT included",
	},
}

func getLabels(lang string) labels {
	if l, ok := translations[lang]; ok {
		return l
	}
	return translations["fr"]
}

// FilePrefix returns the localized invoice file prefix (e.g. "facture", "invoice").
func FilePrefix(lang string) string {
	return getLabels(lang).FilePrefix
}

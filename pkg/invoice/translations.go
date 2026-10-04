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
	TotalVAT         string
	ThankYou         string
	CompanyNumber    string
	Phone            string
	Email            string
	VATIncluded      string
}

// fr is the only label set: invoices are always issued in French, whatever language the customer
// ordered in (owner decision; also the PDF font has no CJK glyphs, so zh could not be printed).
// The discount lines carry no rate: the pickup discount rate is a backend policy value.
var fr = labels{
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
	TakeawayDiscount: "Remise à emporter",
	CouponDiscount:   "Code promo",
	DeliveryFee:      "Frais de livraison",
	TotalVAT:         "Total TVA",
	ThankYou:         "Merci pour votre commande !",
	CompanyNumber:    "N° d'entreprise",
	Phone:            "Tél",
	Email:            "E-mail",
	VATIncluded:      "TVA comprise",
}

// FilePrefix returns the invoice file name prefix, always "facture".
func FilePrefix() string {
	return fr.FilePrefix
}

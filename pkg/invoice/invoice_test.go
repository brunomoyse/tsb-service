package invoice

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tsb-service/pkg/brand"
)

func ptr[T any](v T) *T { return &v }

// baseInvoice is a delivery order with a coupon, a delivery fee and two VAT
// rates, i.e. every optional block that can appear on an invoice.
func baseInvoice() InvoiceData {
	return InvoiceData{
		CustomerName:  "Jeanne Dupont",
		CustomerEmail: "jeanne@example.com",
		CustomerPhone: ptr("+32 470 12 34 56"),
		OrderID:       "7b0f3c1e-5d2a-4c8b-9e41-0a1b2c3d4e5f",
		// 17:30 UTC on 1 July is 19:30 in Brussels (CEST).
		OrderDate: time.Date(2026, 7, 1, 17, 30, 0, 0, time.UTC),
		OrderType: "DELIVERY",
		Language:  "fr",
		Items: []InvoiceItem{
			{Name: "Sushi au Saumon", Code: "S01", Quantity: 2, UnitPrice: "12.50", LineTotal: "25.00"},
			{Name: "Thé vert", Quantity: 1, UnitPrice: "3.00", LineTotal: "3.00"},
		},
		Subtotal:       "28.00",
		VatBreakdown:   []InvoiceVatLine{{Label: "TVA", Rate: "6", Amount: "1.58"}, {Label: "TVA", Rate: "21", Amount: "0.00"}},
		TotalVatAmount: ptr("1.58"),
		CouponDiscount: ptr("2.80"),
		CouponCode:     ptr("WELCOME10"),
		DeliveryFee:    ptr("2.50"),
		Total:          "27.70",
		Address: &InvoiceAddress{
			StreetName: "Rue Saint-Gilles", HouseNumber: "12", BoxNumber: ptr("3B"),
			MunicipalityName: "Liège", Postcode: "4000",
		},
	}
}

func generate(t *testing.T, d InvoiceData) []string {
	t.Helper()
	pdf, err := GeneratePDF(d)
	require.NoError(t, err)
	return pdfTextLines(t, pdf)
}

// setBrand points the invoice at a different restaurant identity for one test
// and restores the package-level brand config afterwards.
func setBrand(t *testing.T, env map[string]string) {
	t.Helper()
	// Registered before t.Setenv so it runs after the environment is restored.
	t.Cleanup(func() { brand.Load() })
	for k, v := range env {
		t.Setenv(k, v)
	}
	brand.Load()
}

func TestGeneratePDFFullInvoiceFrench(t *testing.T) {
	lines := generate(t, baseInvoice())

	// Exact reading order of the whole document: header, order info, customer,
	// items, totals, footer. Amounts are printed exactly as supplied, with " €".
	require.Equal(t, []string{
		"Tokyo Sushi Bar — SRL",
		"Facture",
		"Rue de la Cathédrale 59, 4000 Liège, Belgique",
		"Tél: +32 4 222 98 88  |  Email: tokyosushibar888@gmail.com",
		"N° d'entreprise: BE0772.499.585",
		"Réf. commande: TSB-2026-2C3D4E5F",
		"Date: 01/07/2026 19:30",
		"Type de commande: Livraison",
		"Adresse de livraison: Rue Saint-Gilles 12 / 3B, 4000 Liège",
		"Client",
		"Jeanne Dupont",
		"jeanne@example.com",
		"+32 470 12 34 56",
		"Produit", "Qté", "Prix unit.", "Total",
		"S01 — Sushi au Saumon", "2", "12.50 €", "25.00 €",
		"Thé vert", "1", "3.00 €", "3.00 €",
		"Sous-total", "28.00 €",
		"TVA (6%)", "1.58 €",
		"TVA (21%)", "0.00 €",
		"Total TVA", "1.58 €",
		"Coupon (WELCOME10)", "- 2.80 €",
		"Frais de livraison", "2.50 €",
		"Total", "27.70 €",
		"TVA comprise",
		"Merci pour votre commande !",
	}, lines)
}

func TestGeneratePDFEnglishLabelsAndDateFormat(t *testing.T) {
	d := baseInvoice()
	d.Language = "en"
	d.OrderType = "PICKUP"
	d.Address = nil
	d.CouponCode = nil
	d.DeliveryFee = nil
	lines := generate(t, d)

	require.Contains(t, lines, "Invoice")
	require.Contains(t, lines, "Phone: +32 4 222 98 88  |  Email: tokyosushibar888@gmail.com")
	require.Contains(t, lines, "Company no.: BE0772.499.585")
	require.Contains(t, lines, "Order ref.: TSB-2026-2C3D4E5F")
	// English uses MM/DD/YYYY and a 12-hour clock, in the restaurant timezone.
	require.Contains(t, lines, "Date: 07/01/2026 7:30 PM")
	require.Contains(t, lines, "Order type: Pickup")
	require.Contains(t, lines, "Customer")
	require.Contains(t, lines, "Product")
	require.Contains(t, lines, "Unit price")
	require.Contains(t, lines, "Subtotal")
	require.Contains(t, lines, "Total VAT")
	require.Contains(t, lines, "Coupon")
	require.Contains(t, lines, "VAT included")
	require.Contains(t, lines, "Thank you for your order!")
	require.NotContains(t, lines, "Delivery fee")
	require.NotContains(t, lines, "Delivery address: Rue Saint-Gilles 12 / 3B, 4000 Liège")
}

func TestGeneratePDFUnsupportedLanguageFallsBackToFrench(t *testing.T) {
	for _, lang := range []string{"nl", "zh", "", "xx"} {
		d := baseInvoice()
		d.Language = lang
		lines := generate(t, d)
		require.Contains(t, lines, "Facture", lang)
		require.Contains(t, lines, "Date: 01/07/2026 19:30", lang+": non-English keeps the 24h dd/mm format")
		require.Contains(t, lines, "Merci pour votre commande !", lang)
	}
}

func TestGeneratePDFNormalisesLanguage(t *testing.T) {
	for _, lang := range []string{"EN", " en ", "en-GB", "en_US"} {
		d := baseInvoice()
		d.Language = lang
		lines := generate(t, d)
		require.Contains(t, lines, "Invoice", lang)
		require.Contains(t, lines, "Thank you for your order!", lang)
		require.Contains(t, lines, "Date: 07/01/2026 7:30 PM", lang+": English 12h format")
	}
	for _, lang := range []string{"FR", "fr-BE", "nl-BE", "zh-Hans", "de-DE", "garbage"} {
		d := baseInvoice()
		d.Language = lang
		lines := generate(t, d)
		require.Contains(t, lines, "Facture", lang)
		require.Contains(t, lines, "Date: 01/07/2026 19:30", lang)
	}
}

func TestGeneratePDFOrderTypeLabels(t *testing.T) {
	cases := map[string]string{
		"DELIVERY": "Type de commande: Livraison",
		"PICKUP":   "Type de commande: À emporter",
		"DINE_IN":  "Type de commande: Sur place",
		"":         "Type de commande: À emporter", // unknown defaults to pickup
		"OTHER":    "Type de commande: À emporter",
	}
	for orderType, want := range cases {
		d := baseInvoice()
		d.OrderType = orderType
		require.Contains(t, generate(t, d), want, "order type %q", orderType)
	}
}

func TestGeneratePDFOptionalBlocksAreOmitted(t *testing.T) {
	d := baseInvoice()
	d.CustomerPhone = nil
	d.Address = nil
	d.VatBreakdown = nil
	d.TotalVatAmount = nil
	d.CouponDiscount = nil
	d.CouponCode = nil
	d.DeliveryFee = nil
	lines := generate(t, d)

	require.Equal(t, []string{
		"Tokyo Sushi Bar — SRL", "Facture",
		"Rue de la Cathédrale 59, 4000 Liège, Belgique",
		"Tél: +32 4 222 98 88  |  Email: tokyosushibar888@gmail.com",
		"N° d'entreprise: BE0772.499.585",
		"Réf. commande: TSB-2026-2C3D4E5F",
		"Date: 01/07/2026 19:30",
		"Type de commande: Livraison",
		"Client", "Jeanne Dupont", "jeanne@example.com",
		"Produit", "Qté", "Prix unit.", "Total",
		"S01 — Sushi au Saumon", "2", "12.50 €", "25.00 €",
		"Thé vert", "1", "3.00 €", "3.00 €",
		"Sous-total", "28.00 €",
		"Total", "27.70 €",
		"TVA comprise",
		"Merci pour votre commande !",
	}, lines)
}

func TestGeneratePDFEmptyPhoneIsNotPrinted(t *testing.T) {
	d := baseInvoice()
	d.CustomerPhone = ptr("")
	lines := generate(t, d)
	idx := indexOf(lines, "jeanne@example.com")
	require.GreaterOrEqual(t, idx, 0)
	require.Equal(t, "Produit", lines[idx+1], "no phone line between e-mail and the table")
}

func indexOf(lines []string, s string) int {
	for i, l := range lines {
		if l == s {
			return i
		}
	}
	return -1
}

func TestGeneratePDFTakeawayDiscountAndCoupon(t *testing.T) {
	d := baseInvoice()
	d.OrderType = "PICKUP"
	d.Address = nil
	d.DeliveryFee = nil
	d.TakeawayDiscount = ptr("2.80")
	d.CouponDiscount = ptr("1.00")
	d.CouponCode = nil // coupon without a code: bare label
	d.Total = "24.20"
	lines := generate(t, d)

	i := indexOf(lines, "Remise emporter (-10%)")
	require.GreaterOrEqual(t, i, 0)
	require.Equal(t, []string{"Remise emporter (-10%)", "- 2.80 €", "Coupon", "- 1.00 €", "Total", "24.20 €"}, lines[i:i+6])
}

func TestGeneratePDFVatLineWithoutRateHasNoPercent(t *testing.T) {
	d := baseInvoice()
	d.VatBreakdown = []InvoiceVatLine{{Label: "TVA 6%", Rate: "", Amount: "1.58"}}
	lines := generate(t, d)
	i := indexOf(lines, "TVA 6%")
	require.GreaterOrEqual(t, i, 0)
	require.Equal(t, "1.58 €", lines[i+1])
	for _, l := range lines {
		require.NotContains(t, l, "()")
		require.NotContains(t, l, "%)")
	}
}

func TestGeneratePDFNonLatinAndSpecialCharacters(t *testing.T) {
	d := baseInvoice()
	d.CustomerName = "王小明 (Wang)"
	d.Items = []InvoiceItem{
		{Name: "寿司拼盘 (大)", Code: "天", Quantity: 3, UnitPrice: "9.00", LineTotal: "27.00"},
		{Name: `Back\slash & (paren)`, Quantity: 1, UnitPrice: "1.00", LineTotal: "1.00"},
	}
	lines := generate(t, d)
	require.Contains(t, lines, "王小明 (Wang)")
	require.Contains(t, lines, "天 — 寿司拼盘 (大)")
	require.Contains(t, lines, `Back\slash & (paren)`)
}

func TestGeneratePDFNoItems(t *testing.T) {
	d := baseInvoice()
	d.Items = nil
	d.Subtotal = "0.00"
	d.Total = "0.00"
	lines := generate(t, d)
	require.Contains(t, lines, "Produit")
	require.Equal(t, "0.00 €", lines[indexOf(lines, "Sous-total")+1])
	require.Equal(t, "0.00 €", lines[len(lines)-3], "grand total precedes the VAT note and thank-you")
}

func TestGeneratePDFManyItemsSpillOverPages(t *testing.T) {
	d := baseInvoice()
	d.Items = nil
	for i := range 80 {
		d.Items = append(d.Items, InvoiceItem{
			Name: fmt.Sprintf("Plat %d", i), Quantity: int64(i + 1),
			UnitPrice: "1.00", LineTotal: fmt.Sprintf("%d.00", i+1),
		})
	}
	pdf, err := GeneratePDF(d)
	require.NoError(t, err)
	require.GreaterOrEqual(t, bytes.Count(pdf, []byte("/Type /Page\n")), 2, "80 rows must paginate")

	lines := pdfTextLines(t, pdf)
	for i := range 80 {
		require.Contains(t, lines, fmt.Sprintf("Plat %d", i), "row %d lost across the page break", i)
	}
	require.Equal(t, "Merci pour votre commande !", lines[len(lines)-1])
}

func TestGeneratePDFUsesBrandIdentity(t *testing.T) {
	setBrand(t, map[string]string{
		"RESTAURANT_LEGAL_NAME":     "Yangguofu Liège SRL",
		"RESTAURANT_ADDRESS":        "Rue Test 1, 4000 Liège",
		"RESTAURANT_PHONE":          "+32 4 000 00 00",
		"RESTAURANT_EMAIL":          "hello@ygf.test",
		"RESTAURANT_VAT":            "BE0123.456.789",
		"RESTAURANT_INVOICE_PREFIX": "YGF",
	})
	lines := generate(t, baseInvoice())
	require.Contains(t, lines, "Yangguofu Liège SRL")
	require.Contains(t, lines, "Rue Test 1, 4000 Liège")
	require.Contains(t, lines, "Tél: +32 4 000 00 00  |  Email: hello@ygf.test")
	require.Contains(t, lines, "N° d'entreprise: BE0123.456.789")
	require.Contains(t, lines, "Réf. commande: YGF-2026-2C3D4E5F")
	require.NotContains(t, lines, "Tokyo Sushi Bar — SRL")
}

func TestFormatOrderRef(t *testing.T) {
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	require.Equal(t, "TSB-2026-2C3D4E5F", formatOrderRef("7b0f3c1e-5d2a-4c8b-9e41-0a1b2c3d4e5f", summer))
	require.Equal(t, "TSB-2026-ABC", formatOrderRef("abc", summer), "ids of 8 chars or fewer are used whole")
	require.Equal(t, "TSB-2026-ABCDEF12", formatOrderRef("abcdef12", summer), "exactly 8 chars")
	// The year is the Brussels year: 23:30 UTC on 31 Dec is already 1 Jan locally.
	nye := time.Date(2025, 12, 31, 23, 30, 0, 0, time.UTC)
	require.Equal(t, "TSB-2026-ABCDEF12", formatOrderRef("abcdef12", nye))
	require.Equal(t, "TSB-2025-ABCDEF12", formatOrderRef("abcdef12", time.Date(2025, 12, 31, 12, 0, 0, 0, time.UTC)))
}

func TestInvoiceAddressFormat(t *testing.T) {
	a := InvoiceAddress{StreetName: "Rue X", HouseNumber: "5", MunicipalityName: "Liège", Postcode: "4000"}
	require.Equal(t, "Rue X 5, 4000 Liège", a.Format())
	a.BoxNumber = ptr("")
	require.Equal(t, "Rue X 5, 4000 Liège", a.Format(), "empty box is ignored")
	a.BoxNumber = ptr("2A")
	require.Equal(t, "Rue X 5 / 2A, 4000 Liège", a.Format())
}

func TestLabelsAndFilePrefix(t *testing.T) {
	require.Equal(t, "facture", FilePrefix("fr"))
	require.Equal(t, "invoice", FilePrefix("en"))
	for _, lang := range []string{"nl", "zh", "", "de"} {
		require.Equal(t, "facture", FilePrefix(lang), lang)
	}
	require.Equal(t, translations["fr"], getLabels("unknown"))
	require.Equal(t, "invoice", FilePrefix("EN-gb"))
	require.Equal(t, "facture", FilePrefix("fr-BE"))
	require.Equal(t, translations["en"], getLabels("en"))
}

func TestLabelsAreCompleteInEveryLanguage(t *testing.T) {
	for lang, l := range translations {
		require.NotEmpty(t, l.FilePrefix, lang)
		require.NotEmpty(t, l.InvoiceTitle, lang)
		require.NotEmpty(t, l.Date, lang)
		require.NotEmpty(t, l.OrderRef, lang)
		require.NotEmpty(t, l.Customer, lang)
		require.NotEmpty(t, l.OrderType, lang)
		require.NotEmpty(t, l.TypeDelivery, lang)
		require.NotEmpty(t, l.TypePickup, lang)
		require.NotEmpty(t, l.TypeDineIn, lang)
		require.NotEmpty(t, l.DeliveryAddress, lang)
		require.NotEmpty(t, l.Product, lang)
		require.NotEmpty(t, l.Qty, lang)
		require.NotEmpty(t, l.UnitPrice, lang)
		require.NotEmpty(t, l.Total, lang)
		require.NotEmpty(t, l.Subtotal, lang)
		require.NotEmpty(t, l.TakeawayDiscount, lang)
		require.NotEmpty(t, l.CouponDiscount, lang)
		require.NotEmpty(t, l.DeliveryFee, lang)
		require.NotEmpty(t, l.TotalVAT, lang)
		require.NotEmpty(t, l.ThankYou, lang)
		require.NotEmpty(t, l.CompanyNumber, lang)
		require.NotEmpty(t, l.Phone, lang)
		require.NotEmpty(t, l.Email, lang)
		require.NotEmpty(t, l.VATIncluded, lang)
	}
}

func TestLogoBytes(t *testing.T) {
	custom := func() []byte {
		var b bytes.Buffer
		require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))))
		return b.Bytes()
	}()

	t.Run("defaults to the embedded logo", func(t *testing.T) {
		t.Setenv("INVOICE_LOGO_PATH", "")
		require.Equal(t, logoPNG, logoBytes())
		require.NotEmpty(t, logoPNG)
	})

	t.Run("INVOICE_LOGO_PATH overrides the logo", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "logo.png")
		require.NoError(t, os.WriteFile(p, custom, 0o600))
		t.Setenv("INVOICE_LOGO_PATH", p)
		require.Equal(t, custom, logoBytes())

		pdf, err := GeneratePDF(baseInvoice())
		require.NoError(t, err)
		require.NotEmpty(t, pdf)
	})

	t.Run("an unreadable path falls back to the embedded logo", func(t *testing.T) {
		t.Setenv("INVOICE_LOGO_PATH", filepath.Join(t.TempDir(), "missing.png"))
		require.Equal(t, logoPNG, logoBytes())
		_, err := GeneratePDF(baseInvoice())
		require.NoError(t, err)
	})
}

func TestGeneratePDFFailsOnCorruptLogo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logo.png")
	require.NoError(t, os.WriteFile(p, []byte("this is not a png"), 0o600))
	t.Setenv("INVOICE_LOGO_PATH", p)

	pdf, err := GeneratePDF(baseInvoice())
	require.Error(t, err)
	require.Nil(t, pdf)
	require.ErrorContains(t, err, "failed to generate PDF")
}

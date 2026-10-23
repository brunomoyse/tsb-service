package application_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
	"tsb-service/pkg/timezone"
)

var dec = decimal.RequireFromString

// snap is a cash pickup order of one 12.50 line, the base the cases modify.
func snap() *domain.OrderSnapshot {
	return &domain.OrderSnapshot{
		OrderID: uuid.MustParse("0a1b2c3d-1111-2222-3333-444455556666"),
		UserID:  uuid.MustParse("99999999-1111-2222-3333-444455556666"),
		Status:  domain.LocalPending, Type: domain.TypePickUp,
		TotalPrice: dec("12.50"),
		Lines: []domain.SnapshotLine{{
			ProductID: uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666"), ProductName: "Sushi au Saumon",
			BasePrice: dec("12.50"), Quantity: 1, TotalPrice: dec("12.50"), VatRate: dec("6.00"),
		}},
	}
}

var (
	groupSauce = uuid.MustParse("bbbbbbbb-1111-2222-3333-444455556666")
	soy        = uuid.MustParse("cccccccc-1111-2222-3333-444455556666")
	spicy      = uuid.MustParse("dddddddd-1111-2222-3333-444455556666")
)

func sel(choice uuid.UUID, name, modifier string, qty int) domain.SnapshotSelection {
	return domain.SnapshotSelection{GroupID: groupSauce, ChoiceID: choice, GroupName: "Sauce", Name: name, Modifier: dec(modifier), Quantity: qty}
}

// total adds an order up like HubRise does: items with options, plus charges, minus discounts.
func total(t *testing.T, o *hubrise.Order) decimal.Decimal {
	t.Helper()
	sum := decimal.Zero
	for _, it := range o.Items {
		unit, err := it.Price.Decimal()
		require.NoError(t, err)
		for _, op := range it.Options {
			p, err := op.Price.Decimal()
			require.NoError(t, err)
			unit = unit.Add(p.Mul(decimal.NewFromInt(int64(op.Quantity))))
		}
		q, err := hubrise.ParseQuantity(it.Quantity)
		require.NoError(t, err)
		sum = sum.Add(unit.Mul(q))
	}
	for _, c := range o.Charges {
		p, err := c.Price.Decimal()
		require.NoError(t, err)
		sum = sum.Add(p)
	}
	for _, d := range o.Discounts {
		p, err := d.PriceOff.Decimal()
		require.NoError(t, err)
		sum = sum.Sub(p)
	}
	return sum
}

func sendEnv(id uuid.UUID) application.SendEnv {
	return application.SendEnv{
		OrderID: id, Now: time.Date(2026, 10, 23, 12, 0, 0, 0, timezone.Location), Status: hubrise.StatusNew,
		DeliveryRef: "DELIVERY", PaymentFeeRef: "PAYMENT_FEE",
		SkuRef:    func(uuid.UUID) string { return "SKU" },
		OptionRef: func(uuid.UUID) string { return "OPT" },
	}
}

func build(t *testing.T, s *domain.OrderSnapshot) (domain.CreatePayload, *hubrise.Order) {
	t.Helper()
	p := application.BuildCreatePayload(s)
	o, _ := application.BuildOrder(p, sendEnv(s.OrderID))
	return p, o
}

func TestShortRef(t *testing.T) {
	assert.Equal(t, "0A1B2C3D", application.ShortRef(uuid.MustParse("0a1b2c3d-1111-2222-3333-444455556666")))
}

func TestPayloadOfASimpleOrderHasNoAdjustment(t *testing.T) {
	p, o := build(t, snap())
	assert.Equal(t, domain.PayloadVersion, p.Version)
	assert.Equal(t, "0A1B2C3D", p.Ref)
	require.Len(t, o.Items, 1)
	it := o.Items[0]
	assert.Equal(t, "Sushi au Saumon", it.ProductName)
	assert.Equal(t, "12.50 EUR", string(it.Price))
	assert.Equal(t, "1", it.Quantity)
	assert.Equal(t, "6", it.TaxRate, "a percent, not a ratio")
	assert.Equal(t, "SKU", it.SkuRef)
	assert.Empty(t, o.Charges)
	assert.Empty(t, o.Discounts)
	assert.Equal(t, "12.50", total(t, o).StringFixed(2))
	assert.Nil(t, p.Customer, "a snapshot without a customer sends none")
}

func TestPayloadRoundingClosesTheGapToTheLocalTotal(t *testing.T) {
	t.Run("a 0.10 rounding of the total becomes a ROUNDING discount", func(t *testing.T) {
		s := snap()
		s.IsOnlinePayment, s.TransactionFee, s.TotalPrice = true, dec("0.32"), dec("12.80") // 12.82 snapped down
		p, o := build(t, s)
		require.Len(t, o.Charges, 1)
		assert.Equal(t, "PAYMENT_FEE", o.Charges[0].Ref)
		require.Len(t, o.Discounts, 1)
		assert.Equal(t, "ROUNDING", o.Discounts[0].Ref)
		assert.Equal(t, "Rounding", o.Discounts[0].Name)
		assert.Equal(t, "0.02 EUR", string(o.Discounts[0].PriceOff))
		assert.True(t, total(t, o).Equal(p.Total), "HubRise's total equals the local total")
	})
	t.Run("rounding up becomes a ROUNDING charge", func(t *testing.T) {
		s := snap()
		s.TotalPrice = dec("12.50")
		s.Lines[0].TotalPrice, s.Lines[0].BasePrice = dec("12.46"), dec("12.46")
		_, o := build(t, s)
		require.Len(t, o.Charges, 1)
		assert.Equal(t, "ROUNDING", o.Charges[0].Ref)
		assert.Equal(t, "0.04 EUR", string(o.Charges[0].Price))
		assert.Equal(t, "12.50", total(t, o).StringFixed(2))
	})
	t.Run("a coupon that covers the basket leaves the fee only", func(t *testing.T) {
		s := snap()
		s.IsOnlinePayment, s.TransactionFee = true, dec("0.32")
		s.CouponDiscount, s.CouponCode = dec("20.00"), "FREE20" // more than the basket: clamped at 0 locally
		s.TotalPrice = dec("0.30")
		p, o := build(t, s)
		assert.Equal(t, "0.30", total(t, o).StringFixed(2))
		assert.True(t, total(t, o).Equal(p.Total))
	})
}

func TestPayloadChargesDiscountsAndCoupon(t *testing.T) {
	s := snap()
	s.Type = domain.TypeDelivery
	s.DeliveryFee, s.TakeawayDiscount = dec("2.50"), dec("1.00")
	s.CouponDiscount, s.CouponCode = dec("2.00"), "SAKURA5"
	s.IsOnlinePayment, s.TransactionFee = true, dec("0.32")
	s.TotalPrice = dec("12.30") // 12.50 + 2.50 - 1 - 2 + 0.32 = 12.32
	p, o := build(t, s)

	assert.Equal(t, hubrise.ServiceDelivery, o.ServiceType)
	require.Len(t, o.Charges, 2) // delivery, fee
	assert.Equal(t, "DELIVERY", o.Charges[0].Ref)
	assert.Equal(t, "PAYMENT_FEE", o.Charges[1].Ref)
	require.Len(t, o.Discounts, 3) // takeaway, coupon, rounding
	assert.Equal(t, "TAKEAWAY", o.Discounts[0].Ref)
	assert.Equal(t, "SAKURA5", o.Discounts[1].Ref)
	assert.Equal(t, "Coupon SAKURA5", o.Discounts[1].Name)
	assert.Equal(t, []string{"SAKURA5"}, o.CouponCodes)
	assert.True(t, total(t, o).Equal(p.Total))
	for _, d := range o.Discounts {
		cents, err := d.PriceOff.Cents()
		require.NoError(t, err)
		assert.Positive(t, cents, "discounts are positive price_off amounts")
	}
}

func TestPayloadChargeRefsAreResolvedAtSendTime(t *testing.T) {
	s := snap()
	s.DeliveryFee, s.Type = dec("2.50"), domain.TypeDelivery
	s.TotalPrice = dec("15.00")
	p := application.BuildCreatePayload(s)
	e := sendEnv(s.OrderID)
	e.DeliveryRef, e.PaymentFeeRef = "LIVRAISON", "FRAIS"
	o, _ := application.BuildOrder(p, e)
	assert.Equal(t, "LIVRAISON", o.Charges[0].Ref)
}

func TestSelectionsDivisibleByTheQuantityStayOneItem(t *testing.T) {
	s := snap()
	// Two bowls with two soy sauces in total: one per bowl. Paid 2 x 10.00 + 2 x 0.50.
	s.Lines[0].Quantity, s.Lines[0].TotalPrice, s.Lines[0].BasePrice = 2, dec("21.00"), dec("10.00")
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 2)}
	s.TotalPrice = dec("21.00")
	_, o := build(t, s)
	require.Len(t, o.Items, 1)
	it := o.Items[0]
	assert.Equal(t, "2", it.Quantity)
	assert.Equal(t, "10.00 EUR", string(it.Price))
	require.Len(t, it.Options, 1)
	assert.Equal(t, hubrise.OrderOption{OptionListName: "Sauce", Name: "Soja", Ref: "OPT", Price: "0.50 EUR", Quantity: 1}, it.Options[0])
	assert.Equal(t, "21.00", total(t, o).StringFixed(2))
}

func TestAnOptionChosenSeveralTimesPerUnitRepeatsAsSeparateObjects(t *testing.T) {
	s := snap()
	s.Lines[0].Quantity, s.Lines[0].TotalPrice, s.Lines[0].BasePrice = 2, dec("26.00"), dec("12.50")
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 4)} // twice per unit
	s.TotalPrice = dec("27.00")
	_, o := build(t, s)
	require.Len(t, o.Items, 1)
	require.Len(t, o.Items[0].Options, 2, "the guide: identical options repeat as separate objects of quantity 1")
	for _, op := range o.Items[0].Options {
		assert.Equal(t, 1, op.Quantity)
	}
	assert.Equal(t, "27.00", total(t, o).StringFixed(2))
}

func TestSelectionsNotDivisibleSplitTheLineAndKeepTheTotals(t *testing.T) {
	s := snap()
	// Three plates, two soy and one spicy sauce for the whole line: 3 x 8.00 + 2 x 0.50 + 1 x 1.00.
	s.Lines[0].Quantity, s.Lines[0].TotalPrice, s.Lines[0].BasePrice = 3, dec("26.00"), dec("8.00")
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 2), sel(spicy, "Piquante", "1.00", 1)}
	s.TotalPrice = dec("26.00")
	_, o := build(t, s)

	require.Len(t, o.Items, 2, "units with the same options are merged back")
	assert.Equal(t, "2", o.Items[0].Quantity)
	require.Len(t, o.Items[0].Options, 1)
	assert.Equal(t, "Soja", o.Items[0].Options[0].Name)
	assert.Equal(t, "1", o.Items[1].Quantity)
	require.Len(t, o.Items[1].Options, 1)
	assert.Equal(t, "Piquante", o.Items[1].Options[0].Name)
	assert.Equal(t, "26.00", total(t, o).StringFixed(2), "the split keeps the line total exact")
}

func TestSelectionsSpreadRoundRobinAcrossUnits(t *testing.T) {
	s := snap()
	// Two plates, three soy sauces: 2 + 1 per unit, and 3 does not divide by 2.
	s.Lines[0].Quantity, s.Lines[0].TotalPrice, s.Lines[0].BasePrice = 2, dec("26.50"), dec("12.50")
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 3)}
	s.TotalPrice = dec("26.50")
	_, o := build(t, s)
	require.Len(t, o.Items, 2)
	assert.Len(t, o.Items[0].Options, 2)
	assert.Len(t, o.Items[1].Options, 1)
	assert.Equal(t, "26.50", total(t, o).StringFixed(2))
}

func TestBasePriceFollowsWhatTheCustomerPaid(t *testing.T) {
	t.Run("a price edited since the order does not change the ticket", func(t *testing.T) {
		s := snap()
		s.Lines[0].Quantity, s.Lines[0].TotalPrice, s.Lines[0].BasePrice = 2, dec("25.00"), dec("13.00") // 12.50 paid, 13.00 now
		s.TotalPrice = dec("25.00")
		_, o := build(t, s)
		assert.Equal(t, "12.50 EUR", string(o.Items[0].Price))
		assert.Empty(t, o.Charges)
	})
	t.Run("a total that is not whole cents per unit falls back to the current price", func(t *testing.T) {
		s := snap()
		s.Lines[0].Quantity, s.Lines[0].TotalPrice, s.Lines[0].BasePrice = 3, dec("10.00"), dec("3.50")
		s.TotalPrice = dec("10.00")
		p, o := build(t, s)
		assert.Equal(t, "3.50 EUR", string(o.Items[0].Price))
		assert.True(t, total(t, o).Equal(p.Total), "the rounding line absorbs the 0.50")
		require.Len(t, o.Discounts, 1)
		assert.Equal(t, "0.50 EUR", string(o.Discounts[0].PriceOff))
	})
	t.Run("a negative derived base falls back too", func(t *testing.T) {
		s := snap()
		s.Lines[0].TotalPrice = dec("0.20")
		s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 1)}
		s.TotalPrice = dec("0.20")
		_, o := build(t, s)
		assert.Equal(t, "12.50 EUR", string(o.Items[0].Price))
	})
}

func TestNegativeModifiersAreClampedToZero(t *testing.T) {
	s := snap()
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "-1.00", 1)}
	_, o := build(t, s)
	assert.Equal(t, "0.00 EUR", string(o.Items[0].Options[0].Price))
	assert.Equal(t, "12.50", total(t, o).StringFixed(2))
}

func TestLinesWithoutQuantityAndOutOfRangeVatAreHandled(t *testing.T) {
	s := snap()
	s.Lines = append(s.Lines, domain.SnapshotLine{ProductName: "ghost", Quantity: 0, TotalPrice: dec("0")})
	s.Lines[0].VatRate = dec("250")
	p, o := build(t, s)
	require.Len(t, o.Items, 1, "a line of zero units is dropped")
	assert.Empty(t, o.Items[0].TaxRate, "an impossible tax rate is left out instead of failing the push")
	assert.Len(t, p.Items, 1)
}

func TestCustomer(t *testing.T) {
	t.Run("pickup carries the identity and no address", func(t *testing.T) {
		s := snap()
		s.FirstName, s.LastName, s.Email, s.Phone = "Jean", "Dupont", "jean@example.com", "+32470000000"
		s.StreetName, s.HouseNumber = "Rue du Test", "12" // stored on every order, used for delivery only
		_, o := build(t, s)
		require.NotNil(t, o.Customer)
		assert.Equal(t, "Jean", o.Customer.FirstName)
		assert.Equal(t, "jean@example.com", o.Customer.Email)
		assert.Empty(t, o.Customer.Address1)
		assert.Empty(t, o.Customer.Country)
	})
	t.Run("delivery adds the address, the position and the notes", func(t *testing.T) {
		s := snap()
		s.Type = domain.TypeDelivery
		s.FirstName, s.LastName = "Jean", "Dupont"
		lat, lng := 50.6326, 5.5797
		s.StreetName, s.HouseNumber, s.BoxNumber, s.Postcode, s.Municipality = "Rue du Test", "12", "3", "4000", "Liège"
		s.AddressLat, s.AddressLng, s.AddressExtra = &lat, &lng, "Second floor"
		_, o := build(t, s)
		c := o.Customer
		require.NotNil(t, c)
		assert.Equal(t, "Rue du Test 12 bte 3", c.Address1)
		assert.Equal(t, "4000", c.PostalCode)
		assert.Equal(t, "Liège", c.City)
		assert.Equal(t, "BE", c.Country)
		assert.Equal(t, "50.6326", c.Latitude)
		assert.Equal(t, "5.5797", c.Longitude)
		assert.Equal(t, "Second floor", c.DeliveryNotes)
	})
	t.Run("a delivery without coordinates or box", func(t *testing.T) {
		s := snap()
		s.Type = domain.TypeDelivery
		s.StreetName, s.HouseNumber = "Rue du Test", "12"
		_, o := build(t, s)
		assert.Equal(t, "Rue du Test 12", o.Customer.Address1)
		assert.Empty(t, o.Customer.Latitude)
	})
	t.Run("a registered user replaces the guest object", func(t *testing.T) {
		s := snap()
		s.FirstName = "Jean"
		p := application.BuildCreatePayload(s)
		e := sendEnv(s.OrderID)
		e.CustomerID = "ve343"
		o, _ := application.BuildOrder(p, e)
		assert.Equal(t, "ve343", o.CustomerID)
		assert.Nil(t, o.Customer, "customer_id and a guest customer are never combined")
	})
}

func TestCustomerNotes(t *testing.T) {
	s := snap()
	s.Note = "  Sonnez deux fois  "
	s.Extras = []domain.OrderExtra{{Name: "chopsticks"}, {Name: "sauce", Options: []string{"sweet", "salty"}}, {Name: ""}}
	cash := dec("50")
	s.CashAmount = &cash
	p, o := build(t, s)
	assert.Equal(t, "Sonnez deux fois\nExtras: chopsticks; sauce (sweet, salty)\nCash: prepare change for 50.00 EUR", o.CustomerNotes)
	assert.Equal(t, o.CustomerNotes, p.CustomerNotes)

	online := snap()
	online.IsOnlinePayment, online.CashAmount = true, &cash
	_, o = build(t, online)
	assert.Empty(t, o.CustomerNotes, "a cash amount means nothing for an online order")
}

func TestPayments(t *testing.T) {
	t.Run("cash is an unpaid CASH entry", func(t *testing.T) {
		_, o := build(t, snap())
		require.Len(t, o.Payments, 1)
		assert.Equal(t, hubrise.OrderPayment{Name: "Cash", Ref: "CASH", Amount: "12.50 EUR"}, o.Payments[0])
	})
	t.Run("online is a MOLLIE entry with the payment id", func(t *testing.T) {
		s := snap()
		s.IsOnlinePayment, s.MolliePaymentID = true, "tr_abc123"
		s.TransactionFee, s.TotalPrice = dec("0.32"), dec("12.80")
		_, o := build(t, s)
		require.Len(t, o.Payments, 1)
		p := o.Payments[0]
		assert.Equal(t, "Mollie", p.Name)
		assert.Equal(t, "MOLLIE", p.Ref)
		assert.Equal(t, "12.80 EUR", string(p.Amount))
		assert.JSONEq(t, `{"mollie_payment_id":"tr_abc123"}`, string(p.Info))
		assert.Empty(t, p.Type, "the deprecated type field is not sent")
	})
	t.Run("an online order without a payment id still has a payment", func(t *testing.T) {
		s := snap()
		s.IsOnlinePayment = true
		_, o := build(t, s)
		assert.Empty(t, o.Payments[0].Info)
	})
}

func TestExpectedTimeOnlyWhenInTheFuture(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, timezone.Location)
	for name, tc := range map[string]struct {
		ready    *time.Time
		wantTime bool
	}{
		"none":   {nil, false},
		"future": {ptr(now.Add(30 * time.Minute)), true},
		"past":   {ptr(now.Add(-time.Minute)), false},
		"now":    {ptr(now), false},
	} {
		t.Run(name, func(t *testing.T) {
			s := snap()
			s.PreferredReadyTime = tc.ready
			o, _ := application.BuildOrder(application.BuildCreatePayload(s), sendEnv(s.OrderID))
			if tc.wantTime {
				require.NotNil(t, o.ExpectedTime)
				assert.Equal(t, timezone.Location, o.ExpectedTime.Location(), "times are sent in Europe/Brussels")
				assert.False(t, *o.Asap)
				return
			}
			assert.Nil(t, o.ExpectedTime)
			require.NotNil(t, o.Asap)
			assert.True(t, *o.Asap)
		})
	}
}

func TestChannel(t *testing.T) {
	cases := []struct {
		name, order, setting, want string
	}{
		{"web by default", "", "", "webshop"},
		{"web order", "web", "", "webshop"},
		{"app order", "app", "", "app"},
		{"the setting wins over the order", "app", "Website", "Website"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := snap()
			s.Channel = tc.order
			e := sendEnv(s.OrderID)
			e.Channel = tc.setting
			o, _ := application.BuildOrder(application.BuildCreatePayload(s), e)
			assert.Equal(t, tc.want, o.Channel)
		})
	}
}

func TestBuildOrderCarriesTheIdentityAndTheStatus(t *testing.T) {
	s := snap()
	e := sendEnv(s.OrderID)
	e.Status = hubrise.StatusInPreparation
	o, unlinked := application.BuildOrder(application.BuildCreatePayload(s), e)
	assert.False(t, unlinked)
	assert.Equal(t, s.OrderID.String(), o.PrivateRef)
	assert.Equal(t, "0A1B2C3D", o.Ref)
	assert.Equal(t, hubrise.StatusInPreparation, o.Status)
	assert.Equal(t, hubrise.ServiceCollection, o.ServiceType)
	assert.Empty(t, o.Total, "the total is calculated by HubRise, never sent")
}

func TestUnlinkedItemsAndOptionsAreFlaggedButStillSent(t *testing.T) {
	s := snap()
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 1)}
	p := application.BuildCreatePayload(s)

	e := sendEnv(s.OrderID)
	e.SkuRef = func(uuid.UUID) string { return "" }
	o, unlinked := application.BuildOrder(p, e)
	assert.True(t, unlinked)
	assert.Empty(t, o.Items[0].SkuRef)
	assert.Equal(t, "Sushi au Saumon", o.Items[0].ProductName, "the item is still sent")

	e = sendEnv(s.OrderID)
	e.OptionRef = func(uuid.UUID) string { return "" }
	_, unlinked = application.BuildOrder(p, e)
	assert.True(t, unlinked, "an option without a ref counts too")
}

func TestPreferredReadyTimeIsStoredInBrussels(t *testing.T) {
	s := snap()
	utc := time.Date(2026, 7, 1, 10, 30, 0, 0, time.UTC)
	s.PreferredReadyTime = &utc
	p := application.BuildCreatePayload(s)
	require.NotNil(t, p.PreferredReadyTime)
	assert.Equal(t, "12:30", p.PreferredReadyTime.Format("15:04"))
}

func TestPayloadSurvivesTheOutbox(t *testing.T) {
	s := snap()
	s.IsOnlinePayment, s.MolliePaymentID, s.TransactionFee, s.TotalPrice = true, "tr_x", dec("0.32"), dec("12.80")
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 1)}
	s.Lines[0].TotalPrice = dec("13.00")
	s.TotalPrice = dec("13.30")
	want := application.BuildCreatePayload(s)
	raw, err := want.Marshal()
	require.NoError(t, err)
	var got domain.CreatePayload
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.True(t, want.Total.Equal(got.Total))
	wantOrder, _ := application.BuildOrder(want, sendEnv(s.OrderID))
	gotOrder, _ := application.BuildOrder(got, sendEnv(s.OrderID))
	assert.Equal(t, wantOrder, gotOrder)
}

// The mock validates what a real HubRise would reject; the payload of every case must pass it, and
// it must decode into the API types without losing a field.
func TestEveryPayloadIsAcceptedByTheMock(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	s := snap()
	s.Type = domain.TypeDelivery
	s.FirstName, s.StreetName, s.HouseNumber, s.Postcode, s.Municipality = "Jean", "Rue du Test", "12", "4000", "Liège"
	s.DeliveryFee, s.TakeawayDiscount = dec("2.50"), dec("1.00")
	s.Lines[0].Quantity, s.Lines[0].TotalPrice, s.Lines[0].BasePrice = 3, dec("26.00"), dec("8.00")
	s.Lines[0].Selections = []domain.SnapshotSelection{sel(soy, "Soja", "0.50", 2), sel(spicy, "Piquante", "1.00", 1)}
	s.TotalPrice = dec("27.50")
	p := application.BuildCreatePayload(s)
	o, _ := application.BuildOrder(p, sendEnv(s.OrderID))
	created, err := c.CreateOrder(t.Context(), o)
	require.NoError(t, err)
	got, err := created.Total.Decimal()
	require.NoError(t, err)
	assert.True(t, got.Equal(p.Total), "HubRise calculates %s, the shop charged %s", got, p.Total)
}

// Golden: the shape of an order as the moyun-pos integration records it (the "unpaid_collected"
// scenario: a website order for collection with two lines at 6 % VAT) is what this payload produces
// for the same basket, and the ROUNDING and payment parts are the ones documented for the webshop.
func TestPayloadMatchesTheMoyunPosFixture(t *testing.T) {
	var fixture struct {
		Request pkgOrder `json:"request"`
	}
	for _, x := range hubrisetest.Scenario("unpaid_collected") {
		if x.Method == "POST" {
			require.NoError(t, json.Unmarshal(x.Request, &fixture.Request))
			break
		}
	}
	require.NotEmpty(t, fixture.Request.Items)

	s := snap()
	s.Lines = []domain.SnapshotLine{
		{ProductID: uuid.New(), ProductName: "California roll", BasePrice: dec("8.90"), Quantity: 1, TotalPrice: dec("8.90"), VatRate: dec("6")},
		{ProductID: uuid.New(), ProductName: "Coca-Cola 33cl", BasePrice: dec("3.00"), Quantity: 2, TotalPrice: dec("6.00"), VatRate: dec("6")},
	}
	s.TotalPrice = dec("14.90")
	refs := []string{"CALIFORNIA-8", "COCA-33"}
	e := sendEnv(s.OrderID)
	idx := map[uuid.UUID]string{s.Lines[0].ProductID: refs[0], s.Lines[1].ProductID: refs[1]}
	e.SkuRef = func(id uuid.UUID) string { return idx[id] }
	o, unlinked := application.BuildOrder(application.BuildCreatePayload(s), e)
	assert.False(t, unlinked)

	require.Len(t, o.Items, len(fixture.Request.Items))
	for i, want := range fixture.Request.Items {
		got := o.Items[i]
		assert.Equal(t, want.ProductName, got.ProductName)
		assert.Equal(t, want.SkuRef, got.SkuRef)
		assert.Equal(t, want.Price, got.Price)
		assert.Equal(t, want.Quantity, got.Quantity)
		wantTax, err := hubrise.ParseTaxRate(want.TaxRate)
		require.NoError(t, err)
		gotTax, err := hubrise.ParseTaxRate(got.TaxRate)
		require.NoError(t, err)
		assert.True(t, wantTax.Equal(gotTax), "tax rate %s vs %s", want.TaxRate, got.TaxRate)
	}
	assert.Equal(t, fixture.Request.ServiceType, o.ServiceType)
	assert.Equal(t, hubrise.StatusNew, o.Status)
	assert.Equal(t, fixture.Request.Status, o.Status)
}

type pkgOrder = hubrise.Order

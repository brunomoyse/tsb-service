package application

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/timezone"
)

// Names and refs of the adjustments the push adds to an order.
const (
	refTakeaway = "TAKEAWAY"
	refRounding = "ROUNDING"

	refMollie = "MOLLIE"
	refCash   = "CASH"

	// defaultChannel is the HubRise channel of a web order when the settings name none.
	defaultChannel = "webshop"
	appChannel     = "app"
)

// BuildCreatePayload freezes the item part of an order_create from a snapshot of the order. The
// lines are priced from what the customer was charged, so the payload adds up to the local total:
// whatever is left over (the 0.10 EUR rounding of the total, a coupon that covered the whole basket)
// goes into an explicit ROUNDING charge or discount, which the HubRise total then includes.
func BuildCreatePayload(s *domain.OrderSnapshot) domain.CreatePayload {
	p := domain.CreatePayload{
		Version:         domain.PayloadVersion,
		Ref:             ShortRef(s.OrderID),
		OrderChannel:    s.Channel,
		Online:          s.IsOnlinePayment,
		ServiceType:     s.Type,
		CustomerNotes:   customerNotes(s),
		CouponCode:      s.CouponCode,
		Total:           s.TotalPrice,
		MolliePaymentID: s.MolliePaymentID,
		UserID:          s.UserID,
		Customer:        customerOf(s),
	}
	if s.PreferredReadyTime != nil {
		t := timezone.In(*s.PreferredReadyTime)
		p.PreferredReadyTime = &t
	}

	computed := decimal.Zero
	for _, l := range s.Lines {
		for _, it := range itemsOf(l) {
			p.Items = append(p.Items, it)
			computed = computed.Add(itemTotal(it))
		}
	}
	if s.DeliveryFee.Sign() > 0 {
		p.Charges = append(p.Charges, domain.PayloadAdjustment{Kind: domain.AdjustmentDelivery, Name: "Delivery fee", Amount: s.DeliveryFee})
		computed = computed.Add(s.DeliveryFee)
	}
	if s.TransactionFee.Sign() > 0 {
		p.Charges = append(p.Charges, domain.PayloadAdjustment{Kind: domain.AdjustmentPaymentFee, Name: "Online payment fee", Amount: s.TransactionFee})
		computed = computed.Add(s.TransactionFee)
	}
	if s.TakeawayDiscount.Sign() > 0 {
		p.Discounts = append(p.Discounts, domain.PayloadAdjustment{Kind: domain.AdjustmentTakeaway, Name: "Takeaway discount", Ref: refTakeaway, Amount: s.TakeawayDiscount})
		computed = computed.Sub(s.TakeawayDiscount)
	}
	if s.CouponDiscount.Sign() > 0 {
		p.Discounts = append(p.Discounts, domain.PayloadAdjustment{Kind: domain.AdjustmentCoupon, Name: "Coupon " + s.CouponCode, Ref: s.CouponCode, Amount: s.CouponDiscount})
		computed = computed.Sub(s.CouponDiscount)
	}
	switch gap := s.TotalPrice.Sub(computed); {
	case gap.Sign() > 0:
		p.Charges = append(p.Charges, domain.PayloadAdjustment{Kind: domain.AdjustmentRounding, Name: "Rounding", Ref: refRounding, Amount: gap})
	case gap.Sign() < 0:
		p.Discounts = append(p.Discounts, domain.PayloadAdjustment{Kind: domain.AdjustmentRounding, Name: "Rounding", Ref: refRounding, Amount: gap.Neg()})
	}
	return p
}

// ShortRef is the short human reference of an order: the first 8 characters of its id, upper case.
func ShortRef(id uuid.UUID) string { return strings.ToUpper(id.String()[:8]) }

// itemTotal is price x quantity plus the options of every unit.
func itemTotal(it domain.PayloadItem) decimal.Decimal {
	unit := it.Price
	for _, o := range it.Options {
		unit = unit.Add(o.Price)
	}
	return unit.Mul(decimal.NewFromInt(int64(it.Quantity)))
}

// clamp keeps a price modifier from ever discounting, as order pricing does.
func clamp(d decimal.Decimal) decimal.Decimal {
	if d.Sign() < 0 {
		return decimal.Zero
	}
	return d
}

// baseOf is the unit price of the product for this line. Selection quantities are totals for the
// whole line, so the line total is base x quantity + the surcharges, and the base that reproduces
// what the customer paid is (total - surcharges) / quantity. When that is not a whole number of
// cents (or negative), the price of the product today is used and the ROUNDING line absorbs the rest.
func baseOf(l domain.SnapshotLine) decimal.Decimal {
	q := decimal.NewFromInt(l.Quantity)
	surcharges := decimal.Zero
	for _, s := range l.Selections {
		surcharges = surcharges.Add(clamp(s.Modifier).Mul(decimal.NewFromInt(int64(s.Quantity))))
	}
	if q.Sign() > 0 {
		paid := l.TotalPrice.Sub(surcharges)
		if base := paid.DivRound(q, 2); base.Sign() >= 0 && base.Mul(q).Equal(paid) {
			return base
		}
	}
	return l.BasePrice
}

// itemsOf turns an order line into HubRise items. Option quantities of HubRise are per unit, while
// the shop stores totals for the line: when every selection divides by the quantity the line stays
// one item, otherwise it is split per unit with the selections spread as evenly as possible (the
// totals stay exact; this is an approximation for the kitchen ticket). Units that end up with the
// same options are merged again.
func itemsOf(l domain.SnapshotLine) []domain.PayloadItem {
	if l.Quantity <= 0 {
		return nil
	}
	q := int(l.Quantity)
	base := baseOf(l)
	tax := ""
	if t, err := hubrise.FormatTaxRate(l.VatRate); err == nil {
		tax = t
	}
	mk := func(qty int, counts []int) domain.PayloadItem {
		it := domain.PayloadItem{ProductID: l.ProductID, ProductName: l.ProductName, Price: base, Quantity: qty, TaxRate: tax}
		for i, s := range l.Selections {
			for range counts[i] {
				it.Options = append(it.Options, domain.PayloadOption{
					GroupID: s.GroupID, ChoiceID: s.ChoiceID, ListName: s.GroupName, Name: s.Name, Price: clamp(s.Modifier),
				})
			}
		}
		return it
	}

	divisible := true
	for _, s := range l.Selections {
		if s.Quantity < 0 || s.Quantity%q != 0 {
			divisible = false
		}
	}
	if divisible {
		counts := make([]int, len(l.Selections))
		for i, s := range l.Selections {
			counts[i] = s.Quantity / q
		}
		return []domain.PayloadItem{mk(q, counts)}
	}

	// Split per unit: selection i gives every unit n/q, and the n%q left over go to the next units
	// in turn, continuing where the previous selection stopped.
	units := make([][]int, q)
	for j := range units {
		units[j] = make([]int, len(l.Selections))
	}
	next := 0
	for i, s := range l.Selections {
		n := max(s.Quantity, 0)
		for j := range q {
			units[j][i] = n / q
		}
		for range n % q {
			units[next%q][i]++
			next++
		}
	}
	var items []domain.PayloadItem
	index := map[string]int{}
	for _, counts := range units {
		key := fmt.Sprint(counts)
		if at, ok := index[key]; ok {
			items[at].Quantity++
			continue
		}
		index[key] = len(items)
		items = append(items, mk(1, counts))
	}
	return items
}

func customerNotes(s *domain.OrderSnapshot) string {
	var parts []string
	if note := strings.TrimSpace(s.Note); note != "" {
		parts = append(parts, note)
	}
	var extras []string
	for _, e := range s.Extras {
		switch {
		case e.Name == "":
		case len(e.Options) > 0:
			extras = append(extras, e.Name+" ("+strings.Join(e.Options, ", ")+")")
		default:
			extras = append(extras, e.Name)
		}
	}
	if len(extras) > 0 {
		parts = append(parts, "Extras: "+strings.Join(extras, "; "))
	}
	if !s.IsOnlinePayment && s.CashAmount != nil && s.CashAmount.Sign() > 0 {
		parts = append(parts, "Cash: prepare change for "+string(hubrise.MoneyFromDecimal(*s.CashAmount)))
	}
	return strings.Join(parts, "\n")
}

func customerOf(s *domain.OrderSnapshot) *domain.PayloadCustomer {
	c := &domain.PayloadCustomer{FirstName: s.FirstName, LastName: s.LastName, Email: s.Email, Phone: s.Phone}
	if s.Type == domain.TypeDelivery {
		street := strings.TrimSpace(s.StreetName + " " + s.HouseNumber)
		if s.BoxNumber != "" {
			street += " bte " + s.BoxNumber
		}
		c.Address1, c.PostalCode, c.City, c.DeliveryNotes = street, s.Postcode, s.Municipality, s.AddressExtra
		if s.AddressLat != nil && s.AddressLng != nil {
			c.Latitude = strconv.FormatFloat(*s.AddressLat, 'f', -1, 64)
			c.Longitude = strconv.FormatFloat(*s.AddressLng, 'f', -1, 64)
		}
	}
	if *c == (domain.PayloadCustomer{}) {
		return nil
	}
	return c
}

// SendEnv is what the worker resolves at send time and the payload builder needs: the status, the
// refs from the link table, the settings that may have changed since the order was enqueued.
type SendEnv struct {
	OrderID uuid.UUID
	Now     time.Time
	Status  hubrise.OrderStatus
	// Channel is the connection's channel name, empty to derive it from the order.
	Channel       string
	DeliveryRef   string
	PaymentFeeRef string
	// CustomerID, when set, is the HubRise customer of a registered user: it replaces the guest
	// customer object (the two are never combined).
	CustomerID string
	// SkuRef and OptionRef return the HubRise ref of a product or a choice, "" when it is not linked.
	SkuRef    func(productID uuid.UUID) string
	OptionRef func(choiceID uuid.UUID) string
}

// BuildOrder turns the frozen payload into the HubRise order, with everything that must be current at
// send time resolved. unlinked reports that an item or option has no ref (it is still sent).
func BuildOrder(p domain.CreatePayload, env SendEnv) (order *hubrise.Order, unlinked bool) {
	o := &hubrise.Order{
		PrivateRef: env.OrderID.String(),
		Ref:        p.Ref,
		Status:     env.Status,
		Channel:    channelOf(p, env),
	}
	switch p.ServiceType {
	case domain.TypeDelivery:
		o.ServiceType = hubrise.ServiceDelivery
	default:
		o.ServiceType = hubrise.ServiceCollection
	}
	// The expected time must be in the future or absent (the integration guide).
	if p.PreferredReadyTime != nil && p.PreferredReadyTime.After(env.Now) {
		o.ExpectedTime = hubrise.Time(timezone.In(*p.PreferredReadyTime))
		o.Asap = hubrise.Bool(false)
	} else {
		o.Asap = hubrise.Bool(true)
	}
	o.CustomerNotes = p.CustomerNotes
	if p.CouponCode != "" {
		o.CouponCodes = []string{p.CouponCode}
	}

	switch {
	case env.CustomerID != "":
		o.CustomerID = env.CustomerID
	case p.Customer != nil:
		o.Customer = &hubrise.Customer{
			FirstName: p.Customer.FirstName, LastName: p.Customer.LastName, Email: p.Customer.Email, Phone: p.Customer.Phone,
			Address1: p.Customer.Address1, PostalCode: p.Customer.PostalCode, City: p.Customer.City,
			Latitude: p.Customer.Latitude, Longitude: p.Customer.Longitude, DeliveryNotes: p.Customer.DeliveryNotes,
		}
		if p.Customer.Address1 != "" {
			o.Customer.Country = "BE"
		}
	}

	for _, it := range p.Items {
		item := hubrise.OrderItem{
			ProductName: it.ProductName,
			SkuRef:      env.SkuRef(it.ProductID),
			Price:       hubrise.MoneyFromDecimal(it.Price),
			Quantity:    hubrise.FormatQuantity(it.Quantity),
			TaxRate:     it.TaxRate,
		}
		if item.SkuRef == "" {
			unlinked = true
		}
		for _, op := range it.Options {
			ref := env.OptionRef(op.ChoiceID)
			if ref == "" {
				unlinked = true
			}
			item.Options = append(item.Options, hubrise.OrderOption{
				OptionListName: op.ListName, Name: op.Name, Ref: ref, Price: hubrise.MoneyFromDecimal(op.Price), Quantity: 1,
			})
		}
		o.Items = append(o.Items, item)
	}
	for _, c := range p.Charges {
		ref := c.Ref
		switch c.Kind {
		case domain.AdjustmentDelivery:
			ref = env.DeliveryRef
		case domain.AdjustmentPaymentFee:
			ref = env.PaymentFeeRef
		}
		o.Charges = append(o.Charges, hubrise.OrderCharge{Name: c.Name, Ref: ref, Price: hubrise.MoneyFromDecimal(c.Amount)})
	}
	for _, d := range p.Discounts {
		o.Discounts = append(o.Discounts, hubrise.OrderDiscount{Name: d.Name, Ref: d.Ref, PriceOff: hubrise.MoneyFromDecimal(d.Amount)})
	}

	// At least one payment, always. A Mollie payment is prepaid; the cash entry is how the EPOS
	// learns the order is to be collected (it treats cash refs as unpaid).
	pay := hubrise.OrderPayment{Name: "Cash", Ref: refCash, Amount: hubrise.MoneyFromDecimal(p.Total)}
	if p.Online {
		pay = hubrise.OrderPayment{Name: "Mollie", Ref: refMollie, Amount: hubrise.MoneyFromDecimal(p.Total)}
		if p.MolliePaymentID != "" {
			if info, err := json.Marshal(map[string]string{"mollie_payment_id": p.MolliePaymentID}); err == nil {
				pay.Info = info
			}
		}
	}
	o.Payments = []hubrise.OrderPayment{pay}
	return o, unlinked
}

// channelOf is the HubRise channel: the connection's setting, else "app" for an app order, else
// "webshop".
func channelOf(p domain.CreatePayload, env SendEnv) string {
	switch {
	case env.Channel != "":
		return env.Channel
	case p.OrderChannel == domain.ChannelApp:
		return appChannel
	default:
		return defaultChannel
	}
}

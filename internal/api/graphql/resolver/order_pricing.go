package resolver

// Order pricing: ONE function validates and prices a basket. createOrder runs it in fail-fast mode
// (it stops at the first problem and turns it into the GraphQL error, exactly as before the quote
// existed) and quoteOrder runs it to the end and reports every problem, so what the quote shows is
// what createOrder charges: the maths is never written twice. It lives outside order.go because
// gqlgen relocates helper code it finds in the files it regenerates.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"tsb-service/internal/api/graphql/apperr"
	addressDomain "tsb-service/internal/modules/address/domain"
	couponDomain "tsb-service/internal/modules/coupon/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	productDomain "tsb-service/internal/modules/product/domain"
	restaurantDomain "tsb-service/internal/modules/restaurant/domain"
	"tsb-service/pkg/money"
	"tsb-service/pkg/utils"
)

// The slices of the services the pricing needs. The application services satisfy them as they are;
// the unit tests use small fakes.
type (
	pricingProducts interface {
		GetProductsForPricing(ctx context.Context, productIDs []string) ([]*productDomain.ProductOrderDetails, error)
		GetChoiceByID(ctx context.Context, choiceID uuid.UUID) (*productDomain.ProductChoice, error)
		GetChoiceGroupsByProductID(ctx context.Context, productID uuid.UUID) ([]*productDomain.ProductChoiceGroup, error)
	}
	pricingAddresses interface {
		Resolve(ctx context.Context, placeID, sessionToken string) (*addressDomain.Address, error)
	}
	pricingCoupons interface {
		ValidateCoupon(ctx context.Context, code string, orderAmount decimal.Decimal, userID uuid.UUID) (*couponDomain.Coupon, decimal.Decimal, error)
	}
	pricingOrders interface {
		HasActiveCouponOrder(ctx context.Context, userID uuid.UUID) (bool, error)
	}
	pricingRestaurant interface {
		IsDevMode() bool
		GetConfigWithOverrides(ctx context.Context) (*restaurantDomain.RestaurantConfig, map[string]*restaurantDomain.ScheduleOverride, error)
	}
)

type orderPricer struct {
	products   pricingProducts
	addresses  pricingAddresses
	coupons    pricingCoupons
	orders     pricingOrders
	restaurant pricingRestaurant
}

// orderPricer builds the pricer from the resolver's services.
func (r *Resolver) orderPricer() *orderPricer {
	return &orderPricer{
		products:   r.ProductService,
		addresses:  r.AddressService,
		coupons:    r.CouponService,
		orders:     r.OrderService,
		restaurant: r.RestaurantService,
	}
}

const (
	maxOrderLines = 50
	maxLineQty    = 99
	// The delivery minimum (EUR, goods only, before the fee) and the radius (meters).
	deliveryMinimumEUR = 25
	deliveryMaxMeters  = 9000
	// Pickup discount: from this basket amount (goods + fee) up, 10 % of the discountable lines.
	pickupDiscountThresholdEUR = 20
)

type pricingSelection struct {
	GroupID  uuid.UUID
	ChoiceID uuid.UUID
	Quantity int
}

type pricingItem struct {
	ProductID uuid.UUID
	Quantity  int
	// ChoiceID is the legacy single choice (applies to every unit of the line).
	ChoiceID   *uuid.UUID
	Selections []pricingSelection
	// ExpectedLineTotal is what the client displays for the line (quote only).
	ExpectedLineTotal *decimal.Decimal
}

type pricingInput struct {
	// UserID is nil for an anonymous quote: the coupon is then not evaluated.
	UserID             *uuid.UUID
	OrderType          orderDomain.OrderType
	IsOnlinePayment    bool
	AddressPlaceID     *string
	PreferredReadyTime *time.Time
	Items              []pricingItem
	CouponCode         *string
	// SkipOrderingGate lets store-review accounts order outside opening hours (see CreateOrder).
	SkipOrderingGate bool
	// FailFast stops at the first problem (createOrder); false reports all of them (quoteOrder).
	FailFast bool
	// Now is the clock; zero means time.Now().
	Now time.Time
}

// pricingIssue is one thing that stops the basket from being ordered as it is.
type pricingIssue struct {
	// Err carries the stable code, the English message and the extension parameters; createOrder
	// returns it unchanged.
	Err *apperr.Error
	// Line is the index of the item it belongs to, -1 for an order-level issue.
	Line int
	// CurrentPrice is today's price of the line's product, whenever it exists.
	CurrentPrice *decimal.Decimal
	// Minimum (EUR) for DELIVERY_MINIMUM_NOT_MET / COUPON_MIN_ORDER_NOT_MET.
	Minimum *string
}

func (i *pricingIssue) Code() apperr.Code { return i.Err.Code }

type pricedLine struct {
	ProductID  uuid.UUID
	Quantity   int
	Selections []pricingSelection
	// Modifiers is today's price modifier of each selected choice.
	Modifiers    map[uuid.UUID]decimal.Decimal
	ProductPrice *decimal.Decimal
	UnitPrice    decimal.Decimal
	LineTotal    decimal.Decimal
	// Priced is false when the line cannot be priced; it then adds nothing to the totals.
	Priced bool
}

type couponOutcome struct {
	Code      string
	Valid     bool
	ErrorCode *string
}

type pricingResult struct {
	Lines  []*pricedLine
	Issues []*pricingIssue

	Subtotal       decimal.Decimal
	DeliveryFee    decimal.Decimal
	PickupDiscount decimal.Decimal
	CouponDiscount decimal.Decimal
	OnlineFee      decimal.Decimal
	// Total is what the customer is charged: orderDomain.OrderTotal, never negative.
	Total decimal.Decimal

	Coupon *couponOutcome

	// What createOrder persists.
	RawItems     []orderDomain.OrderProductRaw
	Products     []*productDomain.ProductOrderDetails
	Address      *orderDomain.AddressSnapshot
	CouponCode   *string
	CouponID     *uuid.UUID
	ProductsByID map[uuid.UUID]*productDomain.ProductOrderDetails
}

// FirstError is the error createOrder returns: the first problem found, in validation order.
func (r *pricingResult) FirstError() error {
	if len(r.Issues) == 0 {
		return nil
	}
	return r.Issues[0].Err
}

func (r *pricingResult) add(issue *pricingIssue) { r.Issues = append(r.Issues, issue) }

func (r *pricingResult) addOrder(err *apperr.Error) *pricingIssue {
	issue := &pricingIssue{Err: err, Line: -1}
	r.add(issue)
	return issue
}

// LineIssues are the issues of item i, in discovery order.
func (r *pricingResult) LineIssues(i int) []*pricingIssue {
	var out []*pricingIssue
	for _, issue := range r.Issues {
		if issue.Line == i {
			out = append(out, issue)
		}
	}
	return out
}

// OrderIssues are the order-level issues.
func (r *pricingResult) OrderIssues() []*pricingIssue {
	var out []*pricingIssue
	for _, issue := range r.Issues {
		if issue.Line < 0 {
			out = append(out, issue)
		}
	}
	return out
}

func (r *pricingResult) stop(in pricingInput) bool { return in.FailFast && len(r.Issues) > 0 }

// price validates and prices the basket. A non-nil error is a server fault (a failed lookup); every
// problem the customer can fix is an issue on the result instead.
func (p *orderPricer) price(ctx context.Context, in pricingInput) (*pricingResult, error) {
	res := &pricingResult{ProductsByID: map[uuid.UUID]*productDomain.ProductOrderDetails{}}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	// 0) Ordering availability and the ready-time slot. allowLunchOnly defaults to true so dev mode
	// does not block testing of lunch-only items; production paths take it from the resolved schedule.
	allowLunchOnly := true
	if !in.SkipOrderingGate && !p.restaurant.IsDevMode() {
		config, overrides, err := p.restaurant.GetConfigWithOverrides(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load restaurant config: %w", err)
		}
		if !config.OrderingEnabled {
			res.addOrder(apperr.New(apperr.CodeOrderingUnavailable, "ordering is currently unavailable"))
		} else {
			isOpenNow := config.IsOrderingCurrentlyOpen(now, overrides)
			if err := validatePreferredReadyTime(in.PreferredReadyTime, config, overrides, now, isOpenNow); err != nil {
				appErr, ok := apperr.From(err)
				if !ok {
					return nil, err
				}
				res.addOrder(appErr)
			}
			slotTime := now
			if in.PreferredReadyTime != nil {
				slotTime = *in.PreferredReadyTime
			}
			allowLunchOnly = config.IsLunchOnlyAllowed(slotTime, overrides)
		}
	}
	if res.stop(in) {
		return res, nil
	}

	// 1) The basket and its products.
	lineCount := len(in.Items)
	if lineCount == 0 {
		res.addOrder(apperr.New(apperr.CodeOrderEmpty, "order must contain at least one item"))
		return res, nil
	}
	if lineCount > maxOrderLines {
		res.addOrder(apperr.New(apperr.CodeOrderTooManyItems, "order cannot contain more than 50 different items"))
		return res, nil
	}
	ids := make([]string, lineCount)
	for i, item := range in.Items {
		ids[i] = item.ProductID.String()
	}
	products, err := p.products.GetProductsForPricing(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve products: %w", err)
	}
	res.Products = products
	for _, prod := range products {
		res.ProductsByID[prod.ID] = prod
	}

	// 2) Price every line. A line that cannot be priced keeps its issue and adds nothing.
	cache := newChoiceCache(p.products)
	orderServiceType := productDomain.ServiceTypeTakeaway
	if in.OrderType == orderDomain.OrderTypeDelivery {
		orderServiceType = productDomain.ServiceTypeDelivery
	}
	for i, item := range in.Items {
		// Until the line is priced, the selections are echoed as the client sent them.
		line := &pricedLine{
			ProductID: item.ProductID, Quantity: item.Quantity, Selections: item.Selections,
			Modifiers: map[uuid.UUID]decimal.Decimal{},
		}
		res.Lines = append(res.Lines, line)
		if err := p.priceLine(ctx, res, line, i, item, cache, allowLunchOnly, orderServiceType); err != nil {
			return nil, err
		}
		if res.stop(in) {
			return res, nil
		}
	}

	// 3) The delivery minimum applies to the goods (pickup has none).
	if in.OrderType == orderDomain.OrderTypeDelivery && res.Subtotal.LessThan(decimal.NewFromInt(deliveryMinimumEUR)) {
		minimum := fmt.Sprint(deliveryMinimumEUR)
		issue := res.addOrder(apperr.New(apperr.CodeDeliveryMinimumNotMet, "minimum order amount for delivery is 25").With("minimum", minimum))
		issue.Minimum = &minimum
		if res.stop(in) {
			return res, nil
		}
	}

	// 4) Delivery fee and the address snapshot.
	if in.OrderType == orderDomain.OrderTypeDelivery {
		if err := p.priceDelivery(ctx, res, in); err != nil {
			return nil, err
		}
		if res.stop(in) {
			return res, nil
		}
	}
	goodsAndFee := res.Subtotal.Add(res.DeliveryFee)

	// 5) Pickup discount: 10 % of the discountable lines, only from a 20 EUR basket (goods + fee),
	// snapped to 0,10 EUR so every surface (cart, receipt, Mollie) shows a clean multiple of 10 cents.
	if in.OrderType == orderDomain.OrderTypePickUp && goodsAndFee.GreaterThanOrEqual(decimal.NewFromInt(pickupDiscountThresholdEUR)) {
		for _, raw := range res.RawItems {
			if prod := res.ProductsByID[raw.ProductID]; prod != nil && prod.IsDiscountable {
				res.PickupDiscount = res.PickupDiscount.Add(raw.TotalPrice.Mul(decimal.NewFromFloat(0.10)))
			}
		}
		res.PickupDiscount = money.RoundToNearest10Cents(res.PickupDiscount)
	}

	// 6) Coupon (stacks with the pickup discount). Validated against goods + delivery fee, WITHOUT
	// reserving it: createOrder reserves it afterwards.
	if in.CouponCode != nil && *in.CouponCode != "" {
		if err := p.priceCoupon(ctx, res, in, goodsAndFee); err != nil {
			return nil, err
		}
		if res.stop(in) {
			return res, nil
		}
	}

	// 7) Combined discounts never exceed the basket: scale both proportionally, then snap to 0,10 EUR.
	if totalDiscount := res.PickupDiscount.Add(res.CouponDiscount); totalDiscount.GreaterThan(goodsAndFee) {
		ratio := goodsAndFee.Div(totalDiscount)
		res.PickupDiscount = money.RoundToNearest10Cents(res.PickupDiscount.Mul(ratio))
		res.CouponDiscount = money.RoundToNearest10Cents(goodsAndFee.Sub(res.PickupDiscount))
	}

	// 8) The online fee and the total, added up by the same function the order repository stores.
	if in.IsOnlinePayment {
		res.OnlineFee = orderDomain.TransactionFee
	}
	res.Total = orderDomain.OrderTotal(res.Subtotal, res.DeliveryFee, res.PickupDiscount, res.CouponDiscount, res.OnlineFee)
	return res, nil
}

// choiceCache loads each choice / choice group list once per pricing run.
type choiceCache struct {
	products pricingProducts
	choices  map[uuid.UUID]*productDomain.ProductChoice
	groups   map[uuid.UUID][]*productDomain.ProductChoiceGroup
}

func newChoiceCache(products pricingProducts) *choiceCache {
	return &choiceCache{
		products: products,
		choices:  map[uuid.UUID]*productDomain.ProductChoice{},
		groups:   map[uuid.UUID][]*productDomain.ProductChoiceGroup{},
	}
}

func (c *choiceCache) choice(ctx context.Context, id uuid.UUID) (*productDomain.ProductChoice, error) {
	if choice, ok := c.choices[id]; ok {
		return choice, nil
	}
	choice, err := c.products.GetChoiceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	c.choices[id] = choice
	return choice, nil
}

func (c *choiceCache) groupsOf(ctx context.Context, productID uuid.UUID) ([]*productDomain.ProductChoiceGroup, error) {
	if groups, ok := c.groups[productID]; ok {
		return groups, nil
	}
	groups, err := c.products.GetChoiceGroupsByProductID(ctx, productID)
	if err != nil {
		return nil, err
	}
	c.groups[productID] = groups
	return groups, nil
}

// priceLine validates and prices item i. It reports at most one blocking problem per line (the first
// found) plus the lunch / price-changed notices that leave the line priced.
func (p *orderPricer) priceLine(
	ctx context.Context, res *pricingResult, line *pricedLine, index int, item pricingItem,
	cache *choiceCache, allowLunchOnly bool, serviceType productDomain.ServiceType,
) error {
	pid := item.ProductID
	prod, found := res.ProductsByID[pid]
	if !found {
		res.add(&pricingIssue{Line: index, Err: apperr.Newf(apperr.CodeProductNotFound, "product %s not found", pid).With("productId", pid.String())})
		return nil
	}
	label := pid.String()
	if prod.Name != "" {
		label = prod.Name
	}
	basePrice := prod.Price
	line.ProductPrice = &basePrice
	reject := func(err *apperr.Error) {
		res.add(&pricingIssue{Line: index, Err: err.With("productId", pid.String()), CurrentPrice: &basePrice})
	}

	if !prod.IsAvailable {
		reject(apperr.Newf(apperr.CodeProductUnavailable, "product %s is not available", label))
		return nil
	}
	qty := int64(item.Quantity)
	if qty <= 0 || qty > maxLineQty {
		reject(apperr.Newf(apperr.CodeInvalidQuantity, "invalid quantity for %s: must be between 1 and 99", label))
		return nil
	}

	selectionByChoice := make(map[uuid.UUID]int)
	selectionGroupByChoice := make(map[uuid.UUID]uuid.UUID)
	selectionCountByGroup := make(map[uuid.UUID]int)

	// addChoice resolves a choice and checks it belongs to the product (and, when given, the group).
	addChoice := func(choiceID uuid.UUID, groupID *uuid.UUID, quantity int) (rejected bool, err error) {
		choice, err := cache.choice(ctx, choiceID)
		if err != nil {
			// A choice deleted since the cart was saved is the customer's stale basket, anything else a fault.
			loadErr := choiceLoadError(err, choiceID, pid)
			if appErr, ok := apperr.From(loadErr); ok {
				reject(appErr)
				return true, nil
			}
			return false, loadErr
		}
		switch {
		case choice == nil:
			reject(apperr.Newf(apperr.CodeSelectionInvalid, "choice %s not found", choiceID))
			return true, nil
		case choice.ProductID != pid:
			reject(apperr.Newf(apperr.CodeSelectionInvalid, "choice %s does not belong to product %s", choiceID, label))
			return true, nil
		case groupID != nil && choice.ChoiceGroupID != *groupID:
			reject(apperr.Newf(apperr.CodeSelectionInvalid, "choice %s does not belong to group %s", choiceID, groupID))
			return true, nil
		}
		line.Modifiers[choice.ID] = choice.PriceModifier
		selectionByChoice[choice.ID] += quantity
		selectionGroupByChoice[choice.ID] = choice.ChoiceGroupID
		selectionCountByGroup[choice.ChoiceGroupID] += quantity
		return false, nil
	}

	if item.ChoiceID != nil {
		if rejected, err := addChoice(*item.ChoiceID, nil, legacyChoiceQuantity(qty)); rejected || err != nil {
			return err
		}
	}
	for _, selection := range item.Selections {
		if selection.Quantity <= 0 {
			reject(apperr.Newf(apperr.CodeSelectionInvalid, "selection quantity must be > 0 for product %s", label))
			return nil
		}
		if rejected, err := addChoice(selection.ChoiceID, &selection.GroupID, selection.Quantity); rejected || err != nil {
			return err
		}
	}

	groups, err := cache.groupsOf(ctx, pid)
	if err != nil {
		return fmt.Errorf("failed to load choice groups for product %s: %w", label, err)
	}
	for _, group := range groups {
		selectedCount := selectionCountByGroup[group.ID]
		minRequired := group.MinSelections * int(qty)
		maxAllowed := group.MaxSelections * int(qty)
		if selectedCount < minRequired || selectedCount > maxAllowed {
			reject(apperr.Newf(
				apperr.CodeSelectionInvalid,
				"invalid number of selections for group %s on product %s: expected between %d and %d, got %d",
				group.GetTranslationFor(utils.GetLang(ctx)), label, minRequired, maxAllowed, selectedCount,
			))
			return nil
		}
	}

	// Canonical selection order: by group, then choice (the same order the web stores them in).
	selections := make([]orderDomain.OrderProductSelection, 0, len(selectionByChoice))
	pricedSelections := make([]orderDomain.PricedSelection, 0, len(selectionByChoice))
	for choiceID, quantity := range selectionByChoice {
		selections = append(selections, orderDomain.OrderProductSelection{
			GroupID: selectionGroupByChoice[choiceID], ChoiceID: choiceID, Quantity: quantity,
		})
	}
	sort.Slice(selections, func(a, b int) bool {
		ga, gb := selections[a].GroupID.String(), selections[b].GroupID.String()
		if ga != gb {
			return ga < gb
		}
		return selections[a].ChoiceID.String() < selections[b].ChoiceID.String()
	})
	normalized := make([]pricingSelection, 0, len(selections))
	for _, s := range selections {
		pricedSelections = append(pricedSelections, orderDomain.PricedSelection{Modifier: line.Modifiers[s.ChoiceID], Quantity: s.Quantity})
		normalized = append(normalized, pricingSelection{GroupID: s.GroupID, ChoiceID: s.ChoiceID, Quantity: s.Quantity})
	}
	line.Selections = normalized

	// line total = base × qty + Σ(modifier × selection qty); see PriceLine for why the surcharge is
	// not multiplied by qty again and how unit_price is derived.
	lineTotal, unitPrice := orderDomain.PriceLine(basePrice, qty, pricedSelections)
	if lineTotal.LessThan(decimal.Zero) {
		reject(apperr.Newf(apperr.CodeInvalidPrice, "invalid price for product %s: price cannot be negative", label))
		return nil
	}
	line.LineTotal, line.UnitPrice, line.Priced = lineTotal, unitPrice, true

	// These two leave the line priced: the basket is still worth quoting.
	if !allowLunchOnly && prod.IsLunchOnly {
		reject(apperr.Newf(apperr.CodeLunchSlotRequired, "product %q is only available for a weekday lunch slot", label))
	}
	if item.ExpectedLineTotal != nil && !item.ExpectedLineTotal.Equal(lineTotal) {
		reject(apperr.Newf(apperr.CodePriceChanged, "the price of %s changed", label))
	}

	var choiceID *uuid.UUID
	if len(selections) == 1 && selections[0].Quantity == 1 {
		cid := selections[0].ChoiceID
		choiceID = &cid
	}
	res.Subtotal = res.Subtotal.Add(lineTotal)
	res.RawItems = append(res.RawItems, orderDomain.OrderProductRaw{
		ProductID:       pid,
		Quantity:        qty,
		UnitPrice:       unitPrice,
		TotalPrice:      lineTotal,
		VatRateApplied:  decimal.NewFromFloat(prod.VatCategory.VatRatePercent(serviceType)),
		ProductChoiceID: choiceID,
		Selections:      selections,
	})
	return nil
}

// priceDelivery resolves the address, enforces the zone and sets the fee and the address snapshot.
func (p *orderPricer) priceDelivery(ctx context.Context, res *pricingResult, in pricingInput) error {
	if in.AddressPlaceID == nil || *in.AddressPlaceID == "" {
		res.addOrder(apperr.New(apperr.CodeAddressRequired, "addressPlaceId required for delivery"))
		return nil
	}
	addr, err := p.addresses.Resolve(ctx, *in.AddressPlaceID, "")
	if err != nil {
		res.addOrder(apperr.Newf(apperr.CodeAddressUnresolvable, "failed to resolve address: %w", err))
		return nil
	}
	if addr.Distance >= deliveryMaxMeters {
		res.addOrder(apperr.New(apperr.CodeDeliveryOutOfZone, "address too far for delivery"))
		return nil
	}
	if isExcludedDeliveryPostcode(addr.Postcode) {
		res.addOrder(apperr.New(apperr.CodeDeliveryAreaExcluded, "address not eligible for delivery: excluded area"))
		return nil
	}
	res.DeliveryFee = deliveryFeeFromDistance(addr.Distance)
	res.Address = &orderDomain.AddressSnapshot{
		StreetName:       &addr.StreetName,
		HouseNumber:      &addr.HouseNumber,
		BoxNumber:        addr.BoxNumber,
		MunicipalityName: &addr.MunicipalityName,
		Postcode:         &addr.Postcode,
		Distance:         &addr.Distance,
		PlaceID:          &addr.PlaceID,
		Lat:              addr.Lat,
		Lng:              addr.Lng,
		IsManual:         false,
	}
	return nil
}

// priceCoupon validates the coupon and computes its discount; it never reserves it.
func (p *orderPricer) priceCoupon(ctx context.Context, res *pricingResult, in pricingInput, orderAmount decimal.Decimal) error {
	code := *in.CouponCode
	if in.UserID == nil {
		// validateCoupon is @auth, so the quote does not evaluate a coupon for an anonymous caller:
		// it is not an issue (the customer has to sign in to order anyway), just "not evaluated".
		unauthenticated := string(apperr.CodeUnauthenticated)
		res.Coupon = &couponOutcome{Code: code, Valid: false, ErrorCode: &unauthenticated}
		return nil
	}

	refuse := func(err *apperr.Error, minimum *string) {
		errCode := string(err.Code)
		res.Coupon = &couponOutcome{Code: code, Valid: false, ErrorCode: &errCode}
		res.addOrder(err).Minimum = minimum
	}

	coupon, discount, err := p.coupons.ValidateCoupon(ctx, code, orderAmount, *in.UserID)
	if err != nil {
		// A failure to run the check is ours, not the customer's: surface it as a server fault
		// (createOrder / quoteOrder return it as a GraphQL error), never as "invalid coupon".
		if checkErr, ok := couponCheckFailure(err); ok {
			return checkErr
		}
		appErr := apperr.Newf(couponErrorCode(err), "invalid coupon: %w", err)
		var minimum *string
		var minErr *couponDomain.MinOrderNotMetError
		if errors.As(err, &minErr) {
			required := minErr.Required.String()
			minimum = &required
			appErr = appErr.With("minimum", required)
		}
		refuse(appErr, minimum)
		return nil
	}
	// One coupon at a time: reject if the user already has another non-terminal order holding one.
	hasActive, err := p.orders.HasActiveCouponOrder(ctx, *in.UserID)
	if err != nil {
		return fmt.Errorf("failed to check active coupon orders: %w", err)
	}
	if hasActive {
		refuse(apperr.New(apperr.CodeCouponAlreadyActive, "you already have an active order using a coupon"), nil)
		return nil
	}
	res.CouponDiscount = money.RoundToNearest10Cents(discount)
	res.CouponCode = in.CouponCode
	res.CouponID = &coupon.ID
	res.Coupon = &couponOutcome{Code: code, Valid: true}
	return nil
}

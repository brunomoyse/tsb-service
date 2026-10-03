package upstream

// Documents is the complete list of GraphQL operations the MCP server can send
// upstream. Each one is named Mcp* so it is identifiable in tsb-service's
// staff_audit_log (operation_name). There is deliberately no generic "run any
// query" path: Client.Do only accepts names from this map.
//
// Order writes (createOrder, updateOrder, updatePaymentStatus) must never
// appear here; TestNoOrderWrites enforces it.
var Documents = map[string]string{
	// --- Catalogue reads ---------------------------------------------------
	"McpProducts": productFragment + `
query McpProducts {
  products { ...McpProductFields }
}`,
	"McpProduct": productFragment + `
query McpProduct($id: ID!) {
  product(id: $id) { ...McpProductFields }
}`,
	"McpCategories": `
query McpCategories {
  productCategories { id order slug name translations { language name } }
}`,

	// --- Restaurant ---------------------------------------------------------
	"McpRestaurantConfig": `
query McpRestaurantConfig {
  restaurantConfig {
    orderingEnabled openingHours orderingHours preparationMinutes
    isCurrentlyOpen isOrderingCurrentlyOpen nextOpeningAt updatedAt
  }
}`,
	"McpScheduleOverrides": `
query McpScheduleOverrides($from: DateTime!, $to: DateTime!) {
  scheduleOverrides(from: $from, to: $to) {
    date closed note updatedAt schedule { open close dinnerOpen dinnerClose }
  }
}`,
	"McpUpdateOrderingEnabled": `
mutation McpUpdateOrderingEnabled($enabled: Boolean!) {
  updateOrderingEnabled(enabled: $enabled) { orderingEnabled updatedAt }
}`,
	"McpUpdateOpeningHours": `
mutation McpUpdateOpeningHours($hours: OpeningHoursInput!) {
  updateOpeningHours(hours: $hours) { openingHours updatedAt }
}`,
	"McpUpdateOrderingHours": `
mutation McpUpdateOrderingHours($hours: OpeningHoursInput!) {
  updateOrderingHours(hours: $hours) { orderingHours updatedAt }
}`,
	"McpUpdatePreparationMinutes": `
mutation McpUpdatePreparationMinutes($minutes: Int!) {
  updatePreparationMinutes(minutes: $minutes) { preparationMinutes updatedAt }
}`,
	"McpUpsertScheduleOverride": `
mutation McpUpsertScheduleOverride($input: ScheduleOverrideInput!) {
  upsertScheduleOverride(input: $input) {
    date closed note updatedAt schedule { open close dinnerOpen dinnerClose }
  }
}`,
	"McpDeleteScheduleOverride": `
mutation McpDeleteScheduleOverride($date: DateTime!) {
  deleteScheduleOverride(date: $date)
}`,

	// --- Catalogue writes ---------------------------------------------------
	"McpUpdateProduct": productFragment + `
mutation McpUpdateProduct($id: ID!, $input: UpdateProductInput!) {
  updateProduct(id: $id, input: $input) { ...McpProductFields }
}`,
	"McpCreateProduct": productFragment + `
mutation McpCreateProduct($input: CreateProductInput!) {
  createProduct(input: $input) { ...McpProductFields }
}`,
	"McpCreateChoiceGroup": `
mutation McpCreateChoiceGroup($input: CreateProductChoiceGroupInput!) {
  createProductChoiceGroup(input: $input) { id productId minSelections maxSelections sortOrder name translations { locale name } }
}`,
	"McpUpdateChoiceGroup": `
mutation McpUpdateChoiceGroup($id: ID!, $input: UpdateProductChoiceGroupInput!) {
  updateProductChoiceGroup(id: $id, input: $input) { id productId minSelections maxSelections sortOrder name translations { locale name } }
}`,
	"McpDeleteChoiceGroup": `
mutation McpDeleteChoiceGroup($id: ID!) {
  deleteProductChoiceGroup(id: $id)
}`,
	"McpCreateChoice": `
mutation McpCreateChoice($input: CreateProductChoiceInput!) {
  createProductChoice(input: $input) { id productId choiceGroupId priceModifier sortOrder name translations { locale name } }
}`,
	"McpUpdateChoice": `
mutation McpUpdateChoice($id: ID!, $input: UpdateProductChoiceInput!) {
  updateProductChoice(id: $id, input: $input) { id productId choiceGroupId priceModifier sortOrder name translations { locale name } }
}`,
	"McpDeleteChoice": `
mutation McpDeleteChoice($id: ID!) {
  deleteProductChoice(id: $id)
}`,

	// --- Coupons ------------------------------------------------------------
	"McpCoupons": `
query McpCoupons {
  coupons { ` + couponFields + ` }
}`,
	"McpCoupon": `
query McpCoupon($id: ID!) {
  coupon(id: $id) { ` + couponFields + ` }
}`,
	"McpCreateCoupon": `
mutation McpCreateCoupon($input: CreateCouponInput!) {
  createCoupon(input: $input) { ` + couponFields + ` }
}`,
	"McpUpdateCoupon": `
mutation McpUpdateCoupon($id: ID!, $input: UpdateCouponInput!) {
  updateCoupon(id: $id, input: $input) { ` + couponFields + ` }
}`,

	// --- Orders (read only) -------------------------------------------------
	"McpOrders": `
query McpOrders {
  orders { ` + orderListFields + ` }
}`,
	"McpOrder": `
query McpOrder($id: ID!) {
  order(id: $id) {
    ` + orderListFields + `
    displayAddress addressExtra orderNote cancellationReason estimatedReadyTime
    customer { firstName lastName phoneNumber }
    items { quantity unitPrice totalPrice product { id name } choice { name } selections { quantity choice { name } } }
    statusHistory { status changedAt }
  }
}`,
	"McpOrderHistory": `
query McpOrderHistory($input: OrderHistoryInput) {
  orderHistory(input: $input) {
    summary { totalOrders totalRevenue averageOrder }
    orders { ` + orderListFields + ` }
  }
}`,
	"McpCustomerStats": `
query McpCustomerStats($input: CustomerStatsInput) {
  customerStats(input: $input) {
    summary { totalCustomers totalRevenue averageOrderValue totalOrders }
    customers {
      userId firstName lastName registeredAt totalOrders totalAmount averageOrderAmount
      firstOrderDate lastOrderDate preferredOrderType deliveryCount pickupCount
    }
  }
}`,
}

const productFragment = `
fragment McpProductFields on Product {
  id code slug name description price vatCategory pieceCount
  isAvailable isVisible isDiscountable isHalal isLunchOnly isSpicy isVegetarian
  category { id name translations { language name } }
  translations { language name description }
  choiceGroups {
    id minSelections maxSelections sortOrder name translations { locale name }
    choices { id choiceGroupId priceModifier sortOrder name translations { locale name } }
  }
}
`

const couponFields = `id code discountType discountValue minOrderAmount maxUses maxUsesPerUser usedCount isActive status validFrom validUntil createdAt`

const orderListFields = `id createdAt status type isOnlinePayment totalPrice discountAmount deliveryFee couponCode preferredReadyTime displayCustomerName payment { status } items { quantity }`

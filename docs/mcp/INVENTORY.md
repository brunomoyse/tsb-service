# TSB MCP server: dashboard action inventory

Step 1 of the MCP server work. This is an inventory of every user-facing action in `tsb-dashboard`, with the backend operation it calls, its side effects, a proposed risk class and a proposed MCP tool name. **No code is written until this inventory is reviewed.**

## Ground truth

- **Backend.** The backend is `tsb-service` (this repo), and the GraphQL endpoint is `POST /api/v1/graphql`.
  - `tsb-dashboard` is a static SPA (`ssr: false`). Its `server/` directory holds only a tsconfig, so there is no proxy: it calls this GraphQL API directly with a Zitadel bearer token (`tsb-dashboard/plugins/gqlFetch.ts`).
  - All routes below are therefore `POST /api/v1/graphql`, and the table names the GraphQL operation instead of the route.
  - The only extra REST calls are auth/MFA (`/auth/*`) and `POST /images/preview`.
- **Side effects on writes:**
  - pubsub broadcasts to live dashboards (`productUpdated`, `restaurantConfigUpdated`, `scheduleOverridesUpdated`, `couponUpdated`);
  - image uploads to the file service (`FILE_SERVICE_URL`);
  - for orders only: Mollie refunds, emails, APNs/FCM pushes and Live Activities.
  - **Every** admin/staff mutation is also appended to `staff_audit_log` by the GraphQL audit extension (`internal/shared/audit/audit.go`).
  - There is no HubRise sync, no Reverb and no cache layer.
- **Money.** Upstream uses decimal strings (`"12.5"`), backed by `numeric(10,2)` columns. The MCP exposes `price_cents` (integer) and converts at the boundary.
- **No introspection in prod.** The MCP hard-codes named documents (`query McpX` / `mutation McpX`) so they are identifiable in `staff_audit_log.operation_name`.

Directive legend: `public` (no auth), `@auth` (any user), `@admin` (admin role), `@staff` (admin or POS device).

## 1. Reads

| Dashboard screen | Action | Operation | Side effects | Risk | Proposed tool |
|---|---|---|---|---|---|
| /settings, header toggle (`composables/useOrderingStatus.ts`) | Ordering on/off, weekly opening + ordering hours, prep time, open now, next opening, slots today | `query restaurantConfig` (public) | none | read | `get_restaurant_config` (also feeds `get_status`) |
| /settings (`pages/settings.vue:660`) | Schedule overrides (closed days, special hours) | `query scheduleOverrides(from, to)` @admin | none | read | `list_schedule_overrides` |
| /products (`pages/products.vue:439`) | Products incl. hidden, choices, choice groups, translations | `query products` / `product(id)` (public) | none | read | `search_products`, `get_product` |
| /products (`pages/products.vue:482`) | Categories | `query productCategories` (public) | none | read | `list_categories` |
| /coupons (`pages/coupons.vue:389`) | Coupons | `query coupons` / `coupon(id)` @admin | none | read | `search_coupons`, `get_coupon` |
| /orders (`pages/orders.vue:1073`) | Live orders: latest 200, every status | `query orders` @staff | none | read | `list_orders` (recent) |
| /orders/[id] (`composables/useOrderActions.ts:38`) | Order detail incl. `statusHistory` | `query order(id)` @staff | none | read | `get_order` |
| /order-history (`pages/order-history.vue:406`) | History by date, status, type, search, with totals. Excludes CANCELLED/FAILED. | `query orderHistory(input)` @admin | none | read | `list_orders` (date ranges), `get_daily_summary` |
| /customers (`pages/customers.vue:446`) | Customer stats (names, phones, order counts) | `query customerStats(input)` @admin | none | read, **flag: PII** | `get_customer_stats`, aggregates only (proposed) |
| /customers (`pages/customers.vue:569`) | One customer's orders | `query customerOrders(userId, …)` @admin | none | read, **flag: PII** | excluded by default |
| /more (`pages/more.vue:68`) | Counts for badges | `customerStats` + `coupons` | none | read | covered by the tools above |

## 2. Writes in scope

| Dashboard screen | Action | Operation | Side effects | Risk | Proposed tool |
|---|---|---|---|---|---|
| /products inline toggle (`pages/products.vue:732`) | Mark available / sold out | `updateProduct(id, {isAvailable})` @admin | pubsub `productUpdated`, audit row | **low** | `set_product_availability` |
| /products inline toggle (`pages/products.vue:732`) | Show / hide on the menu | `updateProduct(id, {isVisible})` @admin | same. Making a product visible requires ≥3 translations (`internal/modules/product/domain/product.go:105`). | **low** | `set_product_visibility` |
| /products dialog (`components/ProductDialog.vue`) | Change price | `updateProduct(id, {price})` @admin | pubsub, audit | **sensitive** | `propose_price_change` |
| /products dialog | Edit name/description per language, category, code, piece count, halal/spicy/vegetarian/lunch-only/discountable flags | `updateProduct(id, {...})` @admin (field is `categoryID`, not `categoryId`) | pubsub, audit. A French name change regenerates the slug (public URL). | **sensitive** | `propose_product_update` |
| /products dialog | Change VAT category (`food`, `beverage`, `zero_rated`, `out_of_scope`) | `updateProduct(id, {vatCategory})` @admin | pubsub, audit. Changes the VAT applied to future orders. | **sensitive** (pending your confirmation) | `propose_vat_category_change` |
| /products dialog | Set / replace product image | multipart `updateProduct(id, {image, removeBackground})` @admin | Upload to the file service (`FILE_SERVICE_URL`). An upload failure is only logged upstream, so the MCP re-checks the result. | **sensitive** (pending your confirmation) | `propose_product_image` (takes an `image_url`) |
| /products dialog | Create product (without image) | `createProduct(input)` @admin | audit only, no pubsub (backend gap) | **sensitive** | `propose_product_creation` |
| /products dialog (`ProductDialog.vue:744-849`) | Create / update choice group (min/max selections, names, order) | `createProductChoiceGroup`, `updateProductChoiceGroup` @admin | DB + audit, no pubsub | **sensitive** | `propose_choice_group_change` |
| /products dialog | Create / update choice (price modifier, names, order) | `createProductChoice`, `updateProductChoice` @admin | DB + audit, no pubsub | **sensitive** | `propose_choice_change` |
| /products dialog (`ProductDialog.vue:696,721`) | Delete choice group / choice | `deleteProductChoiceGroup`, `deleteProductChoice` @admin | irreversible (ids lost; undo would recreate with new ids) | **sensitive** | `propose_choice_group_deletion`, `propose_choice_deletion` |
| /settings, /orders header (`composables/useOrderingStatus.ts:12`) | Pause / resume online ordering | `updateOrderingEnabled(enabled)` @admin | pubsub `restaurantConfigUpdated`, audit | **sensitive** | `propose_restaurant_closure`, `propose_restaurant_reopening` |
| /settings (`pages/settings.vue:678`) | Weekly opening hours. **Replaces the whole week.** | `updateOpeningHours(hours)` @admin | pubsub, audit | **sensitive** | `propose_opening_hours` |
| /settings (`pages/settings.vue:684`) | Weekly ordering hours. **Replaces the whole week.** | `updateOrderingHours(hours)` @admin | pubsub, audit | **sensitive** | `propose_ordering_hours` |
| /settings (`pages/settings.vue:690`) | Preparation minutes (1 to 240) | `updatePreparationMinutes(minutes)` @admin | pubsub, audit; shifts customer pickup/delivery slots | **low** | `set_preparation_minutes` |
| /settings (`pages/settings.vue:696`) | Add / edit a date override (closed all day, or special hours + note) | `upsertScheduleOverride(input)` @admin | pubsub ×2, audit. See the UTC pitfall in gap 5. | **sensitive** | `propose_schedule_override` |
| /settings (`pages/settings.vue:704`) | Delete a date override | `deleteScheduleOverride(date)` @admin | pubsub ×2, audit | **sensitive** | `propose_schedule_override_removal` |
| /coupons (`pages/coupons.vue:409`) | Create coupon | `createCoupon(input)` @staff | pubsub `couponUpdated`, audit | **sensitive** | `propose_coupon_creation` |
| /coupons (`pages/coupons.vue:429`) | Edit coupon (value, min amount, limits, validity) | `updateCoupon(id, input)` @admin | pubsub, audit. Nullable fields cannot be cleared (null is ignored). | **sensitive** | `propose_coupon_update` |
| /coupons inline toggle (`pages/coupons.vue:449`) | Deactivate coupon | `updateCoupon(id, {isActive: false})` @admin | pubsub, audit | **low** | `deactivate_coupon` |
| /coupons inline toggle | Activate coupon (gives discounts) | `updateCoupon(id, {isActive: true})` @admin | pubsub, audit | **sensitive** | `propose_coupon_activation` |
| (MCP only) | Undo the last change (< 30 min) | replays the before-state with the mutations above | as the original mutation | low or sensitive, per the original change | `undo_last_change` |
| (MCP only) | Status snapshot | reads above + local audit log | none | read | `get_status` |

## 3. Excluded

| Dashboard screen | Action | Operation | Why |
|---|---|---|---|
| /orders, /orders/[id] (`composables/useOrderActions.ts:17`) | Advance status, set ready time, mark failed, cancel | `updateOrder(id, input)` @staff | **Order write, always excluded.** Cancel triggers a full Mollie refund, emails, pushes and Live Activity updates. |
| /orders (`useOrderActions.ts:28`) | Mark as paid | `updatePaymentStatus(orderId, status)` @staff | Order/payment write |
| customer apps | Create order, quote order | `createOrder` @auth, `quoteOrder` | Order write / not a dashboard action |
| /orders | Print kitchen / client tickets | Sunmi native Capacitor plugin, no API | Not reachable from a server |
| /products dialog | Background-removal preview | REST `POST /images/preview` | Preview only. Use `remove_background` on `propose_product_image` instead. |
| app start (`composables/usePushNotifications.ts`) | Register / unregister push device | `registerDeviceToken`, `unregisterDeviceToken` @auth | Device/account |
| /settings > Security (`components/SecuritySettings.vue`) | TOTP status, enroll, verify, remove | REST `GET /auth/mfa`, `POST /auth/mfa/totp`, `/verify`, `/remove` | Credentials |
| /auth/login, /auth/callback | Login, OTP, TOTP, finalize, token exchange, `me` admin gate | REST `/auth/*`, `query me` | Credentials |
| (account, not in dashboard UI) | `updateMe`, `deleteMe`, `updateMyOrdersLanguage`, `registerLiveActivityToken` | GraphQL @auth | User account / customer-only. `deleteMe` deletes the Zitadel identity. |
| (customer only) | `myOrders`, `myOrder`, `validateCoupon`, `autocompleteAddresses`, `resolveAddress` | GraphQL | Customer-facing, not dashboard actions |
| (all) | Subscriptions `orderCreated`, `orderUpdated`, `productUpdated`, `couponUpdated`, `restaurantConfigUpdated`, `scheduleOverridesUpdated` | WebSocket | Not needed: the MCP re-reads state on demand |

## 4. Gaps: not cleanly possible through the API (decisions needed)

1. **Availability `until`: dropped (decided).** TSB has no notion of stock. The MCP only switches a product off or on when asked, never automatically. `set_product_availability` has no `until` parameter.
2. **Closure with `reopen_at`.** `orderingEnabled` has no reopen time, and `isCurrentlyOpen` / `nextOpeningAt` ignore it. Options:
   - **(a)** `updateOrderingEnabled(false)` plus an MCP-scheduled reopen. This is not visible in the dashboard as "reopens at X".
   - **(b)** A schedule override for the affected date(s): `closed: true` for whole days, or `closed: false` with the remaining hours for "close at 15:00, back at 18:00". This is native and visible in /settings, lapses by itself, and makes `nextOpeningAt` correct. It has day granularity, and the override replaces both opening and ordering hours for that day.
   - **(c)** Both. **Decided.** (a) for "pause now" with no reopen time; (b) when `reopen_at` (and an optional start time) is given. No scheduler.
3. **Not exposed upstream:**
   - product delete (hide instead);
   - coupon delete (deactivate instead);
   - category create/update/delete/reorder;
   - any bulk mutation. Bulk tools would loop over single mutations: always sensitive, and not atomic.
4. **Delivery settings are code constants** (`internal/modules/restaurant/domain/policy.go`): minimum order, radius, fees, pickup discount. Only `RESTAURANT_DELIVERY_ENABLED` is configurable, through the environment. There is no tool for them.
5. **Schedule override date pitfall.** The backend converts the `DateTime` to UTC before writing a `DATE` column. The dashboard sends local midnight, which lands on the previous UTC day. Before relying on either convention, the MCP will check how existing rows are stored and then send `YYYY-MM-DDT00:00:00Z`.
6. **Daily revenue.** There is no dedicated stats query. `orderHistory` for a single day gives `totalOrders` / `totalRevenue` with CANCELLED and FAILED excluded. **Decided:** PENDING orders do not count.
7. **Side observation, not in scope.** The dashboard `products` query does not select `choiceGroups`, but `components/ProductDialog.vue:587` builds the edit form from `product.choiceGroups`.

## 5. Service account (preview for step 2)

How the backend checks tokens:
- It validates JWT access tokens locally through JWKS (`internal/shared/middleware/oidc.go:113`). `aud` must contain `ZITADEL_CLIENT_ID`.
- The admin role is read from `urn:zitadel:iam:org:project:roles` (or the project-scoped claim with `ZITADEL_PROJECT_ID`).
- When `ZITADEL_ADMIN_CLIENT_IDS` is set, the token's `client_id`/`azp` must be in that list (`oidc.go:149-177`).
- TOTP is only enforced at `/auth/finalize` (interactive login), so it does not apply to machine tokens.

What the machine user needs:
- A Zitadel machine user `tsb-mcp` with the **access token type set to JWT** and a client secret, using the client credentials grant.
- A project grant of the `admin` role. There is no narrower role that can write the catalogue.
- Token scopes `openid urn:zitadel:iam:org:project:id:<projectId>:aud urn:zitadel:iam:org:projects:roles`.
- Its client id appended to `ZITADEL_ADMIN_CLIENT_IDS` in the tsb-service deployment.

**Blocker.** On first request, the backend JIT-provisions a `users` row with `email=''` (`internal/modules/user/application/service.go:157-243`). `email` is UNIQUE, so if any blank-email row already exists the insert fails and every MCP request returns 401. Even when the insert succeeds, a blank email makes the backend call the Zitadel user API on every request. Options:
- **(a)** A one-off data seed: insert the `users` row for the machine user's `zitadel_user_id` with a placeholder email such as `mcp-bot@tokyosushibarliege.invalid`.
- **(b)** A small backend change to handle machine users. **Decided: (b)**, done in this branch.

**Audit trail.** All MCP mutations will appear in `staff_audit_log` with `actor_kind = admin` and the machine user's id. That is in addition to the MCP's own SQLite audit log, which keeps the before/after state and `request_context`.

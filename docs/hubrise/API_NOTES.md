# HubRise API notes (from the official docs, read 2026-10-23)

Sources the owner pointed to:
- https://www.hubrise.com/developers/integration-guide
- https://www.hubrise.com/developers/api/general-concepts
Also read: /developers/api/callbacks, /developers/api/authentication, /developers/api/orders.

## Integration guide checklist (online ordering solution)

Connection
- Connect at the LOCATION level. Scope `location[orders.write,customer_list.write,catalog.read]`; add `inventory.read` for stock (catalog.read granting inventory is deprecated).
- One-click OAuth button in the back office (tsb-dashboard). After connecting, show the account name, location name + id, catalog name + id, and customer list name + id. Make disconnecting easy (it revokes the token).
- Register an ACTIVE callback with at least `"order": ["update"]`, plus `catalog: [update]` and `inventory: [update, patch]` for our scope.

IDs
- Store the HubRise order id and customer id in our DB and show them in the dashboard. Set `private_ref` = our order id / user id.

Customers
- On registration: `POST /customer_lists/:id/customers`. On profile update: `PATCH /customer_lists/:id/customers/:id`.
- Anonymise on request or after inactivity: `POST /customer_lists/:id/customers/:id/anonymise`. Hook this into our existing account deletion.
- Registered users: put `customer_id` on the order and PATCH the customer just before pushing it. Guests (if any): an inline `customer` object.

Pushing orders
- Push with `POST /location/orders` as soon as the order is created. ONLINE PAYMENT: wait for the Mollie payment to be confirmed before pushing.
- Every field carries names AND ref codes. Identical options repeat as separate option objects (quantity 1 each). Always send at least one payment. Times are in the store timezone (Europe/Brussels). The expected time must be in the future or null.
- Most EPOS systems ignore or reject items with no ref, so every product, option and charge needs a ref.
- Test orders go through OrderLine.

Order status
- Update our order when an `order.update` callback arrives.
- Alert when an order stays `new` for a fixed delay (about 5 min within business hours), to the restaurant and/or the customer.

Catalog
- Import from HubRise automatically (on the `catalog.update` callback) AND manually (a back-office button). A setting picks auto or manual.
- Save the ref codes of all products, options and charges, and send them back in orders.
- Images: max 1 MB, warn above 1000 px; JPEG, PNG, WEBP, GIF or BMP; reuse existing images to save API calls.

Assessment
- HubRise reviews the logs, then holds a call of about 90 min with an integration engineer and produces an Integration Sheet. Contact integration@hubrise.com. Test orders are free during development.

## General concepts
- Base `https://api.hubrise.com/v1`; header `X-Access-Token`; JSON.
- Pagination: `count` (max 100) + `cursor` taken from the `X-Cursor-Next` response header; stop when it is absent.
- Rate limits: 500/min, 2,500/hour, 10,000/day overall. `GET /catalogs/:id`, `GET /orders` and `GET /customer_lists/:id/customers` are limited to 10/min. Images: 3,000/min. Over the limit returns 429.
- Money is a string: "8.90 EUR". Decimals are strings ("1", "-2.5"): use shopspring/decimal, never float.
- Times are ISO 8601 in the location timezone. DOW is a 7-char string like "12----7".
- Errors: `{"error_type": ...}` with unauthorized, forbidden, not_found, unprocessable_entity, unsupported_media_type, too_many_requests, internal_error or routing_error. A 422 adds `message` + `errors[{field,message}]`. 304 Not Modified is not an error.
- `private_ref` (up to 255 chars) on orders, items and customers is visible only to our client and can be filtered on: `GET /location/orders?private_ref=...`. Use this to make order pushes idempotent: before retrying, look the order up by private_ref.

## Authentication
- Authorize at `https://manager.hubrise.com/oauth2/v1/authorize` (redirect_uri, client_id, scope, state), and exchange the token at `https://manager.hubrise.com/oauth2/v1/token`. The exchange runs server-side with HTTP Basic client_id:client_secret. The code is single-use and lives 10 min.
- The token response carries access_token, account_id, location_id, catalog_id, customer_list_id and the matching *_name fields.
- Tokens never expire (no refresh), but they can turn invalid and return 401: mark the connection as broken and alert.
- Revoke with `https://manager.hubrise.com/oauth2/v1/revoke` (server-side, Basic auth).
- One connection per location.

## Callbacks
- `POST /callback` {url, events} creates or replaces the single callback of the connection; `GET` / `DELETE /callback`. A client never receives events it generated itself.
- Events: catalog [create, delete, update], customer [create, update], customer_list, delivery/own_order_delivery, inventory [patch, update], location [update], order [create, update], own_order [update].
- Payload: id, resource_type, event_type, created_at, ids of the resource and its parents, previous_state/new_state (orders). Catalog events have no state. Inventory patch events have state_change.
- Signature: `X-HubRise-Hmac-SHA256` = hex HMAC-SHA256 of the RAW body, keyed with the CLIENT SECRET.
- Acknowledge with any 2xx-4xx within 20 s. A 5xx or a timeout makes HubRise retry 6 times (1 min doubling, capped at 32 min), then it drops the event. Unacknowledged events stay listed under `GET /callback/events`, acknowledged with `DELETE /callback/events/:id`. Passive polling: at most every 30 s. So: verify, persist, answer fast, process async, and keep a polling reconciler as a fallback.

## Orders
- Statuses: new, received, accepted, in_preparation, awaiting_collection (awaiting_shipment is deprecated, read it as awaiting_collection), in_delivery, completed, rejected, cancelled, delivery_failed.
- Create fields: status (required), private_ref, ref, channel, service_type (delivery|collection|eat_in), service_type_ref, expected_time, asap, confirmed_time, customer_notes, seller_notes, collection_code, coupon_codes[], custom_fields.
  - items[]: product_name, sku_name, sku_ref, price (unit, excluding options), quantity (decimal string), tax_rate, customer_notes, private_ref.
  - options[]: option_list_name, name, ref, price, quantity, removed.
  - deals, discounts[{name, ref, price_off}], charges[{name, ref, price, tax_rate}] (delivery fee, transaction fee), payments[{name, ref, amount, info}] summing to the total.
  - customer_id (or customer_list_id + customer_private_ref, or a guest customer object; never combined).
- Update with `PATCH /location/orders/:id`: status, confirmed_time, seller_notes, collection_code, private_ref, custom_fields. Collection elements cannot be modified, only deleted (id + deleted:true) and re-added.
- Prices may be tax-inclusive or tax-exclusive, set per HubRise ACCOUNT. Belgium = tax-inclusive; verify the setting rather than assume it.

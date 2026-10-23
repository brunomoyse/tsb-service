# HubRise integration: implementation plan (tsb-service)

Status: plan, nothing implemented yet. Branch `feat/hubrise` (fresh start, no reuse of the discarded attempt).
Scope of this workflow: `tsb-service` only. Client work (tsb-dashboard, tsb-core, tsb-mobile, tsb-pos-android, tsb-infra) is listed in section 15.

## 1. Goals and the one rule that governs everything

Features (all optional, all toggleable):

1. Push webshop orders (web and app) to HubRise.
2. Use a HubRise catalog as the menu source of truth (products, SKUs, option lists, prices, categories, SKU codes).
3. Order status changes made in HubRise flow back into tsb-service orders and the existing customer notifications and PubSub subscriptions.
4. HubRise inventory (sold out) drives product availability, and a manual sold-out toggle in the dashboard flows back to HubRise.
5. STANDALONE MODE: a restaurant that does not use HubRise behaves exactly as today.

The rule: **HubRise is an additive layer, never in the critical path.** Placing an order, paying, serving the menu and the dashboard must work identically when HubRise is unconfigured, disabled, unreachable or returning errors. The webshop reads only local tables; HubRise data is copied in by background jobs, and local events are pushed out through a durable outbox.

## 2. What the exploration found (facts the design depends on)

Backend facts (tsb-service at 554ccfa):

- **Catalog model.** `products` (price numeric(10,2), `code`, `slug` NOT NULL-ish unique, flags, `vat_category`, `is_visible`, `is_available`), `product_categories` (`order`, unique NOT NULL `slug`), translation tables with `language` CHECK in (en, fr, nl, zh). Choices are **per product**: `product_choice_groups.product_id`, `product_choices.product_id + choice_group_id`, with `min_selections >= 0`, `max_selections >= 1`, `min <= max`, `price_modifier >= 0` (CHECK). Orders reference them through `order_product_choices` (FKs, no cascade on the choice side).
- **Order history is fragile.** `order_product.product_id` is `ON DELETE CASCADE`. Deleting a product destroys order lines. The integration must NEVER delete products: it retires them (hidden and unavailable).
- **`order_product` does not snapshot** product name, code or base price. `unit_price` is an average per unit that includes option surcharges. HubRise needs the base SKU price and the options separately, so the push payload is built when the order becomes actionable and frozen in the outbox row.
- **Selection quantities** in `order_product_choices.quantity` are totals for the whole line (group min/max are multiplied by the line quantity, see `domain.PriceLine`). HubRise option quantity is per single item unit. Lines whose selections are not divisible by the quantity must be split per unit (section 7).
- **Order actionable moments.** Cash orders: end of `CreateOrder` resolver (where `SendNewOrderPush` is called). Online orders: Mollie webhook paid branch (`payment/interfaces/handler.go`). These are exactly the moments to enqueue the HubRise push. Online orders must not be pushed before payment (HubRise integration guide says the same).
- **The status-change orchestration lives inside the GraphQL resolver** `mutationResolver.UpdateOrder` (about 400 lines): payment settlement and Mollie refund on cancel (`cancelOrder`, advisory lock), no reopening of refunded orders, confirmation / ready / cancelled / refund emails, APNs + FCM alerts, Live Activity and Android live update, "late notification" suppression, `orderUpdated` PubSub topics. A HubRise status callback must reuse all of it, so it has to be extracted into a reusable method (task T5). This is the riskiest refactor in the plan; the existing test suite is the safety net.
- **Dual DB pool.** `DBPool.ForContext(ctx)` returns the Admin pool for staff contexts (`utils.SetIsAdmin`). Background jobs and webhooks must run with an admin context, or the HubRise repositories take `pool.Admin` directly (as `audit.NewRecorder(dbPool.Admin)` does). New tables get no per-role GRANTs in this repo (no migration contains GRANT), so the Admin pool is the only one that touches them.
- **Production runs 2 replicas** (k3s, `replicas: 2`). Every worker must be safe to run twice: `FOR UPDATE SKIP LOCKED` claims and `pg_try_advisory_lock` for singleton jobs.
- **Go 1.27 pitfall** (memory note): a nil `json.RawMessage` is written as SQL NULL and cannot be scanned back. All new jsonb columns are `NOT NULL DEFAULT '{}'` and written from non-nil values.
- **Tests** use a shared PostgreSQL container per package (`internal/api/graphql/testhelpers`, dockertest, goose migrations applied once into a template database). CI gate: 98 % statement coverage (`scripts/coverage-check.sh`, excludes generated code, testhelpers, fakeupstream, cmd/). Local runs use `CGO_ENABLED=0`.
- `product/domain/vat.go` only mentions HubRise in a comment on `ServiceType` (mirror of HubRise service types). No other existing code to reuse.
- `restaurant_config` is a single-row table (`id BOOLEAN PRIMARY KEY CHECK (id)`): one installation = one deployment = one database. White-label brands (tokyosushi, ygfliege) are separate deployments, so "per installation" equals "single row".

HubRise facts (read from the official docs at www.hubrise.com/developers, and from `moyun-pos/crates/pos-hubrise*`):

- Base URL `https://api.hubrise.com/v1`, header `X-Access-Token`. OAuth at `https://manager.hubrise.com/oauth2/v1` (`/authorize`, `/token` with HTTP Basic client auth and a form body `code=`, `/revoke`). Authorization codes are one-use and expire after 10 minutes. **Access tokens do not expire** and have no refresh; a 401 means revoked, blocked or unpaid, and the connection must be flagged and the user told to reconnect.
- Scopes: `location[orders.write,catalog.read,inventory.read,inventory.write]`. (`catalog.*` also grants inventory, deprecated, so request `inventory.*` explicitly.)
- Active callbacks: HubRise POSTs the event JSON to our URL with header `X-HubRise-Hmac-SHA256` = hex HMAC-SHA256 of the **raw body** keyed with the **client secret**. We must answer 200 to 499 within 20 s; otherwise HubRise retries up to 6 times with 1 min doubling to 32 min, then deletes the event. Unacknowledged events stay readable at `GET /callback/events`. A client **never receives events it generated itself**. One callback per connection (`POST /callback` replaces).
- Event envelope: `id, resource_type, event_type, created_at, <resource ids>, previous_state, new_state`. Order create/update events carry `new_state`. **Catalog events carry no state** (re-fetch). Inventory events carry state only for `patch` (`state_change`).
- Orders: `POST /location/orders` (almost everything optional). Statuses: `new, received, accepted, in_preparation, awaiting_collection, in_delivery, completed` plus `rejected, cancelled, delivery_failed`; `awaiting_shipment` is a deprecated alias of `awaiting_collection`. `private_ref` is visible only to the client that set it (usable as an idempotency key: `GET /location/orders?private_ref=`). Payments omitted means unpaid. Prices are `"8.90 EUR"`, quantities decimal strings. HubRise does not compute tax.
- Catalog: `GET /catalogs/:id` returns everything (variants, categories tree, products with `skus`, `option_lists` with `options`, deals, discounts, charges). `GET /catalogs/:id` and order list reads are limited to **10 requests per minute** on top of 500/min, 2,500/h, 10,000/day. SKU `ref` is what orders carry (EPOS ignore or reject items without refs). Option lists: `min_selections` (default 0), `max_selections` (null = unlimited), `multiple_selection`. Restrictions: `enabled`, `variant_refs`, `dow`, time and date windows. `price_overrides` by variant and conditions (last matching rule wins). Product `tax_rate` per service type (`delivery`, `collection`, `eat_in`). `custom_fields` on SKUs, `tags` on categories, products, SKUs, options.
- Inventory: `GET/PUT/PATCH /location/inventory` (unscoped, recommended), entries `{sku_ref|option_ref, stock, expires_at}`; `stock "0"` is sold out, `expires_at` only with stock 0, `PATCH` with `stock: null` removes the entry (unlimited). Expiry of an entry triggers an `inventory.patch` callback.
- Conventions already in use by the owner's POS (moyun-pos), which our importer must understand: SKU `custom_fields.code` (menu code such as "A2"), `custom_fields.vat_category` (food, beverage, zero_rated, out_of_scope), `custom_fields.translations.{fr,en,nl,zh}.{name,description}`, category `description` as JSON `{"translations":{"fr":..,"en":..,"nl":..,"zh":..}}`, one POS item per SKU, option keys `sku|list|option`, channel key lower-cased (`webshop` when absent) matched against a `hubrise_channels` table, cash payment refs table to decide prepaid versus unpaid.

## 3. Architecture

### 3.1 Module layout

```
pkg/hubrise/                       HTTP client + API types (like pkg/apns, pkg/fcm)
  client.go  types.go  money.go  oauth.go  errors.go  ratelimit.go
  hubrisetest/                     Go mock HubRise server (httptest) + fixtures (like pkg/apns/apnstest)
internal/modules/hubrise/
  domain/          Connection, CatalogLink, OutboxItem, Event, status mapping, neutral Catalog, repository ports
  application/     ConnectionService (oauth, settings, disconnect), CatalogImporter (plan/apply),
                   InventoryService, OrderPusher (payload builder + enqueue), OutboxWorker,
                   EventProcessor, StatusSync, Reconciler, Hooks (consumer-facing facade), ports
  infrastructure/  sqlx repositories (Admin pool), tokencrypt (AES-256-GCM), clock
  interfaces/      gin handlers: POST /hubrise/callback, GET /hubrise/oauth/callback
cmd/app/hubrise.go                 composition root helper (initHubrise), excluded from coverage like all of cmd/
```

Dependency rule as everywhere: interfaces -> application -> domain <- infrastructure. The hubrise module does **not** import other business modules directly. It declares ports (`OrderReader`, `UserReader`, `CatalogWriter`, `OrderTransitioner`, `ProductGuardTarget`...) implemented by thin adapters in `cmd/app/hubrise.go` or in the resolver package, which is where cross-module orchestration already lives (compare `NewOrderNotifier` in payment/interfaces).

### 3.2 Hooks: how core code calls into HubRise with zero cost when absent

The resolver package gets one optional field `Resolver.Hubrise HubriseHooks` (nil in standalone). Consumer-defined interface, all methods nil-safe through tiny helpers:

| Hook | Called from | Effect |
|---|---|---|
| `OrderReady(ctx, orderID)` | `CreateOrder` (cash orders) and the Mollie paid branch | enqueue `order_create` |
| `OrderStatusChanged(ctx, orderID, newStatus, source)` | the extracted transition method, only when `source != hubrise` | enqueue `order_status` |
| `AvailabilityChanged(ctx, productID, available)` | `UpdateProduct` / MCP path when `isAvailable` changed | enqueue `inventory` |
| `ProductGuard.CheckUpdate(ctx, old, new)` | product service `UpdateProduct`, choice and group mutations | reject edits of HubRise-owned fields |

When the installation is not configured, `Hubrise` is nil and no HubRise code runs. When it is configured but the runtime switch is off, every hook returns after one cached boolean check (15 s TTL cache of `hubrise_connection.enabled`, refreshed on settings change; at most 15 s of lag across replicas is accepted).

### 3.3 Two-level opt-in (standalone switch)

1. **Deployment level.** HubRise is *available* only when `HUBRISE_CLIENT_ID`, `HUBRISE_CLIENT_SECRET` and `HUBRISE_TOKEN_KEY` are all set. Otherwise: module not constructed, routes not registered, workers not started, GraphQL admin API answers `available: false`. A deployment that never sets them (every existing one, on day one) is byte-for-byte unchanged apart from additive migrations.
2. **Runtime level.** `hubrise_connection` single row. Not connected = standalone. Connected with `enabled = false` = paused, nothing leaves, nothing is imported, locks are released, links and the outbox are kept. Four feature toggles on top: `push_orders`, `sync_status`, `sync_inventory`, `sync_catalog` (catalog mode `hubrise`).

Toggling is lossless: disabling never deletes links or products; re-enabling re-adopts the same rows. "Disconnect" revokes the token at HubRise, deletes the callback, wipes the encrypted token, keeps links unless `forgetLinks: true` (products stay either way, they simply become ordinary local products).

### 3.4 Workers (all in-process, started from main when available)

| Worker | Cadence | What it does |
|---|---|---|
| Outbox drain | wake-up channel + 5 s tick | claims due rows (`FOR UPDATE SKIP LOCKED`, per-order FIFO), sends them, backoff 30 s doubling to 30 min, parks permanent refusals |
| Event processor | wake-up channel + 5 s tick | claims `pending` callback events, applies them idempotently |
| Reconciler | 2 min, advisory-locked | (a) enqueue orders that became actionable but have no `hubrise_orders` row (crash between commit and enqueue), (b) refresh status of open pushed orders older than 10 min, (c) flag orders still `new` at HubRise after 5 min, (d) purge old events |
| Catalog drift | 6 h and on `catalog` events (debounced 30 s) | fetch catalog, compare content hash, import if changed |
| Inventory full sync | 30 min and on `inventory` events | `GET /location/inventory`, apply |
| Callback poller | 10 min | `GET /callback/events` to pick up events HubRise could not deliver, then ack |
| Sold-out expiry sweeper | 1 min, advisory-locked | stock-0 entries whose `expires_at` passed become available again |

All workers use an admin context and stop on shutdown (same pattern as the existing purge and sweeper goroutines in `main.go`).

## 4. Database changes (all additive, expand only)

Principles: no table rewrite, no long lock, no data destroyed, every migration has a working `Down`. Each migration starts with `SET LOCAL lock_timeout = '5s'` so a busy table makes the migration fail fast and retry instead of queueing writers behind an ACCESS EXCLUSIVE lock. `ADD COLUMN ... NOT NULL DEFAULT <constant>` is metadata-only on PostgreSQL 11+ (prod is far above). CHECK constraints on existing tables are added `NOT VALID` then `VALIDATE CONSTRAINT` (validation takes only SHARE UPDATE EXCLUSIVE). No `CREATE INDEX` on big existing tables. `goose` timestamps follow the latest (`20261018120000`).

Considered and **rejected** restructurings (and why):

- *Shared option lists as a first-class table.* HubRise option lists are shared across SKUs; ours are per product (`product_choice_groups.product_id`). Making them shared would change the GraphQL shape (`ProductChoiceGroup.productId`, `choices`), the order FKs, the dashboard editor and the apps already in the stores. Instead the importer **materializes one local group per (SKU, option list)**, linked by key `sku|list`. Cost: duplication of rows. Benefit: zero client change.
- *Splitting product and SKU.* Local products have a single price; a HubRise product with several SKUs becomes several local products named "Product - SKU" (same as moyun-pos, one item per SKU).
- *Unique constraint on `products.code`* or making `code` mandatory: not needed.
- *Moving HubRise columns onto core tables* (`products.hubrise_ref`...): rejected in favour of one link table so core tables stay HubRise-free and standalone installs carry nothing.

### Migration M1 `20261026120000_create_hubrise_connection.sql`

- `hubrise_connection`: `id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id)` (single row, same idiom as `restaurant_config`), `enabled BOOLEAN NOT NULL DEFAULT FALSE`, `access_token_enc BYTEA NOT NULL` (AES-256-GCM, 1 version byte + 12 byte nonce + ciphertext), `account_id, account_name, location_id, location_name, catalog_id, catalog_name, customer_list_id TEXT`, `scope TEXT NOT NULL`, feature toggles `push_orders, sync_status, sync_inventory, sync_catalog BOOLEAN NOT NULL DEFAULT TRUE/TRUE/TRUE/FALSE`, `catalog_language TEXT NOT NULL DEFAULT 'fr'` (CHECK in fr,en,nl,zh), `variant_ref TEXT`, `channel_name TEXT`, `import_images BOOLEAN NOT NULL DEFAULT FALSE`, `catalog_hash TEXT`, `last_catalog_sync_at, last_event_at, auth_error_at TIMESTAMPTZ`, `last_error TEXT`, `callback_registered_at`, `created_at, updated_at`.
- `hubrise_oauth_states (state TEXT PRIMARY KEY, admin_user_id UUID NOT NULL, created_at, expires_at)` for the OAuth `state` nonce (one-time, 10 min).
- Backfill: none. Table starts empty; absence of a row = not connected = standalone.
- Down: drop both tables.

### Migration M2 `20261026130000_create_hubrise_catalog_links.sql`

`hubrise_catalog_links`: `id BIGSERIAL PK`, `entity_type TEXT CHECK in (category, product, choice_group, choice)`, four nullable FK columns `category_id -> product_categories`, `product_id -> products`, `choice_group_id -> product_choice_groups`, `choice_id -> product_choices`, **all `ON DELETE CASCADE`** (deleting a choice in the dashboard cleans its link), a CHECK that exactly the column matching `entity_type` is non-null, `hubrise_key TEXT NOT NULL` (category: ref; product: SKU ref; group: `skuRef|listRef`; choice: `skuRef|listRef|optionRef`), `hubrise_ref TEXT NOT NULL` (last segment; the option ref is what inventory entries name), `hubrise_id TEXT`, `content_hash TEXT NOT NULL DEFAULT ''`, `origin TEXT CHECK in (import, adopted, manual)`, `stock NUMERIC(12,3)`, `sold_out_until TIMESTAMPTZ`, `inventory_synced_at`, `image_md5 TEXT`, `retired_at TIMESTAMPTZ`, timestamps. Partial unique indexes per FK column (one link per local row) and `UNIQUE (entity_type, hubrise_key)`; index on `(entity_type, hubrise_ref)` for inventory lookups.
Backfill: none (no links until the owner runs adoption). Down: drop table.

### Migration M3 `20261026140000_add_translation_source.sql`

`source TEXT NOT NULL DEFAULT 'manual'` plus `CHECK (source IN ('manual','hubrise')) NOT VALID` then `VALIDATE`, on `product_translations`, `product_category_translations`, `product_choice_translations`, `product_choice_group_translations`.
Backfill: the default **is** the backfill: every existing row is correctly `manual` (nothing ever came from HubRise). Down: drop the columns (loses provenance only).
Code change that goes with it: the dashboard's translation upsert must keep `source = 'manual'` only when the text actually changed:
`ON CONFLICT ... DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description, source = CASE WHEN (t.name, t.description) IS DISTINCT FROM (EXCLUDED.name, EXCLUDED.description) THEN 'manual' ELSE t.source END`.
The dashboard sends all languages on every product save; without the `IS DISTINCT FROM` rule a price edit would turn every HubRise name into a manual override.

### Migration M4 `20261026150000_add_choice_is_available.sql`

`product_choices.is_available BOOLEAN NOT NULL DEFAULT TRUE`. Backfill: default. Down: drop column. Needed because HubRise inventory can sell out an *option* (egg, extra sauce), and choices had no availability.

### Migration M5 `20261026160000_add_orders_channel.sql`

`orders.channel TEXT NULL` with `CHECK (channel IN ('web','app')) NOT VALID` + `VALIDATE`. Set at creation from the existing `couponChannel(ctx)` (app when the access token belongs to the native client). Backfill: none; `NULL` is read as `web`. Used as the HubRise `channel` (EPOS side such as moyun-pos maps channels to payment types) and for reporting. Down: drop column.

### Migration M6 `20261026170000_create_hubrise_orders_outbox_events.sql`

- `hubrise_orders`: `order_id UUID PRIMARY KEY REFERENCES orders ON DELETE CASCADE`, `hubrise_order_id TEXT` (unique where not null), `push_state TEXT CHECK in (pending, sent, parked, skipped)`, `pushed_at`, `last_remote_status TEXT`, `last_event_at TIMESTAMPTZ` (ordering guard for inbound events), `remote_total NUMERIC(10,2)`, `needs_attention TEXT`, timestamps.
- `hubrise_outbox`: `id BIGSERIAL PK`, `kind TEXT CHECK in (order_create, order_status, inventory)`, `order_id UUID REFERENCES orders ON DELETE CASCADE NULL`, `dedupe_key TEXT NOT NULL`, `payload JSONB NOT NULL DEFAULT '{}'`, `state TEXT CHECK in (pending, sent, parked) DEFAULT pending`, `attempts INT`, `next_attempt_at TIMESTAMPTZ DEFAULT now()`, `last_error TEXT`, `created_at, sent_at, parked_at`. Partial unique index `(dedupe_key) WHERE state = 'pending'` (inventory rows collapse to the latest state; order rows are idempotent), index `(state, next_attempt_at)`.
- `hubrise_events`: `id TEXT PRIMARY KEY` (HubRise event id), `resource_type, event_type TEXT`, `payload JSONB NOT NULL`, `received_at`, `state TEXT CHECK in (pending, done, failed, ignored)`, `attempts INT`, `next_attempt_at`, `processed_at`, `last_error`.
- Backfill: none. Down: drop the three tables.

Net effect on existing production tables: 6 additive columns (4 translation `source`, `product_choices.is_available`, `orders.channel`), no data change.

## 5. Catalog: HubRise as source of truth

### 5.1 Ownership model

For a product that has a link row *and* `sync_catalog` on *and* `enabled`:

| Field | Owner | Dashboard edit |
|---|---|---|
| price, category, vat_category, code (when HubRise has one), choice groups and choices (names, prices, min/max) | HubRise | rejected with `HUBRISE_MANAGED` |
| name and description in `catalog_language` | HubRise unless overridden | allowed; the edit flips `source` to `manual` and survives re-imports |
| translations in the other languages | local (HubRise is mono-language) | allowed, never touched by the importer |
| is_halal, is_vegetarian, is_spicy, is_lunch_only, is_discountable, piece_count, image | local | allowed (tags only seed them at creation) |
| is_visible | HubRise sets it at creation / on enable-disable; local toggle allowed | allowed |
| is_available | inventory, two-way (section 8) | allowed, pushed to HubRise |

With the integration off, `sync_catalog` off or disconnected, **nothing is locked**: the guard is a no-op and managed products become plain local products again.

### 5.2 Translations survive re-imports

HubRise names live in `catalog_language` (default `fr`, the authoring language). The importer upserts a translation row only if it is missing or `source = 'hubrise'`; a `manual` row is never overwritten. Languages not present in HubRise are never written. If the HubRise data uses the owner's existing conventions (`custom_fields.translations`, category `description` JSON), those languages are written with `source = 'hubrise'` too. Products with a single translation stay valid: the read path already falls back through `translationFallbackOrder` (fr first). The domain rule "visible product needs 3 translations" lives in `NewProduct` only; the importer writes through its own repository method and does not call it, so imported products are visible with one language.

### 5.3 Mapping rules

- **Category**: every HubRise category that directly holds products becomes a local category (the tree is flattened, parents without products are ignored). Name from HubRise in `catalog_language`; `order` from depth-first catalog order; **slug generated once** (gosimple/slug of the name, `-2` suffix on collision) and never regenerated, because slugs are public URLs (`productCategoryBySlug`).
- **Product**: one local product per SKU. Name = product name, plus " " and the SKU name when the product has several SKUs or the SKU is named. Price = SKU price, after `price_overrides` for the configured `variant_ref` (last matching rule wins; rules with time or date windows are ignored in v1 and reported). Product slug uses the existing rule (category fr name + product fr name) through a shared function; `ErrDuplicateProductName` is resolved with a numeric suffix.
- **Code**: SKU `custom_fields.code` (kept only if present; an existing local code is never blanked).
- **VAT**: `custom_fields.vat_category` wins. Otherwise from `tax_rate`: collection and eat_in `6/12` -> food, `21/21` -> beverage, all `0` -> zero_rated. Anything else is **unmappable**: the product is created with `is_visible = false` and listed in the import report as `VAT_UNMAPPED`, because VAT drives invoices and fiscal rules.
- **Visibility**: SKU `restrictions.enabled = false`, tag `hidden` or tag `web:hidden` -> `is_visible = false` (on create and on change of the HubRise side). Time windows, day-of-week and per-customer restrictions are ignored in v1 (reported).
- **Option lists**: for each (SKU, option list) a local group keyed `sku|list`. `min_selections` from HubRise, `max_selections` null -> number of options (or 99 when `multiple_selection`), 0 -> the same. Options -> choices keyed `sku|list|option`, `price_modifier = max(price, 0)` (local CHECK forbids negatives; a negative HubRise price is reported), `sort_order` from list order. Option `restrictions.enabled = false` -> `is_available = false`.
- **Deals, discounts, charges, nutrition, barcodes**: not imported in v1. Charge refs for delivery and online fee are configurable strings used by the order push.
- **Removed from HubRise**: link gets `retired_at`, product becomes `is_visible = false, is_available = false`. Never deleted. If the SKU reappears, the same product is revived.
- **Images** (opt-in `import_images`): download `GET /catalogs/:id/images/:id/data`, upload through `utils.UploadProductImage` keyed by product UUID, only when the MD5 differs from `image_md5`. Failures are non-fatal.

### 5.4 Import is a plan, then an apply (adoption)

Production already has a populated menu. Turning on catalog sync must not duplicate it. The importer therefore has two phases and three modes:

- `Plan(catalog) -> Diff`: pure function over (HubRise catalog, local state, links). Lists `create`, `update`, `link` (adoption), `retire`, `warnings`, `conflicts`. No writes.
- `Apply(diff)`: one transaction per category-batch, under `pg_advisory_xact_lock`, idempotent (content hash per link; unchanged items cost no write).
- Modes: `LINK_ONLY` (match and create link rows only; used to give an existing menu its SKU refs for order pushing without touching any product data), `FULL` (adopt, create, update, retire), `DRY_RUN` (return the Diff).
- Adoption matching order: explicit manual link; `custom_fields.code` equal to `products.code`; normalized fr name within the same-named category. Anything ambiguous or unmatched is reported, never guessed.

Publishing: each changed product emits the existing `productUpdated` PubSub event so dashboards refresh.

## 6. Pushing orders

### 6.1 Flow

`OrderReady` -> build payload (frozen) -> `order_create` outbox row (+ `hubrise_orders` row `pending`) -> drain worker POST -> `hubrise_order_id` stored, `private_ref` = local order UUID.

Idempotency: if an attempt did not get an answer (timeout), the retry first calls `GET /location/orders?private_ref=<uuid>`; if the order exists it is marked sent. Retries run for 48 h (by age, with the same backoff) and are then parked and flagged. 401 pauses the whole pipeline (rows stay pending, connection gets `auth_error_at`), 400/403/404/422 park the row with the HubRise message, 429 and 5xx retry.

Catch-up guarantee: the enqueue happens after the order commit, so a crash between the two would lose the push. The reconciler enqueues any actionable order (not pending payment, not test, not failed, created after the connection) without a `hubrise_orders` row.

### 6.2 Payload mapping

| HubRise field | Value |
|---|---|
| `private_ref` | order UUID |
| `ref` | short human reference (first 8 chars of the UUID, uppercase) |
| `channel` | `connection.channel_name`, else `orders.channel` (`web`/`app`), else `webshop` |
| `status` | `new` |
| `service_type` | PICKUP -> `collection`, DELIVERY -> `delivery` |
| `expected_time`, `asap` | `preferred_ready_time` (Europe/Brussels offset) else `asap: true` |
| `customer` (guest order) | first/last name, email, phone from the user; for delivery address_1 (street + number + box), postal_code, city, country `BE`, lat/lng, `delivery_notes` = address_extra. Customer lists are not used in v1 (GDPR surface, open question 3) |
| `customer_notes` | order note + order extras (chopsticks, sauces), cash amount to prepare change for |
| `coupon_codes` | coupon code |
| `items[]` | `product_name` (fr name), `sku_name`, `sku_ref` (link; item still sent without a ref if unlinked, and the order is flagged `needs_attention = UNLINKED_ITEM`), `price` = **base** SKU price, `quantity`, `tax_rate` = stored `vat_rate_applied`, `options[]` |
| `charges[]` | delivery fee (`ref` = configured delivery charge ref), online payment fee (`PAYMENT_FEE`) |
| `discounts[]` | pickup discount (`TAKEAWAY`), coupon discount (coupon code) |
| `payments[]` | online: one entry `{name: "Mollie", ref: "MOLLIE", amount: total, info: {mollie_payment_id}}`; cash: none (unpaid), so the EPOS collects |
| rounding | the local total is rounded to 0.10 EUR; a `ROUNDING` charge or discount closes the gap so HubRise's computed `total` equals the local `total_price` exactly (asserted in tests, and compared with the response's `total`, a mismatch sets `needs_attention = TOTAL_MISMATCH`) |

Options: base price is recomputed as `(line total - sum(modifier x selection qty)) / qty` only as a cross check; the payload uses today's product base price and choice modifiers read at enqueue time (negative modifiers clamped to 0, as `PriceLine` does). Selection quantities are totals for the line: if every selection quantity is divisible by the line quantity, send `quantity = q` and per-unit option quantities; otherwise split the line into `q` single-unit items with the selections distributed as evenly as possible (totals stay exact). This is documented as an approximation for the kitchen ticket in tests.

HubRise-side edits to items of an order (EPOS deleting or adding items) are **not** applied in v1; the remote `total` is stored in `hubrise_orders.remote_total` and a difference sets `needs_attention`.

## 7. Status back to tsb-service

### 7.1 Mapping

| HubRise | Local effect |
|---|---|
| `new`, `received` | none (store `last_remote_status`) |
| `accepted` | CONFIRMED |
| `in_preparation` | PREPARING |
| `awaiting_collection` / `awaiting_shipment` | PICKUP: AWAITING_PICK_UP. DELIVERY: no change (a "ready for pickup" email would be wrong for a delivery) |
| `in_delivery` | OUT_FOR_DELIVERY |
| `completed` | PICKUP: PICKED_UP, DELIVERY: DELIVERED |
| `cancelled`, `rejected` | CANCELLED (reason `OTHER`) through the full cancel path: Mollie refund / payment cancel under the payment advisory lock, refund email, coupon rollback |
| `delivery_failed` | no automatic change; `needs_attention = DELIVERY_FAILED`, Sentry event. Staff decide (refund policy) |
| `confirmed_time` changed | `estimated_ready_time` updated (existing "ready time updated" notification) |

Reverse (local to HubRise, only for orders with a `sent` link): CONFIRMED -> `accepted`, PREPARING -> `in_preparation`, AWAITING_PICK_UP -> `awaiting_collection`, OUT_FOR_DELIVERY -> `in_delivery`, PICKED_UP / DELIVERED -> `completed`, CANCELLED -> `cancelled`. PENDING and FAILED are never pushed. Status PATCHes of one order are sent strictly in order. Both sides can change status (the dashboard and the EPOS); last event wins, with the guards below.

### 7.2 Safe application of inbound events

1. Callback handler: verify HMAC on the raw body (constant time), reject mismatched `location_id`, insert into `hubrise_events` (`ON CONFLICT DO NOTHING` = idempotency), wake the processor, answer 200 immediately. Nothing else happens in the request.
2. Processor: find the local order by `hubrise_order_id`; if unknown, `GET /location/orders/:id` (our `private_ref` is only visible when fetched with our token) and match on `private_ref`; unknown orders (placed by other channels) are ignored.
3. Ordering guard: ignore events with `created_at` older than `hubrise_orders.last_event_at`; for safety re-read the authoritative status with `GET /location/orders/:id` when the event looks stale or its `new_state` is missing.
4. Idempotent: mapped status equal to current status is a no-op. Terminal local statuses (CANCELLED, FAILED, DELIVERED, PICKED_UP) are never moved back by a non-terminal remote status (logged, flagged).
5. Apply through the shared transition method (task T5) with `source = hubrise`, so emails, APNs/FCM, Live Activity, `orderUpdated` PubSub and coupon rollback behave exactly as for a dashboard change, and no echo PATCH is queued (HubRise would not echo it anyway because it is our own client).
6. Failure (for example `ErrPaymentNotRefundable` on cancel) leaves the order untouched, the event `failed` with backoff, and the order flagged for staff.

## 8. Inventory and sold-out

- **Inbound.** On `inventory.update` / `inventory.patch`, or the 30 min poll: `GET /location/inventory`. For each entry: `sku_ref` -> product link; `option_ref` -> **all** choice links with that option ref. `stock = 0` -> `is_available = false` (product or choice) and `sold_out_until = expires_at`; positive stock -> available (the number is stored on the link for display; no per-order stock accounting in v1); entry absent -> available. Only links under `sync_inventory`; unlinked refs are ignored. Changes publish `productUpdated`. Expired `stock 0` entries are released by the 1 min sweeper and by the `inventory.patch` expiry notice.
- **Outbound.** `AvailabilityChanged` writes an `inventory` outbox row with dedupe key `inv:sku:<ref>` (latest state wins) sent as `PATCH /location/inventory [{sku_ref, stock: "0"}]` or `stock: null` to make it available again. The payload is computed from the product's state at send time. An inbound change applied with `source = hubrise` does not enqueue an echo.
- **Order validation.** Pricing/quoting already refuses unavailable products (`PRODUCT_UNAVAILABLE`). It additionally refuses a selection of an unavailable choice with the **existing** code `SELECTION_INVALID` (no new code, so stored apps behave).
- **Conflict.** One truth: the HubRise inventory. A dashboard toggle is pushed to it; if HubRise later reports different, the later event wins.

## 9. Security

- **Token at rest.** AES-256-GCM, key `HUBRISE_TOKEN_KEY` (32 bytes, base64), key version byte for rotation. Decrypted only inside the client constructor; never logged, never in GraphQL, never in `staff_audit_log` variables, stripped from errors. `zap` fields use a `Redacted` type.
- **Callback authenticity.** HMAC-SHA256 hex over the raw body with `HUBRISE_CLIENT_SECRET`, `hmac.Equal`, body capped at 1 MB (already the global cap), per-IP rate limit generous enough for HubRise retries. No signature or bad signature: 401 and nothing stored. Events carry unique ids so replays are harmless.
- **OAuth.** Authorization code flow; `state` is a random nonce stored in `hubrise_oauth_states` bound to the admin user, one use, 10 minutes. The callback endpoint is public (the browser lands on it) but accepts only a valid stored state. The code is exchanged server-side with HTTP Basic client auth. The redirect after success goes to `APP_DASHBOARD_URL`.
- **Authorization.** Every GraphQL operation is `@admin`; the existing audit extension records mutations in `staff_audit_log`. The two REST endpoints record custom audit entries (`hubrise.connect`, `hubrise.disconnect`).
- **Least privilege.** Scope `location[orders.write,catalog.read,inventory.read,inventory.write]`; no customer list scope in v1.
- **SSRF / host pinning.** HubRise base URLs come from env only; the client refuses redirects to other hosts.

## 10. GraphQL and REST surface (additive only)

New file `internal/api/graphql/schema/hubrise.graphql`, all `@admin`:

```graphql
type HubriseStatus {
  available: Boolean!            # env configured on this deployment
  connected: Boolean!
  enabled: Boolean!
  accountName: String  locationName: String  catalogName: String
  features: HubriseFeatures!     # pushOrders, syncStatus, syncInventory, syncCatalog
  catalogLanguage: String!  variantRef: String  channelName: String  importImages: Boolean!
  lastCatalogSyncAt: DateTime  lastEventAt: DateTime
  authErrorAt: DateTime  lastError: String
  backlog: HubriseBacklog!       # pending, parked
  unlinkedProducts: Int!
  attention: Int!                # orders with needs_attention
}
extend type Query {
  hubriseStatus: HubriseStatus!
  hubriseOutbox(state: HubriseOutboxState, first: Int = 50): [HubriseOutboxItem!]!
}
extend type Mutation {
  startHubriseConnection: String!                       # authorize URL
  disconnectHubrise(forgetLinks: Boolean = false): HubriseStatus!
  updateHubriseSettings(input: HubriseSettingsInput!): HubriseStatus!
  previewHubriseCatalogSync(mode: HubriseSyncMode!): HubriseCatalogDiff!
  syncHubriseCatalog(mode: HubriseSyncMode!): HubriseSyncResult!
  syncHubriseInventory: Int!
  linkHubriseProduct(productId: ID!, skuRef: String): Boolean!   # manual adoption; null unlinks
  retryHubriseOutbox(id: ID): Int!
}
```

Additive fields on existing types (clients that do not ask for them are unaffected): `Product.hubriseManaged: Boolean!` (DataLoader backed), `Product.hubriseLockedFields: [String!]!`, `ProductChoice.isAvailable: Boolean!`, `Order.hubrise: HubriseOrderInfo` (@staff: remote order id, push state, remote status, attention). New `apperr` codes (codes are an API, add only): `HUBRISE_MANAGED`, `HUBRISE_NOT_CONFIGURED`, `HUBRISE_NOT_CONNECTED`, `HUBRISE_UPSTREAM`.

REST (registered only when available):
- `POST /api/v1/hubrise/callback` (HMAC auth, no bearer).
- `GET /api/v1/hubrise/oauth/callback` (state auth, redirects to the dashboard).

Nothing existing changes shape: no field removed, renamed or made non-null, no enum value added to an enum clients already switch on (`OrderStatusEnum` untouched; new quote issue codes: none).

## 11. Configuration

New env vars (documented in `.env.example` and CLAUDE.md): `HUBRISE_CLIENT_ID`, `HUBRISE_CLIENT_SECRET`, `HUBRISE_TOKEN_KEY`, optional `HUBRISE_API_BASE_URL` (default `https://api.hubrise.com/v1`), `HUBRISE_OAUTH_BASE_URL` (default `https://manager.hubrise.com/oauth2/v1`; both overridden to the Go mock in tests and local dev). Callback and OAuth redirect URLs derive from the existing `API_BASE_URL`. All timestamps sent to HubRise use `pkg/timezone` (Europe/Brussels), never the runtime zone.

## 12. Failure modes

| Situation | Behaviour |
|---|---|
| HubRise down or slow | Orders still created, paid, shown in the dashboard; outbox retries; nothing user-visible breaks |
| 401 (revoked, unpaid, blocked) | `auth_error_at` + `last_error`, workers pause the pipeline without burning attempts, banner in the dashboard status, Sentry; reconnect clears it |
| 429 | client-side limiter plus backoff; catalog and order-list reads throttled to under 10/min |
| Callback lost | event poller + reconciler + 6 h catalog drift check |
| Two replicas | SKIP LOCKED claims, advisory locks on singleton jobs and on the catalog import |
| Catalog invalid or empty | refuse to apply an empty catalog over a populated menu (guard: a catalog with zero products is rejected); partial failure rolls back the batch |
| HubRise order never accepted | after 5 min in `new` the order is flagged for staff (visible in `hubriseStatus.attention`) |

## 13. Production rollout and rollback

1. Deploy code with the new env unset: only the six additive migrations run; behaviour identical. This is the first release and can sit there.
2. Deploy tsb-dashboard with the HubRise settings page and managed-product UI **before** enabling catalog sync (otherwise a price edit on a managed product returns `HUBRISE_MANAGED` to an unprepared UI).
3. Set `HUBRISE_*` secrets (tsb-infra SOPS), restart. Connect from the dashboard.
4. Enable progressively, each step reversible: orders push (with `LINK_ONLY` adoption first so items carry refs) -> status sync -> inventory -> catalog sync (after reading the `DRY_RUN` diff).
5. Rollback ladder: toggle a feature off; `enabled = false` (everything pauses, locks released); disconnect; unset env (module inert again); goose Down of the migrations (all droppable; the data they hold is HubRise-derived).

## 14. Testing strategy (98 % gate)

- `pkg/hubrise`: unit tests with `httptest` for every endpoint, error classification, rate limiting, money parsing (port the moyun-pos `money.rs` cases: float traps, rejects, round trips).
- `pkg/hubrise/hubrisetest`: in-memory HubRise implementing the used endpoints (OAuth token/revoke, location, catalog GET/PUT, orders POST/GET/PATCH with `private_ref`, inventory GET/PUT/PATCH, callback register, events list/ack) with a control API (inject order status change, replace catalog, change inventory, fault injection, request log) and an HMAC-signing event dispatcher to our callback URL. Fixtures mirror moyun-pos (`pos-hubrise-mock/fixtures`, `pos-hubrise-types/tests/fixtures`).
- Repositories: against the shared PostgreSQL container with real migrations, including Up/Down/Up and a test that M1..M6 apply on a database seeded with the current schema and sample data (counts unchanged).
- Application services: table-driven, fake ports for unit paths, plus end-to-end flows against the mock: connect -> adopt -> import -> push order -> remote `accepted` -> local CONFIRMED + notification fakes -> remote `cancelled` -> refund path; outage -> retry -> park -> manual retry; duplicate and out-of-order callbacks; bad HMAC; token revoked; two workers racing on one outbox.
- **Standalone parity**: the whole existing suite runs with `Resolver.Hubrise == nil` and must stay green unchanged; plus tests with the module present but `enabled = false` asserting zero HubRise HTTP calls and zero new rows after create/update order and update product; plus a spy test that hooks are the only coupling.
- Locks: every locked field has a rejection test, and a test that the same edit succeeds when disabled.
- Translation survival: manual override kept across three imports; non-catalog languages untouched; the `IS DISTINCT FROM` rule under a full-language resave.
- Commands: `CGO_ENABLED=0 go test ./internal/... ./cmd/... ./pkg/...`, `/opt/homebrew/Cellar/golangci-lint/2.14.0/bin/golangci-lint run --timeout=5m`, `go run github.com/99designs/gqlgen generate`, `go build ./cmd/... ./internal/... ./pkg/...`. CI runs `-race` and the 98 % script.

## 15. Client impact (follow-ups outside this workflow)

- **tsb-dashboard** (needed before catalog sync is enabled): HubRise settings page (connect, disconnect, toggles, language, variant, preview/adopt flow with diff table, backlog and parked rows with retry, auth-error banner); product dialog: "managed by HubRise" badge, disable locked fields from `hubriseLockedFields`, handle `HUBRISE_MANAGED`, show per-language override state; order detail: push state and attention; i18n fr/en/zh. Everything is additive, so the current dashboard keeps working until sync is enabled.
- **tsb-core (web)** and **tsb-mobile**: nothing required. Optional: grey out a choice when `ProductChoice.isAvailable` is false (until then the server refuses with the existing `SELECTION_INVALID`). Check the menu cache TTL in tsb-core SSR so a sold-out change is visible quickly. Both are unaffected by the order or status behaviour.
- **tsb-pos-android**: nothing required (status changes arrive through the existing `orderUpdated` subscription). Optional HubRise badge.
- **tsb-mcp** (in this repo): propose_* tools on managed products will surface `HUBRISE_MANAGED`; add the managed flag to `get_product` and mention it in `docs/mcp`.
- **tsb-infra**: new SOPS secrets and env, route `/api/v1/hubrise/*` (already covered by `/api/v1/*` in the tunnel), no WAF rule blocking HubRise callbacks.
- **moyun-pos**: no change; this plan consumes its existing catalog conventions (`custom_fields.code`, `vat_category`, `translations`) and expects the `webshop`/channel and payment refs described in section 6.2 (open question 5).

## 16. Task breakdown

Each task is one committable increment: build green, lint clean, tests passing, coverage kept at or above 98 %, standalone parity intact. Conventional Commits. Detailed specs are in the structured output of the planner; summary:

- **T1 Schema, domain, storage, crypto, config.** Migrations M1, M2, M6 (M3/M4/M5 come with the code that uses them), domain entities and ports, sqlx repositories on the Admin pool, AES-GCM token store, `Module` constructor that returns nil when unconfigured, env parsing.
- **T2 HubRise API client and Go mock server.** `pkg/hubrise` (+ OAuth, errors, limiter, money) and `pkg/hubrise/hubrisetest` with fixtures and control API.
- **T3 Catalog import.** Migrations M3, M4; translation `source` rules; neutral catalog mapping; planner and applier (`DRY_RUN`, `LINK_ONLY`, `FULL`); product ownership guard and the `HUBRISE_MANAGED` error; unavailable-choice refusal; optional images.
- **T4 Outbox and order push.** Migration M5; hooks in `CreateOrder` and the paid webhook; payload builder; outbox worker with idempotent retry; reconciler catch-up.
- **T5 Callbacks, status sync and the shared transition method.** Refactor `UpdateOrder` orchestration into a reusable method; callback endpoint; event store and processor; status mapping both ways; stale/terminal guards.
- **T6 Inventory.** Inbound apply, expiry sweeper, outbound availability, option sold-out, drift and poll jobs.
- **T7 Connection management and GraphQL admin API.** OAuth endpoints, settings, disconnect, status/outbox queries, preview/sync/link mutations, additive Product/Order fields, audit entries, apperr codes.
- **T8 Wiring, workers, end-to-end, docs.** `cmd/app/hubrise.go`, graceful shutdown, `.env.example`, CLAUDE.md, `docs/hubrise/README.md` (operator runbook: connect, adopt, rollout ladder, tag and custom field conventions), full-flow tests, parity tests, coverage-check exclusion of the mock if needed.

## 17. Open questions for the owner

1. HubRise developer client: create one (client id and secret) and register the OAuth redirect URI `<API_BASE_URL>/hubrise/oauth/callback`. One client for both brands, or one each?
2. Is HubRise (written by moyun-pos) going to be the menu source of truth for Tokyo Sushi production, or only the order and status channel at first? The rollout ladder supports both; the default keeps `sync_catalog` off.
3. Guest orders (customer data inside the order) or push customers to a customer list? v1 uses guest orders to avoid a second GDPR data store.
4. Which `channel` value and `service_type_ref` does the POS expect for the webshop and the app (moyun-pos reads the lower-cased `channel`, defaulting to `webshop`, against a `hubrise_channels` table)?
5. Payment refs: moyun-pos classifies prepaid versus unpaid by payment refs. Confirm that `MOLLIE` is treated as non-cash prepaid, and cash orders stay unpaid.
6. Should an item edit made in the EPOS (deleted item) be applied to the local order? v1 only records the remote total and flags a mismatch.
7. Stock above zero: availability only (v1), or also cap the orderable quantity?
8. Who is alerted when HubRise rejects or never receives an order: dashboard banner and Sentry only (v1), or also an email like `ASSISTANT_ALERT_EMAIL`?
9. Delivery fee and online payment fee: do they exist as catalog charges with refs in HubRise (then we reuse those refs) or should we send our own (`DELIVERY`, `PAYMENT_FEE`)?

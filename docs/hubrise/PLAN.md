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

- **Catalog model.** `products` (price numeric(10,2), `code`, `slug` NOT NULL-ish unique, flags, `vat_category`, `is_visible`, `is_available`), `product_categories` (`order`, unique NOT NULL `slug`), translation tables with `language` CHECK in (en, fr, nl, zh). Choices are **per product**: `product_choice_groups.product_id`, `product_choices.product_id + choice_group_id`, with `min_selections >= 0`, `max_selections >= 1`, `min <= max`, `price_modifier >= 0` (CHECK). Orders reference them through `order_product_choices` (re-verified in `20260428130000_choice_groups_and_multi_selections.sql`: `product_choice_group_id` and `product_choice_id` are plain FKs without cascade, so the database refuses to delete a choice or group used by an order; the `ON DELETE CASCADE` seen on `20260220205101` belongs to the translation table and to `product_choices.choice_group_id`). The importer still never calls `DeleteChoice` / `DeleteChoiceGroup` / `DeleteProduct`: it only flips `is_available` / `is_visible`.
- **Order history is fragile.** `order_product.product_id` is `ON DELETE CASCADE`. Deleting a product destroys order lines. The integration must NEVER delete products: it retires them (hidden and unavailable).
- **`order_product` does not snapshot** product name, code or base price. `unit_price` is an average per unit that includes option surcharges. HubRise needs the base SKU price and the options separately, so the item part of the push payload is snapshotted into the outbox row when the order becomes actionable (the status and the SKU refs are resolved at send time, section 6.1).
- **Selection quantities** in `order_product_choices.quantity` are totals for the whole line (group min/max are multiplied by the line quantity, see `domain.PriceLine`). HubRise option quantity is per single item unit. Lines whose selections are not divisible by the quantity must be split per unit (section 7).
- **Order actionable moments.** Cash orders: end of `CreateOrder` resolver (where `SendNewOrderPush` is called). Online orders: the `default` case of the Mollie webhook paid branch (`payment/interfaces/handler.go`, after the cancelled/failed/test exclusions, next to `notifier.SendNewOrderPush`). These are exactly the moments to enqueue the HubRise push. Online orders must not be pushed before payment (HubRise integration guide says the same). Mollie redelivers webhooks, so the enqueue must be idempotent (section 6.1).
- **Single chokepoint for order status writes (verified).** `orders.order_status` is written only by `OrderRepository.Update` (called solely from `orderService.UpdateOrder`, `order/application/service.go`) and by `CancelStaleTestOrders` (store-review test orders, which are never pushed). `orderService.UpdateOrder` is reached from the GraphQL `UpdateOrder` mutation (through `order_settlement.go`), from `paymentService.HandlePaymentFailed` (Mollie cancelled/failed/expired) and from the POS paths that use the same service. The outbound status hook therefore lives in `orderService.UpdateOrder` (an optional observer port), NOT in the resolver.
- **The status-change orchestration lives inside the GraphQL resolver** `mutationResolver.UpdateOrder` (about 400 lines): payment settlement and Mollie refund on cancel (`cancelOrder`, advisory lock), no reopening of refunded orders, confirmation / ready / cancelled / refund emails, APNs + FCM alerts, Live Activity and Android live update, "late notification" suppression, `orderUpdated` PubSub topics. A HubRise status callback must reuse all of it, so it has to be extracted into a reusable method (task T3). This is the riskiest refactor in the plan; the existing test suite is the safety net.
- **Dual DB pool.** `DBPool.ForContext(ctx)` returns the Admin pool for staff contexts (`utils.SetIsAdmin`). Background jobs and webhooks must run with an admin context, or the HubRise repositories take `pool.Admin` directly (as `audit.NewRecorder(dbPool.Admin)` does). New tables get no per-role GRANTs in this repo (no migration contains GRANT), so the Admin pool is the only one that touches them.
- **Production runs 2 replicas** (k3s, `replicas: 2`). Every worker must be safe to run twice and no correctness rule may depend on process memory: `FOR UPDATE SKIP LOCKED` claims for queues, a DB lease row (`hubrise_job_leases`, compare-and-set) for singleton jobs, `pg_advisory_xact_lock` (transaction scoped, safe with pooled connections) for the catalog import, DB-backed debounce. Session-level advisory locks are not used for HubRise jobs.
- **Client IP behind Cloudflare (verified).** `router.TrustedPlatform = gin.PlatformCloudflare` in `main.go`, so `c.ClientIP()` is the CF-Connecting-IP and the existing per-IP limiters key on the real client, not on the tunnel address.
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
| `OrderReady(ctx, orderID)` | `CreateOrder` (cash orders) and the Mollie paid branch, the latter through a small nil-safe `OrderReadyHook` port added to the `PaymentHandler` constructor (the payment package never imports resolver or hubrise; `main` passes an adapter) | idempotent enqueue of `order_create` |
| `OrderStatusChanged(ctx, orderID, newStatus)` | `orderService.UpdateOrder` through an optional `StatusObserver` port (single chokepoint, covers the GraphQL mutation, `HandlePaymentFailed` and POS paths); skipped when the context carries the HubRise origin marker | enqueue `order_status` |
| `AvailabilityChanged(ctx, productID, available)` | `UpdateProduct` / MCP path when `isAvailable` changed | enqueue `inventory` |
| `ProductGuard.CheckUpdate(ctx, old, new)` | product service `UpdateProduct`, choice and group mutations | reject edits of HubRise-owned fields |

When the installation is not configured, `Hubrise` is nil and no HubRise code runs. When it is configured but the runtime switch is off, every hook returns after one cached boolean check (15 s TTL cache of `hubrise_connection.enabled`, refreshed on settings change). The cache only saves queries on the hook path; it is never a correctness boundary: workers re-read `enabled` (and the feature toggles) inside the claim statement itself, so a disable takes effect on the next claim on every replica, and rows enqueued during the at-most-15 s lag are simply held until re-enable or aged out (section 6.1).

### 3.3 Two-level opt-in (standalone switch)

1. **Deployment level.** HubRise is *available* only when `HUBRISE_CLIENT_ID`, `HUBRISE_CLIENT_SECRET` and `HUBRISE_TOKEN_KEY` are all set. Otherwise: module not constructed, routes not registered, workers not started, GraphQL admin API answers `available: false`. A deployment that never sets them (every existing one, on day one) is byte-for-byte unchanged apart from additive migrations.
2. **Runtime level.** `hubrise_connection` single row. Not connected = standalone. Connected with `enabled = false` = paused, nothing leaves, nothing is imported, locks are released, links and the outbox are kept. Four feature toggles on top: `push_orders`, `sync_status`, `sync_inventory`, `sync_catalog` (catalog mode `hubrise`).

Toggling is lossless: disabling never deletes links or products; re-enabling re-adopts the same rows. "Connected" means `access_token_enc IS NOT NULL`. "Disconnect" revokes the token at HubRise, deletes the callback, sets `access_token_enc = NULL` and `enabled = false` (the row, its settings and ids stay), keeps links unless `forgetLinks: true` (products stay either way, they simply become ordinary local products).

Re-enabling push is not a replay: `push_orders_since` is stamped every time order pushing becomes effective (connect, `enabled` false to true, `push_orders` false to true), and the reconciler only ever considers OPEN orders created after `push_orders_since - 1 h` (the 1 h grace covers orders in flight at connect time). Terminal orders are never auto-pushed, so a toggle cannot flood the EPOS with history or reprint kitchen tickets. A reconnect to a different HubRise location or catalog is detected and blocked (section 5.5).

### 3.4 Workers (all in-process, started from main when available)

| Worker | Cadence | What it does |
|---|---|---|
| Outbox drain | wake-up channel (an optimisation only) + 5 s tick | claims due rows (`FOR UPDATE SKIP LOCKED`, per-order FIFO, claim statement increments `attempts`, sets the lease and re-checks `enabled`/toggles), sends them, backoff 30 s doubling to 30 min, parks permanent refusals (section 6.1) |
| Event processor | wake-up channel + 5 s tick | claims `pending` callback events, applies them idempotently |
| Reconciler | 2 min, lease-guarded | (a) enqueue OPEN orders created after `push_orders_since - 1 h` that became actionable but have no `hubrise_orders` row (crash between commit and enqueue), (b) status safety net: for orders with a `sent` link whose local status differs from `last_remote_status` and no pending `order_status` row, enqueue the missing PATCH (covers any writer that bypassed the observer, and lost rows), (c) refresh at most 5 open pushed orders per run with `GET /location/orders/:id` (oldest refresh first), (d) flag orders still `new` at HubRise after 5 min, (e) purge old events and foreign-order cache |
| Catalog drift | 6 h, plus every 30 s a check for `catalog` events older than 30 s (debounce is DB backed: events carry `received_at`, all due ones collapse into one import) | fetch catalog, compare content hash, import if changed (never auto-applies a mass retire, section 5.5) |
| Inventory full sync | 30 min and on `inventory` events | `GET /location/inventory`, apply |
| Callback poller | 10 min | `GET /callback/events` to pick up events HubRise could not deliver, then ack |
| Sold-out expiry sweeper | 1 min, lease-guarded | stock-0 entries whose `expires_at` passed become available again (only links with `inventory_sold_out = true`) |

Singleton jobs take a row in `hubrise_job_leases (name PRIMARY KEY, holder, leased_until)` with `UPDATE ... WHERE leased_until < now()` (compare-and-set, renewed while running), so correctness does not depend on which pooled connection runs which statement. All workers use an admin context and stop on shutdown (same pattern as the existing purge and sweeper goroutines in `main.go`).

**Rate budget** (HubRise limits: 500/min, 2,500/h, 10,000/day per connection; only `GET /catalogs/:id` and the order LIST endpoint `GET /orders`, which includes `?private_ref=` lookups, are also capped at 10/min; single-order retrieval `GET /orders/:id` is not in that bucket). The client limiter enforces all four windows with headroom (default ceilings 400/min, 2,000/h, 8,000/day) and splits the 10/min list bucket by priority: 6/min reserved for order lookups (idempotency), 3/min for catalog reads, 1 spare. Two classes of caller: `order-critical` (create, status PATCH, private_ref lookups, inventory PATCH) which may use the whole hourly/daily budget, and `background` (reconciler refresh, drift, inventory poll, callback poller) which is cut off at 60 % of the hourly and daily ceilings. The reconciler never lists orders: it refreshes at most 5 single orders per run. After an outage, only rows that were already claimed before (possibly sent) need a lookup, so a 100 order backlog drains in about 17 min without ever starving the catalog drift check; this is asserted by a limiter test.

## 4. Database changes (all additive, expand only)

Principles: no table rewrite, no long lock, no data destroyed, every migration has a working `Down`.

- Each migration that touches an existing table starts with `SET LOCAL lock_timeout = '5s'`. Goose wraps a file in one transaction, so `lock_timeout` only bounds lock ACQUISITION: a busy table makes the migration fail fast instead of queueing every writer behind an ACCESS EXCLUSIVE request. A failed migration makes `tsb-migrate` exit non-zero, which fails the deploy job before the new pods roll out; the old pods keep serving (the migration is additive and the old binary ignores new columns), and the deploy is simply re-run. A test holds an open transaction with a row lock on `orders` and asserts the migration errors out within the timeout without waiting.
- `ADD COLUMN ... NOT NULL DEFAULT <constant>` and nullable `ADD COLUMN` are metadata-only on PostgreSQL 11+ (prod is far above). **No CHECK constraints are added to existing tables** (review decision): `ADD CONSTRAINT ... NOT VALID` followed by `VALIDATE CONSTRAINT` inside one goose transaction would hold the ACCESS EXCLUSIVE lock through the validation scan, which is exactly what must not happen on `orders`. Values of `orders.channel` and `*_translations.source` are validated in Go (domain constants) instead. New tables (all empty at creation) may carry CHECKs freely.
- No `CREATE INDEX` on big existing tables.
- **Version ordering.** Every HubRise migration has a strictly increasing timestamp in task order (below) and later than anything on main, so an environment that already applied an earlier task's migrations (the test server deploys on every push to main) never meets a "missing" lower version. A test applies all migrations in order on a clean database, then asserts goose reports no missing versions (`goose.WithAllowOutofOrder(false)`) and that every HubRise migration is newer than the newest pre-HubRise one.
- `cmd/migrate` runs before the new binary in the deploy flow (tsb-migrate ships in the image), as for every previous migration.

Considered and **rejected** restructurings (and why):

- *Shared option lists as a first-class table.* HubRise option lists are shared across SKUs; ours are per product (`product_choice_groups.product_id`). Making them shared would change the GraphQL shape (`ProductChoiceGroup.productId`, `choices`), the order FKs, the dashboard editor and the apps already in the stores. Instead the importer **materializes one local group per (SKU, option list)**, linked by key `sku|list`. Cost: duplication of rows. Benefit: zero client change.
- *Splitting product and SKU.* Local products have a single price; a HubRise product with several SKUs becomes several local products named "Product - SKU" (same as moyun-pos, one item per SKU).
- *Unique constraint on `products.code`* or making `code` mandatory: not needed.
- *Moving HubRise columns onto core tables* (`products.hubrise_ref`...): rejected in favour of one link table so core tables stay HubRise-free and standalone installs carry nothing. The only core-table additions are the three listed below.

Migration order (task order = timestamp order):

### M1 `20261026120000_create_hubrise_connection.sql` (T1)

- `hubrise_connection`: `id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id)` (single row, same idiom as `restaurant_config`), `enabled BOOLEAN NOT NULL DEFAULT FALSE`, **`access_token_enc BYTEA NULL`** (AES-256-GCM, 1 version byte + 12 byte nonce + ciphertext; NULL = disconnected), `account_id, account_name, location_id, location_name, catalog_id, catalog_name, customer_list_id TEXT`, `scope TEXT`, feature toggles `push_orders, sync_status, sync_inventory, sync_catalog BOOLEAN NOT NULL DEFAULT TRUE/TRUE/TRUE/FALSE`, `auto_refund_on_cancel BOOLEAN NOT NULL DEFAULT FALSE`, `catalog_language TEXT NOT NULL DEFAULT 'fr'` (CHECK in fr,en,nl,zh), `variant_ref TEXT`, `channel_name TEXT`, `delivery_charge_ref, payment_fee_charge_ref TEXT`, `import_images BOOLEAN NOT NULL DEFAULT FALSE`, `catalog_hash TEXT`, `catalog_preview_hash TEXT`, `catalog_preview_at TIMESTAMPTZ` (a successful DRY_RUN; required before `sync_catalog` can be turned on, section 5.4), `push_orders_since TIMESTAMPTZ`, `inventory_baseline_at TIMESTAMPTZ` (section 8), `relink_required_at TIMESTAMPTZ` (section 5.5), `last_catalog_sync_at, last_event_at, auth_error_at TIMESTAMPTZ`, `last_error TEXT`, `callback_registered_at`, `created_at, updated_at`.
- `hubrise_oauth_states (state TEXT PRIMARY KEY, admin_user_id UUID NOT NULL, created_at, expires_at)` for the OAuth `state` nonce (one-time, 10 min).
- `hubrise_job_leases (name TEXT PRIMARY KEY, holder TEXT NOT NULL, leased_until TIMESTAMPTZ NOT NULL)`.
- Backfill: none. Tables start empty; absence of a row = not connected = standalone. Down: drop the tables.

### M2 `20261026130000_create_hubrise_catalog_links.sql` (T1)

`hubrise_catalog_links`: `id BIGSERIAL PK`, `entity_type TEXT CHECK in (category, product, choice_group, choice)`, four nullable FK columns `category_id -> product_categories`, `product_id -> products`, `choice_group_id -> product_choice_groups`, `choice_id -> product_choices`, **all `ON DELETE CASCADE`** (deleting a choice in the dashboard cleans its link), a CHECK that exactly the column matching `entity_type` is non-null, `hubrise_key TEXT NOT NULL` (category: ref; product: SKU ref; group: `skuRef|listRef`; choice: `skuRef|listRef|optionRef`), `hubrise_ref TEXT NOT NULL` (last segment; the option ref is what inventory entries name), `hubrise_id TEXT`, `content_hash TEXT NOT NULL DEFAULT ''`, `origin TEXT CHECK in (import, adopted, manual)`, `stock NUMERIC(12,3)`, **`inventory_sold_out BOOLEAN NOT NULL DEFAULT FALSE`** (true only while this link's unavailability was caused by HubRise inventory; the provenance marker of section 8), `sold_out_until TIMESTAMPTZ`, **`catalog_disabled BOOLEAN NOT NULL DEFAULT FALSE`** (HubRise restrictions say disabled; effective availability of a managed row = `NOT catalog_disabled AND NOT inventory_sold_out`), `inventory_synced_at`, `image_md5 TEXT`, `retired_at TIMESTAMPTZ`, timestamps. Partial unique indexes per FK column (one link per local row) and `UNIQUE (entity_type, hubrise_key)`; index on `(entity_type, hubrise_ref)` for inventory lookups.
Backfill: none (no links until the owner runs adoption). Down: drop table.

### M3 `20261026140000_create_hubrise_orders_outbox_events.sql` (T1; was M6 in the first draft)

- `hubrise_orders`: `order_id UUID PRIMARY KEY REFERENCES orders ON DELETE CASCADE`, `hubrise_order_id TEXT` (unique where not null), `push_state TEXT CHECK in (pending, sent, parked, skipped)`, `pushed_at`, `last_remote_status TEXT`, `last_remote_event_at TIMESTAMPTZ` (set ONLY from remote event or remote GET data), `last_local_patch_at TIMESTAMPTZ` (set when we send a status PATCH), `remote_total NUMERIC(10,2)`, `needs_attention TEXT`, timestamps. This table is also the idempotency anchor for `order_create`: enqueue is `INSERT INTO hubrise_orders ... ON CONFLICT (order_id) DO NOTHING` and the outbox row is inserted in the same transaction only when the insert happened.
- `hubrise_outbox`: `id BIGSERIAL PK`, `kind TEXT CHECK in (order_create, order_status, inventory)`, `order_id UUID REFERENCES orders ON DELETE CASCADE NULL`, `dedupe_key TEXT`, `payload JSONB NOT NULL DEFAULT '{}'`, `state TEXT CHECK in (pending, sending, sent, parked, skipped) DEFAULT pending`, `attempts INT NOT NULL DEFAULT 0` (number of claims), `leased_until TIMESTAMPTZ`, `next_attempt_at TIMESTAMPTZ DEFAULT now()`, `last_error TEXT`, `created_at, sent_at, parked_at`. Partial unique index `(dedupe_key) WHERE state = 'pending' AND kind = 'inventory'` (inventory rows collapse to the latest pending state; a row already `sending` is excluded, so a newer toggle creates a fresh row and is never lost). `order_status` rows are NOT deduplicated (section 7.1). Index `(state, next_attempt_at)`, index `(order_id, id)`.
- `hubrise_events`: `id TEXT PRIMARY KEY` (HubRise event id), `resource_type, event_type TEXT`, `payload JSONB NOT NULL`, `received_at`, `run_after TIMESTAMPTZ`, `state TEXT CHECK in (pending, done, failed, ignored)`, `attempts INT`, `next_attempt_at`, `processed_at`, `last_error`.
- `hubrise_foreign_orders (hubrise_order_id TEXT PRIMARY KEY, seen_at TIMESTAMPTZ)`: cache of HubRise orders known not to be ours (placed by other channels), purged after 7 days, so the processor never re-fetches them.
- Backfill: none. Down: drop the tables.

### M4 `20261026150000_add_translation_source.sql` (T4)

`source TEXT NOT NULL DEFAULT 'manual'` on `product_translations`, `product_category_translations`, `product_choice_translations`, `product_choice_group_translations`. No CHECK (validated in Go: `manual` | `hubrise`). Each of the four ALTERs sits behind `SET LOCAL lock_timeout`.
Backfill: the default **is** the backfill: every existing row is correctly `manual` (nothing ever came from HubRise). Down: drop the columns (loses provenance only).
Code change that goes with it: the dashboard's translation upsert must keep `source = 'manual'` only when the text actually changed:
`ON CONFLICT ... DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description, source = CASE WHEN (t.name, t.description) IS DISTINCT FROM (EXCLUDED.name, EXCLUDED.description) THEN 'manual' ELSE t.source END`.
The dashboard sends all languages on every product save; without the `IS DISTINCT FROM` rule a price edit would turn every HubRise name into a manual override.

### M5 `20261026160000_add_choice_is_available.sql` (T4)

`product_choices.is_available BOOLEAN NOT NULL DEFAULT TRUE`. Backfill: default. Down: drop column. Needed because HubRise inventory can sell out an *option* (egg, extra sauce), and choices had no availability.

### M6 `20261026170000_add_orders_channel.sql` (T5)

`orders.channel TEXT NULL`, no CHECK. Set at creation from the existing `couponChannel(ctx)` (verified: it returns `web` or `app`, `app` when the access token belongs to the native client). Backfill: none; `NULL` is read as `web`. **Scan safety (verified):** `OrderRepository` reads orders with `SELECT *` / `SELECT o.*` into `domain.Order` through sqlx, which fails on a column without a destination field, and a NULL cannot scan into a plain `string`. So `domain.Order` gets `Channel *string` (tag `db:"channel"`), every `SELECT` into `domain.Order` is audited, and a test reads rows that existed before the migration (NULL channel) through every order read path. Used as the HubRise `channel` fallback (EPOS side such as moyun-pos maps channels to payment types) and for reporting. Down: drop column.

Net effect on existing production tables: 6 additive columns (4 translation `source`, `product_choices.is_available`, `orders.channel`), no data change, no constraint added.

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

Safety rules that live in the application service (T4), so T8 only wires them:

- **Preview before enabling.** `sync_catalog` can be turned on only if a successful `DRY_RUN` of the current `catalog_hash` was recorded in the last 24 h (`catalog_preview_hash` / `catalog_preview_at`). Otherwise `HUBRISE_PREVIEW_REQUIRED`.
- **Empty-catalog refusal**: a catalog with zero products is rejected.
- **Mass-retire threshold** (see 5.5): the plan refuses to retire more than 30 % of the linked, non-retired products (and at least 5) unless the caller passes `confirmRetire: true`. Scheduled drift imports never carry that confirmation: when the threshold trips they apply nothing, store the diff summary in `last_error` / the status `pendingReview` flag and wait for a human.

### 5.5 Reconnecting to a different location or catalog

Links name refs of one specific HubRise catalog. On the OAuth callback the new `location_id` / `catalog_id` are compared with the stored ones. If they differ and `hubrise_catalog_links` is not empty, the connection is saved with `enabled = false`, `sync_catalog = false`, `sync_inventory = false` and `relink_required_at = now()`; `updateHubriseSettings(enabled: true)` is then refused with `HUBRISE_RELINK_REQUIRED` until the owner clears the old links (`forgetHubriseLinks` mutation, or `disconnectHubrise(forgetLinks: true)` before reconnecting). Pushing orders with stale `sku_ref`s and retiring a whole menu against a foreign catalog are therefore both impossible. The mass-retire threshold above is the second line of defence.

## 6. Pushing orders

### 6.1 Flow

`OrderReady` -> one transaction: `INSERT INTO hubrise_orders (order_id, push_state='pending') ON CONFLICT DO NOTHING`, and only if a row was inserted, insert the `order_create` outbox row -> drain worker POST -> `hubrise_order_id` stored, `private_ref` = local order UUID. A redelivered Mollie webhook (or a double call) hits the primary key and enqueues nothing (test: paid webhook delivered 3 times yields exactly one outbox row).

**Snapshot at enqueue, status and refs at send time.** Because `order_product` does not snapshot names or base prices (section 2), the item data (names, base prices, option modifiers, tax rate, charges, discounts, customer, notes) is captured into the outbox payload in the enqueue transaction, so a price edit seconds later cannot change what the kitchen sees. Two parts are resolved by the worker right before the POST: `sku_ref` / `option ref` (from the link table, so an adoption done meanwhile is picked up) and the initial status. The initial `status` is the order's CURRENT local status mapped to HubRise (so an order that staff already moved to CONFIRMED / PREPARING / AWAITING_PICK_UP while the push was waiting or backing off is created in that state, never as a stale `new`). To keep this consistent with queued status rows, the worker reads, in this order and in one short transaction, (1) the highest pending `order_status` outbox id for the order, then (2) the current local status; after a successful POST the pending `order_status` rows with `id <= (1)` are marked `skipped` (their transitions are covered by the create) and `last_remote_status` is set. Rows enqueued after (1) stay queued and are sent as PATCHes in order.
If, at send time, the order is already terminal and was never sent (CANCELLED, FAILED, DELIVERED, PICKED_UP), the create is marked `skipped` (HubRise never sees an order the shop has already finished; nothing is reprinted).

**Claiming and leases.** The claim is one statement: `UPDATE hubrise_outbox SET state = 'sending', attempts = attempts + 1, leased_until = now() + interval '2 minutes' WHERE id IN (SELECT ... WHERE state = 'pending' AND next_attempt_at <= now() AND <FIFO rule> AND connection enabled and toggle on FOR UPDATE SKIP LOCKED) RETURNING ...`. `attempts` is therefore durable before the HTTP call, so a crash between send and acknowledgement is recognised on the next claim (`attempts > 1`, or a `sending` row whose lease expired and is reclaimed) and the worker first runs the idempotency lookup `GET /location/orders?private_ref=<uuid>`; if the order exists it is adopted (store `hubrise_order_id`, mark sent) instead of being POSTed again. Completion is `UPDATE ... SET state = 'sent' WHERE id = $1 AND state = 'sending'`. A row in `sending` is outside the inventory dedupe index, so a toggle arriving mid-send inserts a fresh pending row instead of being merged into the in-flight one and lost.

**FIFO rule per order.** A row is claimable only if the same order has no earlier row in `pending`, `sending` or `parked` (a parked create blocks everything behind it, so a PATCH is never sent for an order HubRise never received). Inventory rows are FIFO per `dedupe_key`.

**Outcome classification.**

| Outcome | Behaviour |
|---|---|
| 2xx | sent |
| 401, 403 (revoked token, unpaid account, missing scope, location disabled: all connection-level at HubRise) | pause the pipeline: `auth_error_at` + `last_error`, the claim's `attempts` increment is reverted, nothing is parked, banner in the dashboard status and Sentry; reconnect clears it |
| 400, 422 | park the row with the HubRise message (the payload is wrong, retrying cannot help); a manual retry reactivates the WHOLE parked chain of that order |
| 404 on a status PATCH or on a lookup of a known id | do not park: re-verify through `private_ref`; if HubRise knows the order under another id, backfill `hubrise_order_id` and retry; if it does not know it, reset `hubrise_orders` to `pending` and re-enqueue the create once (the rest of the chain waits behind it); a second 404 parks |
| 429, 5xx, timeout | retry with backoff 30 s doubling to 30 min |

**Max age per kind.** `order_create` 48 h then parked and flagged. `order_status` 6 h then `skipped` (the reconciler diff re-enqueues the right PATCH if the order is still open and the link is `sent`); an `order_status` or `inventory` row whose order has no `hubrise_order_id` and whose create is parked or skipped is `skipped` immediately. `inventory` 24 h. After a pause or a disable of several days, stale rows are therefore aged out, not replayed (test: re-enable after 3 days).

**Catch-up guarantee.** The enqueue happens after the order commit, so a crash between the two would lose the push. The reconciler enqueues any OPEN, actionable order (not pending payment, not test, not failed, not terminal) created after `push_orders_since - 1 h` that has no `hubrise_orders` row. It never enqueues terminal orders and never uses the connection row's `created_at`, so neither a toggle nor a reconnect can replay history. The same reconciler status diff (section 3.4 (b)) is the safety net if the status observer ever misses a transition.

### 6.2 Payload mapping

| HubRise field | Value |
|---|---|
| `private_ref` | order UUID |
| `ref` | short human reference (first 8 chars of the UUID, uppercase) |
| `channel` | `connection.channel_name`, else `orders.channel` (`web`/`app`), else `webshop` |
| `status` | current local status mapped to HubRise at send time (`new` for a fresh order), section 6.1 |
| `service_type` | PICKUP -> `collection`, DELIVERY -> `delivery` |
| `expected_time`, `asap` | `preferred_ready_time` (Europe/Brussels offset) else `asap: true` |
| `customer` (guest order) | first/last name, email, phone from the user; for delivery address_1 (street + number + box), postal_code, city, country `BE`, lat/lng, `delivery_notes` = address_extra. Customer lists are not used in v1 (GDPR surface, open question 3) |
| `customer_notes` | order note + order extras (chopsticks, sauces), cash amount to prepare change for |
| `coupon_codes` | coupon code |
| `items[]` | `product_name` (fr name), `sku_name`, `sku_ref` (link; item still sent without a ref if unlinked, and the order is flagged `needs_attention = UNLINKED_ITEM`), `price` = **base** SKU price, `quantity`, `tax_rate` = stored `vat_rate_applied` as a percent decimal 0 to 100 (HubRise format, e.g. `"6"`, not a ratio), `options[]` |
| `charges[]` | delivery fee (`ref` = configured delivery charge ref), online payment fee (`PAYMENT_FEE`) |
| `discounts[]` | pickup discount (`TAKEAWAY`), coupon discount (coupon code); amounts are positive `price_off` Money strings as in the moyun-pos `order_create_doc` fixture |
| `payments[]` | online: one entry `{name: "Mollie", ref: "MOLLIE", amount: total, info: {mollie_payment_id}}` (the `type` field is deprecated at HubRise and not sent); cash: none (unpaid), so the EPOS collects. Name/ref values are checked against the moyun-pos fixtures and open question 5 |
| rounding | the local total is rounded to 0.10 EUR; a `ROUNDING` charge or discount (explicit name "Rounding", ref `ROUNDING`, documented in the operator runbook so the kitchen understands the line) closes the gap so HubRise's `total` equals the local `total_price` exactly. HubRise CALCULATES `total` (items + charges - discounts, per the API docs); the mock does the same, and the response's `total` is compared with the local total: a mismatch sets `needs_attention = TOTAL_MISMATCH` |

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
| `cancelled`, `rejected` | Unpaid / cash orders: CANCELLED (reason `OTHER`) through the full cancel path (coupon rollback, notifications). **Online-paid orders: depends on `auto_refund_on_cancel`** (default `false`, an owner decision, open question 10): when off, nothing moves money; the order is left as is, `needs_attention = REMOTE_CANCELLED_PAID` is set, staff and Sentry are alerted, and staff confirm by cancelling in the dashboard (which runs the existing refund path; the matching outbound PATCH is skipped because `last_remote_status` is already `cancelled`). When on, the same full cancel path runs automatically: Mollie refund / payment cancel under the payment advisory lock, refund email, coupon rollback. An EPOS mis-tap or auto-reject rule can never refund money by default. |
| `delivery_failed` | no automatic change; `needs_attention = DELIVERY_FAILED`, Sentry event. Staff decide (refund policy) |
| `confirmed_time` changed | `estimated_ready_time` updated (existing "ready time updated" notification) |

Reverse (local to HubRise): CONFIRMED -> `accepted`, PREPARING -> `in_preparation`, AWAITING_PICK_UP -> `awaiting_collection`, OUT_FOR_DELIVERY -> `in_delivery`, PICKED_UP / DELIVERED -> `completed`, CANCELLED -> `cancelled`. PENDING and FAILED are never pushed (FAILED orders were never actionable). Both sides can change status (the dashboard and the EPOS); last event wins, with the guards below.

- **Enqueue rule.** `StatusObserver.OrderStatusChanged` (called from `orderService.UpdateOrder`, section 3.2) enqueues an `order_status` row for any order whose `hubrise_orders.push_state` is `pending` OR `sent` (a transition during an outage or backoff is no longer lost). Rows follow the per-order FIFO of section 6.1, so they wait behind the `order_create`; if the create goes out first with the then-current status, the covered rows are `skipped`.
- **No collapsing dedupe.** Each local transition gets its own row (`dedupe_key = order:<uuid>:status:<order_status_history id>`, unique per transition), with the TARGET status frozen in the payload. A sequence CONFIRMED -> PREPARING -> CONFIRMED is sent as three PATCHes in order; it is not collapsed by a "same status pending" key. A PATCH whose target equals `last_remote_status` is skipped as a no-op.
- **Writers audited.** T3 greps every writer of `orders.order_status` (see section 2) and adds a characterization test per path: GraphQL `UpdateOrder`, `UpdatePaymentStatus` -> service, `HandlePaymentFailed` (cancelled/failed/expired webhook), POS endpoints, MCP paths, and `CancelStaleTestOrders` (asserted NOT to enqueue: test orders are never pushed).

### 7.2 Safe application of inbound events

We register only the events we use (`order: [update]`, `catalog: [create, update, delete]`, `inventory: [update, patch]`; the names HubRise documents). A `POST /callback` with any other combination is a 422 at HubRise, so the Go mock rejects unknown combinations too and the registration test pins the exact body. `order.create` is not registered (orders created by other channels are irrelevant); if one ever arrives it is stored as `ignored` without any API call.

1. Callback handler: verify HMAC (`X-HubRise-Hmac-SHA256`, hex HMAC-SHA256 of the raw body keyed with the client secret, constant time), reject mismatched `location_id`, insert into `hubrise_events` (`ON CONFLICT DO NOTHING` = idempotency), wake the processor, answer 200 immediately. Nothing else happens in the request.
2. Processor lookup of the local order: by `hubrise_order_id`. If unknown:
   - if the order is in the foreign-order cache: ignore, no API call;
   - **race with our own push:** if any `order_create` outbox row is `pending`/`sending`, or a `hubrise_orders` row is `pending` with no `hubrise_order_id` and was claimed within the last 10 minutes, the event is DEFERRED (state stays `pending`, `next_attempt_at` +15 s growing, for up to 10 minutes), never ignored, because the answer to our POST may not be stored yet or may have been lost;
   - otherwise `GET /location/orders/:id` (single retrieval, not in the 10/min list bucket; at most 60 such GETs per minute, further events are deferred). The fetched `private_ref` is the authoritative match: if it names a local order, `hubrise_order_id` is backfilled and the event proceeds; if not, the order id goes into `hubrise_foreign_orders` and the event is `ignored`.
3. Ordering guard (clocks differ, HubRise timestamps have second precision and our own PATCHes generate no event for our client): `last_remote_event_at` is set only from remote data. When the event `created_at` is not strictly newer than it, equals it, falls within 5 s of `last_local_patch_at` for that order, or `new_state` is missing, the processor does not trust the event payload: it re-reads `GET /location/orders/:id` and applies the status found there (authoritative). Older strictly-stale events with no such ambiguity are ignored.
4. Idempotent: mapped status equal to current status is a no-op. Terminal local statuses (CANCELLED, FAILED, DELIVERED, PICKED_UP) are never moved back by a non-terminal remote status (logged, flagged).
5. Apply through the shared transition method (task T3) with the HubRise origin marker in the context, so emails, APNs/FCM, Live Activity, `orderUpdated` PubSub and coupon rollback behave exactly as for a dashboard change, and the observer does not queue an echo PATCH.
6. Failure (for example `ErrPaymentNotRefundable` on cancel) leaves the order untouched, the event `failed` with backoff, and the order flagged for staff.

Callback endpoint rate limiting (section 9): requests with a valid signature are never rate limited, so HubRise retry bursts cannot get 429 and lose events.

## 8. Inventory and sold-out

- **Provenance rule (review decision).** The system never "restores" availability it did not take away. Each link carries `inventory_sold_out` (true only while the current unavailability was caused by HubRise inventory) and `catalog_disabled`. Hand-set `is_available = false` in production data (or unavailability from any non-HubRise reason) is never flipped back by an inventory sync.
- **Inbound.** On `inventory.update` / `inventory.patch`, or the 30 min poll: `GET /location/inventory`. For each entry: `sku_ref` -> product link; `option_ref` -> **all** choice links with that option ref. `stock = 0`: if the row is currently available, set `is_available = false`, `inventory_sold_out = true`, `sold_out_until = expires_at`; if it is already unavailable for a local reason, record the sold-out state on the link but do not take ownership (`inventory_sold_out` stays false). Positive stock or entry absent: only rows with `inventory_sold_out = true` are made available again (and the flag cleared); the stock number is stored on the link for display (no per-order stock accounting in v1). Only links under `sync_inventory`; unlinked refs are ignored. Changes publish `productUpdated`. Expired `stock 0` entries are released by the 1 min sweeper and by the `inventory.patch` expiry notice, again only where `inventory_sold_out = true`. For managed choices, effective availability is `NOT catalog_disabled AND NOT inventory_sold_out`.
- **Baseline on first enable.** The first time `sync_inventory` becomes effective (`inventory_baseline_at IS NULL`), local state is authoritative for what the shop already decided: every linked product that is currently `is_available = false` is pushed to HubRise (`stock "0"`, outbound), and `inventory_baseline_at` is stamped. Until the baseline is done, inbound "entry absent" never restores anything. Tests: pre-existing manual unavailability survives enabling sync, the 30 min poll and a full sync.
- **Outbound.** `AvailabilityChanged` (from a dashboard / MCP toggle, never from an inbound apply) clears `inventory_sold_out` on the link (the shop now owns that state) and writes an `inventory` outbox row with dedupe key `inv:sku:<ref>` (latest pending state wins; a `sending` row is never merged into) sent as `PATCH /location/inventory [{sku_ref, stock: "0"}]` or `stock: null` to make it available again. The payload is computed from the product's state at send time. An inbound change applied with the HubRise origin marker does not enqueue an echo.
- **Order validation.** Pricing/quoting already refuses unavailable products (`PRODUCT_UNAVAILABLE`). It additionally refuses a selection of an unavailable choice with the **existing** code `SELECTION_INVALID` (no new code, so stored apps behave).
- **Conflict.** One truth: the HubRise inventory. A dashboard toggle is pushed to it; if HubRise later reports different, the later event wins (subject to the provenance rule above).

## 9. Security

- **Token at rest.** AES-256-GCM, key `HUBRISE_TOKEN_KEY` (32 bytes, base64), key version byte for rotation. Decrypted only inside the client constructor; never logged, never in GraphQL, never in `staff_audit_log` variables, stripped from errors. `zap` fields use a `Redacted` type.
- **Callback authenticity.** HMAC-SHA256 hex over the raw body with `HUBRISE_CLIENT_SECRET`, `hmac.Equal`, body capped at 1 MB (already the global cap). **Rate limiting only applies to FAILED verifications** (30 per minute per client IP, keyed on `c.ClientIP()` which is the CF-Connecting-IP behind the tunnel); a request with a valid signature is never limited, so HubRise retry bursts (6 retries, event loss after that) cannot be turned into 429s by other traffic sharing a bucket. Test: 20 signed events in a burst all get 200. No signature or bad signature: 401 and nothing stored. Events carry unique ids so replays are harmless.
- **OAuth.** Authorization code flow; `state` is a random nonce stored in `hubrise_oauth_states` bound to the admin user, one use, 10 minutes. The callback endpoint is public (the browser lands on it) but accepts only a valid stored state. The code is exchanged server-side exactly as documented (`docs/hubrise/02-authentication.md`, confirmed by moyun-pos `oauth.rs`): `POST /token`, `Authorization: Basic base64(client_id:client_secret)`, form body `code=...`. The redirect after success goes to `APP_DASHBOARD_URL`.
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
  autoRefundOnCancel: Boolean!
  lastCatalogSyncAt: DateTime  lastEventAt: DateTime
  authErrorAt: DateTime  lastError: String
  relinkRequired: Boolean!  catalogPendingReview: Boolean!
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
  syncHubriseCatalog(mode: HubriseSyncMode!, confirmRetire: Boolean = false): HubriseSyncResult!
  forgetHubriseLinks: Int!                                       # clears every link, products stay
  syncHubriseInventory: Int!
  linkHubriseProduct(productId: ID!, skuRef: String): Boolean!   # manual adoption; null unlinks
  retryHubriseOutbox(id: ID): Int!
}
```

Additive fields on existing types (clients that do not ask for them are unaffected): `Product.hubriseManaged: Boolean!` (DataLoader backed), `Product.hubriseLockedFields: [String!]!`, `ProductChoice.isAvailable: Boolean!`, `Order.hubrise: HubriseOrderInfo` (@staff: remote order id, push state, remote status, attention). New `apperr` codes (codes are an API, add only): `HUBRISE_MANAGED`, `HUBRISE_NOT_CONFIGURED`, `HUBRISE_NOT_CONNECTED`, `HUBRISE_UPSTREAM`, `HUBRISE_RELINK_REQUIRED`, `HUBRISE_PREVIEW_REQUIRED`, `HUBRISE_RETIRE_CONFIRM_REQUIRED`. Clients render unknown codes with their generic error, so only the dashboard needs to know them.

REST (registered only when available):
- `POST /api/v1/hubrise/callback` (HMAC auth, no bearer).
- `GET /api/v1/hubrise/oauth/callback` (state auth, redirects to the dashboard).

Nothing existing changes shape: no field removed, renamed or made non-null, no enum value added to an enum clients already switch on (`OrderStatusEnum` untouched; new quote issue codes: none).

**Behaviour changes that shipped clients can observe (explicit list).** All of them are inert until the owner enables the matching HubRise feature.

| Change | Who sees it | Mitigation |
|---|---|---|
| `createOrder` / `quoteOrder` refuse a selection of an unavailable choice with the existing `SELECTION_INVALID` | tsb-core web, tsb-mobile already in the stores (they do not query `ProductChoice.isAvailable`, so they cannot grey the choice out beforehand) | existing code and handling, so no crash; clients optionally read `isAvailable` later; the server never lets the order through |
| `HUBRISE_MANAGED` on dashboard / MCP / POS edits of locked fields | tsb-dashboard, tsb-mcp, tsb-pos-android | dashboard release ships first (rollout step 2); MCP surfaces the message; POS has no catalog edits |
| Products or categories hidden or retired by the import (`VAT_UNMAPPED`, removed from HubRise, `restrictions.enabled = false`) | tsb-core web (public URLs), apps | never deleted; slugs generated once and never regenerated; a hidden product behaves like any hidden product today (not listed, direct URL 404); unavailable products stay queryable but not orderable |
| Order status may now change from HubRise | all order clients | same `orderUpdated` events and push notifications as a dashboard change, no new status value |

Compatibility test (T7): the query documents currently used by tsb-core, tsb-mobile, tsb-dashboard and tsb-pos-android (kept as fixtures, no HubRise fields) are replayed against the new schema with a standalone and with an enabled HubRise installation and return identical data and error codes.

## 11. Configuration

New env vars (documented in `.env.example` and CLAUDE.md): `HUBRISE_CLIENT_ID`, `HUBRISE_CLIENT_SECRET`, `HUBRISE_TOKEN_KEY`, optional `HUBRISE_API_BASE_URL` (default `https://api.hubrise.com/v1`), `HUBRISE_OAUTH_BASE_URL` (default `https://manager.hubrise.com/oauth2/v1`; both overridden to the Go mock in tests and local dev). Callback and OAuth redirect URLs derive from the existing `API_BASE_URL`. All timestamps sent to HubRise use `pkg/timezone` (Europe/Brussels), never the runtime zone.

## 12. Failure modes

| Situation | Behaviour |
|---|---|
| HubRise down or slow | Orders still created, paid, shown in the dashboard; outbox retries; nothing user-visible breaks |
| 401 (revoked, unpaid, blocked) | `auth_error_at` + `last_error`, workers pause the pipeline without burning attempts, banner in the dashboard status, Sentry; reconnect clears it |
| 429 | client-side limiter plus backoff; catalog and order-list reads throttled to under 10/min |
| Callback lost | event poller + reconciler + 6 h catalog drift check |
| Connection-level refusal (401 or 403) | pipeline pauses, nothing is parked, banner + Sentry; reconnect resumes |
| Orders changed while a push is backing off | create is built with the current status at send time; queued status rows are covered or sent in FIFO order |
| Reconnect to another location/catalog | blocked until old links are cleared (`HUBRISE_RELINK_REQUIRED`) |
| Remote cancel of an online-paid order | no automatic refund unless `auto_refund_on_cancel`; flagged for staff otherwise |
| Stale rows after a long pause | aged out per kind (section 6.1), never replayed |
| Migration cannot get its lock | `lock_timeout` aborts it, deploy job fails, old pods keep serving, deploy is re-run |
| Two replicas | SKIP LOCKED claims, advisory locks on singleton jobs and on the catalog import |
| Catalog invalid, empty or suspicious | an empty catalog is rejected; retiring more than 30 % of linked products (min 5) needs `confirmRetire` and is never applied by a scheduled import; partial failure rolls back the batch |
| HubRise order never accepted | after 5 min in `new` the order is flagged for staff (visible in `hubriseStatus.attention`) |

## 13. Production rollout and rollback

1. Deploy code with the new env unset: only the additive migrations run (new tables plus six nullable / defaulted columns, no constraint, each behind a 5 s lock timeout); behaviour identical. This is the first release and can sit there. If a migration cannot get its lock, the deploy job fails before the pods roll and is simply re-run.
2. Deploy tsb-dashboard with the HubRise settings page and managed-product UI **before** enabling catalog sync (otherwise a price edit on a managed product returns `HUBRISE_MANAGED` to an unprepared UI).
3. Set `HUBRISE_*` secrets (tsb-infra SOPS), restart. Connect from the dashboard.
4. Enable progressively, each step reversible: orders push (with `LINK_ONLY` adoption first so items carry refs; only open orders from the moment of activation are pushed) -> status sync -> inventory (the first enable pushes the shop's existing sold-out products to HubRise as the baseline) -> catalog sync (requires a `DRY_RUN` of the current catalog within 24 h; the refund-on-cancel switch stays off until the owner decides).
5. Rollback ladder: toggle a feature off; `enabled = false` (everything pauses, locks released); disconnect; unset env (module inert again); goose Down of the migrations (all droppable; the data they hold is HubRise-derived).

## 14. Testing strategy (98 % gate)

- `pkg/hubrise`: unit tests with `httptest` for every endpoint, error classification, rate limiting, money parsing (port the moyun-pos `money.rs` cases: float traps, rejects, round trips).
- `pkg/hubrise/hubrisetest`: in-memory HubRise implementing the used endpoints (OAuth token/revoke, location, catalog GET/PUT, orders POST/GET/PATCH with `private_ref`, inventory GET/PUT/PATCH, callback register, events list/ack) with a control API (inject order status change, replace catalog, change inventory, fault injection, request log) and an HMAC-signing event dispatcher to our callback URL. Fixtures mirror moyun-pos (`pos-hubrise-mock/fixtures`, `pos-hubrise-types/tests/fixtures`).
- Repositories: against the shared PostgreSQL container with real migrations, including Up/Down/Up, a test that M1..M6 apply on a database seeded with the current schema and sample data (counts unchanged, pre-existing orders readable through every order read path with `channel` NULL), a no-missing-versions test, and a lock test (an open transaction holding a lock on `orders` makes the migration fail within `lock_timeout`).
- Contract tests: the Go API types decode every moyun-pos fixture (`pos-hubrise-types/tests/fixtures/*.json`, `pos-hubrise-mock/fixtures`) and the order payload builder output validates against the same shapes; the mock rejects unknown callback event combinations, unknown fields it does not model, and computes `total` like HubRise does.
- Outbox concurrency (T5): crash between send and mark (attempt recorded before the HTTP call, retry does the `private_ref` lookup, no second POST); enqueue-during-send (a newer inventory/status row survives); outage with status changes x3 then recovery (final remote status correct, no backwards PATCH); two workers on one outbox; toggle off, order, toggle on (no historic push); re-enable after 3 days (stale rows aged out); paid webhook delivered 3 times (one row).
- Inbound (T6): event for an order whose POST response was lost (deferred then matched through `private_ref`, status applied); foreign orders cached and never fetched twice; interleaving of a local PATCH and a remote event within 5 s (authoritative re-read); `auto_refund_on_cancel` both modes; 20 signed events burst all 200.
- Inventory (T7): pre-existing manual unavailability survives enabling sync, poll and full sync; provenance flag cleared by a local toggle; baseline push on first enable.
- Catalog (T4): reconnect to another catalog blocked; mass-retire threshold trips and needs confirmation; preview-required rule.
- Limiter: hourly/daily budgets and the priority split (a 100 order lookup backlog does not starve the catalog read).
- Application services: table-driven, fake ports for unit paths, plus end-to-end flows against the mock: connect -> adopt -> import -> push order -> remote `accepted` -> local CONFIRMED + notification fakes -> remote `cancelled` -> refund path; outage -> retry -> park -> manual retry; duplicate and out-of-order callbacks; bad HMAC; token revoked (pipeline paused, nothing parked); a two-instance test (two worker sets and two observers on one database, one mock) asserting exactly one POST per order and one applied transition per event.
- **Standalone parity**: the whole existing suite runs with `Resolver.Hubrise == nil` and must stay green unchanged; plus tests with the module present but `enabled = false` asserting zero HubRise HTTP calls and zero new rows after create/update order and update product; plus a spy test that hooks are the only coupling.
- Locks: every locked field has a rejection test, and a test that the same edit succeeds when disabled.
- Translation survival: manual override kept across three imports; non-catalog languages untouched; the `IS DISTINCT FROM` rule under a full-language resave.
- Coverage discipline: the 98 % gate is checked per new package at the end of each task (`go test -coverprofile` on `pkg/hubrise`, `internal/modules/hubrise/...`), not only globally. Only `pkg/hubrise/hubrisetest` may be excluded from the gate (test support, like `apnstest`); client code, workers and repositories are never excluded, and the mock is exercised through the client tests so it carries no dead production code.
- Commands: `CGO_ENABLED=0 go test ./internal/... ./cmd/... ./pkg/...`, `/opt/homebrew/Cellar/golangci-lint/2.14.0/bin/golangci-lint run --timeout=5m`, `go run github.com/99designs/gqlgen generate`, `go build ./cmd/... ./internal/... ./pkg/...`. CI runs `-race` and the 98 % script.

## 15. Client impact (follow-ups outside this workflow)

- **tsb-dashboard** (needed before catalog sync is enabled): HubRise settings page (connect, disconnect, toggles incl. `autoRefundOnCancel`, language, variant, preview/adopt flow with diff table and the `confirmRetire` prompt, backlog and parked rows with retry, auth-error banner, relink-required and pending-review banners, forget-links action), orders list badge for `needs_attention` (incl. `REMOTE_CANCELLED_PAID` which needs a staff cancel); product dialog: "managed by HubRise" badge, disable locked fields from `hubriseLockedFields`, handle `HUBRISE_MANAGED`, show per-language override state; order detail: push state and attention; i18n fr/en/zh. Everything is additive, so the current dashboard keeps working until sync is enabled.
- **tsb-core (web)** and **tsb-mobile**: nothing required to keep working. Follow-ups: read `ProductChoice.isAvailable` and grey the choice out (until then the server refuses the order line with the existing `SELECTION_INVALID`, which stored app builds already display generically; old app builds cannot be updated, so the server refusal is the guarantee); show unavailable products as sold out rather than hiding them (they stay queryable); `Product.hubriseManaged` is not needed by customer clients. Check the menu cache TTL in tsb-core SSR so a sold-out change is visible quickly. Unaffected by the order or status behaviour.
- **tsb-pos-android**: nothing required (status changes arrive through the existing `orderUpdated` subscription). Optional HubRise badge.
- **tsb-mcp** (in this repo): propose_* tools on managed products will surface `HUBRISE_MANAGED`; add the managed flag to `get_product` and mention it in `docs/mcp`.
- **tsb-infra**: new SOPS secrets and env, route `/api/v1/hubrise/*` (already covered by `/api/v1/*` in the tunnel), no WAF rule blocking HubRise callbacks.
- **moyun-pos**: no change; this plan consumes its existing catalog conventions (`custom_fields.code`, `vat_category`, `translations`) and expects the `webshop`/channel and payment refs described in section 6.2 (open question 5).

## 16. Task breakdown

Each task is one committable increment: build green, lint clean, tests passing, per-package coverage at or above 98 %, standalone parity intact, migrations strictly newer than every earlier task's. Conventional Commits. Detailed specs are in the structured output of the planner; summary (T3 is the behaviour-preserving refactor and is merged before any HubRise behaviour):

- **T1 Schema, domain, storage, crypto, config, test seeding.** Migrations M1, M2, M3, domain entities and ports, sqlx repositories on the Admin pool, AES-GCM token store, job leases, `Module` constructor that returns nil when unconfigured, env parsing, and a `testhelpers` function that seeds a connected `hubrise_connection` row (encrypted token pointing at the mock) used by T4 to T9.
- **T2 HubRise API client and Go mock server.** `pkg/hubrise` (+ OAuth, errors, multi-window priority limiter, money) and `pkg/hubrise/hubrisetest` with fixtures and control API; fixtures and docs read first.
- **T3 Order transition refactor (characterization first).** Extract the `UpdateOrder` orchestration into a reusable `ApplyOrderUpdate`, add the optional `StatusObserver` port on `orderService.UpdateOrder`, the origin marker in the context, and a characterization test per order_status writer. No HubRise behaviour.
- **T4 Catalog import.** Migrations M4, M5; translation `source` rules; neutral catalog mapping; planner and applier (`DRY_RUN`, `LINK_ONLY`, `FULL`); preview-required, empty and mass-retire guards; reconnect/relink rule in the service API; product ownership guard and `HUBRISE_MANAGED`; unavailable-choice refusal; optional images.
- **T5 Outbox and order push.** Migration M6; `OrderReadyHook` in `CreateOrder` and the paid webhook; send-time payload builder; outbox worker (claim with lease and attempts, FIFO, classification, max age); idempotent create; `push_orders_since`; reconciler catch-up and status diff.
- **T6 Callbacks, event processor and status sync.** Callback endpoint (HMAC, failed-verification limiter), event store and processor (defer, foreign cache, authoritative re-read), inbound/outbound status mapping, `auto_refund_on_cancel`, terminal guards.
- **T7 Inventory.** Inbound apply with provenance, baseline push, expiry sweeper, outbound availability, option sold-out, poll and drift jobs.
- **T8 Connection management and GraphQL admin API.** OAuth endpoints, settings, disconnect, relink guard, status/outbox queries, preview/sync/link/forget mutations, additive Product/Order fields, audit entries, apperr codes, old-client query compatibility test.
- **T9 Wiring, workers, end-to-end, docs.** `cmd/app/hubrise.go`, graceful shutdown, `.env.example`, CLAUDE.md, `docs/hubrise/README.md` (operator runbook: connect, adopt, rollout ladder, ROUNDING line, tag and custom field conventions), full-flow, two-instance and parity tests.

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
10. Remote `cancelled` / `rejected` on an online-paid order: automatic Mollie refund (`auto_refund_on_cancel`) or staff confirmation first? The plan defaults to staff confirmation (an EPOS mis-tap must not move money).

## 18. Review resolutions (second pass)

Two adversarial reviews were checked against the code and the docs. Accepted items are folded into the sections above. Items rejected or narrowed:

- **"order_product_choices cascades on the choice side" (rejected).** The migration shows plain FKs without cascade (section 2). The cascade the reviewer saw is on the translation table and on `product_choices.choice_group_id`. The importer rule "never delete, only flip flags" stays regardless.
- **"OAuth token exchange must send client_id/secret/code in the form body" (rejected).** The official docs in `docs/hubrise/02-authentication.md` and moyun-pos `oauth.rs` both specify HTTP Basic plus a `code=` form body, which is what the plan uses.
- **"order/own_order update event" (not in the plan).** The plan never named it; section 7.2 now pins the documented combinations and makes the mock reject others.
- **"HubRise stores the total the client sends" (rejected).** The docs define `total` as calculated (items + charges - discounts). The plan compares the computed response total with the local total instead.
- **"Single-order reads burn the 10/min bucket" (narrowed).** Only `GET /catalogs/:id` and the order LIST endpoint (including `?private_ref=`) are in the 10/min bucket; `GET /orders/:id` is not. Hourly and daily budgets and the priority split were nevertheless added (section 3.4).
- **"Callback rate limit keyed on the proxy address" (verified not the case).** `TrustedPlatform = PlatformCloudflare` makes `ClientIP()` the CF-Connecting-IP. The limiter was still narrowed to failed verifications only.
- **CHECK NOT VALID + VALIDATE in one goose transaction (accepted).** No CHECK constraints on existing tables at all.
- **Hooks only inside the resolver (accepted, blocker).** The observer moved to `orderService.UpdateOrder`, the verified single writer (section 2), with a reconciler diff as a safety net.


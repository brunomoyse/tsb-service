# HubRise integration: operator runbook

HubRise connects the webshop to the restaurant's EPOS (the till). This runbook is for the person who
turns it on and keeps it running. The design is in [PLAN.md](PLAN.md), the facts about the HubRise API
in [API_NOTES.md](API_NOTES.md).

What it does, each part optional and switchable on its own:

- **Orders**: every web and app order is pushed to HubRise once it can be prepared (a cash order when it
  is placed, an online order when Mollie confirms the payment).
- **Status**: what the EPOS does with an order (accepted, in preparation, ready, completed, cancelled)
  comes back and moves the order here, with the same e-mails, push notifications and dashboard updates
  as a change made in the dashboard. A status changed in the dashboard goes to HubRise.
- **Customers**: registered customers are kept in the HubRise customer list.
- **Inventory**: a product sold out at HubRise cannot be ordered here, and the sold-out toggle of the
  dashboard is sent to HubRise.
- **Catalog**: the HubRise catalog becomes the menu (products, prices, option lists, categories, menu
  codes).

The one rule: **HubRise is an addition and never in the way.** Placing an order, paying and reading the
menu work the same when HubRise is not configured, switched off, down or refusing. Everything HubRise
related happens in the background, from a durable queue.

## 1. Setting it up

### 1.1 Register the OAuth client (once, at HubRise)

1. In the HubRise developer portal create a client. The redirect URI is
   `<API_BASE_URL>/hubrise/oauth/callback`, for example
   `https://tokyosushibarliege.be/api/v1/hubrise/oauth/callback`. One client can serve several brands
   (each deployment has its own redirect URI, add them all), or create one per brand.
2. Note the client id and the client secret.
3. Generate the key that seals the access token in the database: `openssl rand -base64 32`.

### 1.2 Configure the deployment

Set these three variables on the API (tsb-infra secrets) and restart. HubRise exists on the deployment
only when **all three** are set; with none of them the shop is the standalone shop, byte for byte.

| Variable | Meaning |
|---|---|
| `HUBRISE_CLIENT_ID` | OAuth client id |
| `HUBRISE_CLIENT_SECRET` | OAuth client secret. It also signs the callbacks HubRise posts to us |
| `HUBRISE_TOKEN_KEY` | base64 of 32 random bytes, seals the access token. Losing or changing it makes the stored token unreadable: reconnect from the dashboard |
| `HUBRISE_REPLICAS` | optional, default 2: the API replicas that share the HubRise rate budget |
| `HUBRISE_API_BASE_URL`, `HUBRISE_OAUTH_BASE_URL` | optional, default to the real HubRise; only for development against another server |

Also needed and already set on a working API: `API_BASE_URL` (public URL of the API, with `/api/v1`, it is
where HubRise is told to call) and `APP_DASHBOARD_URL`. `ASSISTANT_ALERT_EMAIL` receives the e-mail about
an order the EPOS never accepted.

A malformed value (a key that is not 32 bytes, a base URL that is not http or https) is logged as an
error at start and the integration stays off; the shop starts normally. A partly configured deployment
logs a warning naming the missing variables.

Behind the tunnel nothing special is needed: `/api/v1/*` already reaches the API, and no WAF rule may
block `POST /api/v1/hubrise/callback`.

### 1.3 Connect (dashboard, HubRise page)

The admin starts the connection (`startHubriseConnection` gives the HubRise authorization URL), approves
the scopes `orders.write`, `customer_list.write`, `catalog.read`, `inventory.read` and `inventory.write`
at HubRise, and the browser comes back to `<API_BASE_URL>/hubrise/oauth/callback`. That endpoint stores
the sealed token, registers our callback at HubRise (orders `update`, catalog `create/update/delete`,
inventory `update/patch`) and sends the browser to `<APP_DASHBOARD_URL>/hubrise?hubrise=<outcome>`, with
the outcome `connected`, `relink_required`, `denied`, `invalid_state`, `authorization_failed` or `error`.
Nothing else travels in that address.

Connected is **not** enabled: the connection starts paused. Every feature switch (`pushOrders`,
`syncStatus`, `syncInventory`, `syncCustomers`) is already on, though, so `enabled` is the moment they all
start: switch the ones that must wait off in the same call that sets `enabled` (section 2, step 4). The status shows the account, location, catalog and customer list names and
ids to give HubRise when they review the integration.

Disconnecting revokes the token at HubRise, deletes the callback and forgets the token. The settings and
the links stay, so connecting again to the same location resumes where it was.

## 2. The rollout ladder

Each step can be undone by the one below it in section 3. Do them in this order and look at the result of
each before the next.

1. **Deploy with the variables unset.** Only additive migrations run (new tables, six new columns). The
   behaviour is unchanged. This release can stay like that. The deploy job migrates right AFTER the new
   pods roll, and until it finishes the menu, ordering and product edits fail on the old schema
   (`docs/hubrise/PLAN.md` A10), so deploy off-peak and check that the migration step went green.
2. **Deploy the dashboard** with the HubRise page and the "managed by HubRise" product states **before**
   catalog sync is ever enabled, otherwise a price edit on a managed product answers `HUBRISE_MANAGED` to a
   screen that does not know the code.
3. **Set the variables, restart, connect.**
4. **Adopt the menu you already have (`LINK_ONLY`).** An import is refused (`HUBRISE_NOT_ENABLED`) while the
   integration is paused, and turning `enabled` on starts every feature switch that is on, so do both in one
   call: `updateHubriseSettings(input: {enabled: true, pushOrders: false, syncStatus: false,
   syncInventory: false, syncCustomers: false})`. Nothing leaves yet; an order placed now would carry no SKU
   refs, which is why `pushOrders` waits for the links. `previewHubriseCatalogSync(mode: LINK_ONLY)` lists
   the products that match a HubRise SKU; `syncHubriseCatalog(mode: LINK_ONLY)` records the links and
   touches no product data. This gives the orders their SKU refs. A product matches by the menu code
   (`custom_fields.code` of the SKU equals `products.code`), then by its normalized name inside the
   category of the same name. Anything ambiguous is listed as a conflict and never guessed; link it by
   hand with `linkHubriseProduct`.
5. **Turn on `pushOrders`.** Only the open orders from this moment (and the last hour,
   for the ones in flight) are pushed. History is never replayed. Place a test order and watch it in the
   EPOS. Then `syncStatus`, `syncCustomers` and `syncInventory`; the first time inventory sync becomes
   effective, the products the shop already marks sold out are pushed to HubRise as the baseline.
6. **Preview the catalog (`DRY_RUN`), then turn on `syncCatalog`.** It is refused (`HUBRISE_PREVIEW_REQUIRED`)
   unless a dry run of the current catalog was made in the last 24 hours. Read the diff: what is created,
   updated, linked, retired, and the warnings. Then `syncHubriseCatalog(mode: FULL)`.
   From then on HubRise owns price, category, VAT category, menu code, option groups and options of the linked
   products; the dashboard answers `HUBRISE_MANAGED` for those. Names and descriptions in the catalog language
   can be edited (the edit sticks across imports), other languages and the flags are local.
7. **Decide the cancel policy.** `autoRefundOnCancel` stays off by default: a cancel at the EPOS of an order
   already paid online does not move money, it flags the order (section 4) and staff confirm by cancelling
   in the dashboard. Turn it on only when the owner accepts that a mis-tap at the till refunds a customer.
8. **Automatic catalog import** (`catalogAutoImport`) is a separate choice: with it off, a change in the
   HubRise catalog only shows an "update available" banner and the admin imports with the button.

Catalog imports never delete anything: removed products and options are retired (hidden and unavailable),
and come back if the SKU reappears. An import that would retire more than 30 % of the linked products (and at
least 5) needs `confirmRetire`, and a scheduled import never applies it: it stops and waits for a person.
An empty HubRise catalog is refused.

## 3. The rollback ladder

From the lightest to the heaviest; each step keeps the data of the previous ones.

1. **Switch one feature off** (`pushOrders`, `syncStatus`, `syncInventory`, `syncCustomers`,
   `syncCatalog`). Locked fields are unlocked when `syncCatalog` is off. Nothing is deleted.
2. **`enabled` off.** Everything pauses: nothing leaves, nothing is imported, nothing is locked. The queue
   and the links are kept. Switching it on again does not replay anything: rows that waited too long are
   aged out (an order creation after 48 h is parked and flagged, a status after 6 h and an inventory change
   after 24 h are skipped; the reconciler re-sends the right status if the order is still open).
3. **Disconnect** (`disconnectHubrise`). Token revoked and forgotten; with `forgetLinks: true` the links are
   deleted too (the products stay, they become ordinary local products).
4. **Unset the variables and restart.** The module is not built: no route, hook or worker.
5. **Down-migrate.** The HubRise migrations are all droppable; the data they hold comes from HubRise.

## 4. What the order flags mean (`needs_attention`)

The dashboard order list shows an order with a flag; `hubriseStatus.attention` counts them. The first
problem found stays on the order.

| Code | Meaning | What to do |
|---|---|---|
| `UNLINKED_ITEM` | an item or option of the order has no HubRise ref, so the EPOS may ignore it | adopt the product (section 2 step 4) and tell the kitchen about this order |
| `TOTAL_MISMATCH` | the total HubRise calculated differs from ours | compare the order in both systems; usually a price changed at HubRise |
| `PUSH_PARKED` | HubRise refused the order (a 400 or 422) or it grew too old | read the message in `hubriseOutbox`, fix the cause, `retryHubriseOutbox` |
| `NOT_ACCEPTED` | still `new` at HubRise after 5 minutes, for an order pushed within ordering hours (needs the status sync on; the alert is retried until the mail goes out) | check the EPOS; an e-mail went to `ASSISTANT_ALERT_EMAIL` |
| `REMOTE_MISSING` | HubRise no longer knows the order (the periodic read finds it gone, or it was lost again after the shop had already created it a second time) | check at HubRise; a first loss is repaired by creating the order once more |
| `REMOTE_CANCELLED_PAID` | cancelled or rejected at the EPOS, but it was paid online and `autoRefundOnCancel` is off | cancel the order in the dashboard if the cancel is right; that refunds the customer and is not sent back to HubRise |
| `REMOTE_CANCEL_FAILED` | cancelled at the EPOS and we could not cancel it here (the payment cannot be refunded through Mollie, for example a voucher) | settle the payment by hand, then cancel |
| `DELIVERY_FAILED` | the delivery failed according to HubRise | staff decide about the order and the payment; nothing changes by itself |
| `STATUS_CONFLICT` | the EPOS moved an order that is already finished here | the shop keeps its status; check why the EPOS reopened it |

Other things to look at on the HubRise page: `authErrorAt` and `lastError` (a 401 or 403 pauses the whole
pipeline without parking anything: the token was revoked, the account is unpaid or a scope is missing;
reconnect), the backlog (pending and parked rows, with `retryHubriseOutbox` to retry a parked chain), the
relink banner and the "catalog update pending review" banner.

## 5. Conventions on the HubRise side

What the importer and the order push expect, mostly the conventions of the owner's POS (moyun-pos).

**Catalog**

- One local product per SKU. The name is the product name, followed by the SKU name when the product has
  several SKUs or the SKU is named.
- SKU `custom_fields.code`: the menu code (such as `A2`); it is kept only when present and never blanks
  an existing local code. It is also how an existing product is adopted.
- SKU `custom_fields.vat_category`: `food`, `beverage`, `zero_rated` or `out_of_scope`. Without it the VAT
  category comes from the tax rates (collection and eat-in 6 and 12 give food, 21 and 21 beverage, all 0
  zero rated). Anything else is `VAT_UNMAPPED`: the product is created hidden and listed in the report.
- SKU `custom_fields.translations.{fr,en,nl,zh}.{name,description}` and a category `description` holding
  `{"translations":{"fr":...}}` give the other languages. Without them only the catalog language
  (default `fr`, setting `catalogLanguage`) is written.
- Tags on products and SKUs seed a new product only: `hidden` or `web:hidden` (not visible), `halal`,
  `vegetarian` or `vegan`, `spicy`, `no-discount`.
- `restrictions.enabled = false` hides a product or disables an option. Time windows, days of the week and
  customer restrictions are ignored in this version (and reported). `price_overrides` apply for the
  configured `variantRef`, the last matching rule wins; those with time windows are ignored.
- Option lists become one group per SKU and list: the minimum from `min_selections`, the maximum from
  `max_selections` (none or 0 gives the number of options, or 99 when the same option can be picked
  several times). A negative option price is clamped to 0 and reported.
- Deals, discounts, charges, nutrition and barcodes are not imported.
- Product images are imported only with `importImages` on, and only when their checksum changed.

**Orders**

- `private_ref` is the order id (this makes a retry safe: before sending again the shop looks the order
  up), `ref` the first 8 characters of the id in upper case.
- `channel` is the setting `channelName`, otherwise `webshop` for the website and `app` for the apps.
- Every item carries `sku_ref` and `option` refs; an identical option chosen several times is sent as
  several options of quantity 1. The item `price` is the base price of the SKU; options carry their own.
- Charges: `DELIVERY` (delivery fee) and `PAYMENT_FEE` (online payment fee), both refs overridable in the
  settings. Discounts: `TAKEAWAY` (pickup discount) and the coupon code.
- Payments: always at least one. Online: `MOLLIE` with the Mollie payment id in `info`. Cash:
  `CASH`, which the EPOS treats as not yet paid, so it collects.
- **The ROUNDING line.** Our total is rounded to 0.10 EUR, and a coupon can cover the whole basket, while
  HubRise computes its total as items plus charges minus discounts. A charge or a discount named
  "Rounding" with the ref `ROUNDING` carries the difference so that the two totals are equal to the cent.
  Kitchen staff who see it on the ticket should know it is not an item.
- `expected_time` is sent only when it is in the future, otherwise `asap` is set. Times are Europe/Brussels.
- Test orders (store review) and finished orders are never pushed.

## 6. Reconnecting to another location or catalog

The links name the refs of one catalog. When the authorization points at a different location or catalog
and links exist, the connection is stored **disabled** (`enabled`, `syncCatalog` and `syncInventory` off)
with the relink flag, and the browser returns with `?hubrise=relink_required`. Enabling is refused
(`HUBRISE_RELINK_REQUIRED`) until the old links are gone:

1. `forgetHubriseLinks` (or `disconnectHubrise(forgetLinks: true)` before reconnecting). Products stay.
2. Adopt again against the new catalog (section 2 step 4), then enable.

The old token is revoked only after the new connection is stored.

## 7. Operating notes

- Production runs two replicas; both run every worker. Queues are claimed with `FOR UPDATE SKIP LOCKED`,
  the singleton jobs (reconciler, callback poller, inventory read, sold-out sweeper, catalog drift,
  catalog import) take a lease row in `hubrise_job_leases`. A worker that panics is logged, reported to
  Sentry and restarted.
- Workers: outbox drain (every row that leaves), event processor, reconciler (2 minutes: catch-up of
  missed pushes, status differences, five order refreshes, orders stuck in `new`, erasures the deletion hook missed, purge of finished outbox rows and of the customer data they froze), callback poller
  (10 minutes, picks up events HubRise could not deliver), inventory read (30 minutes and on every
  inventory event), sold-out sweeper (1 minute), catalog drift (6 hours and on catalog events, debounced).
- HubRise limits: 500 calls a minute, 2,500 an hour, 10,000 a day, and 10 a minute for catalog and order
  lists. The client keeps under 400, 2,000 and 8,000 divided by `HUBRISE_REPLICAS`, and background work
  stops at 60 % of the hour and day budgets so an order is never starved.
- The callback endpoint answers 200 for a valid signature (HMAC-SHA256 of the raw body keyed with the client
  secret), 401 for a bad one (30 a minute per client address are tolerated before 429), and never limits a
  valid one: HubRise drops an event after six retries.
- Rows in the queue are visible with `hubriseOutbox(state: PARKED)`; a retry reactivates the whole parked
  chain of an order so a status is never sent for an order HubRise never received.

## 8. Follow-ups outside this repository

- **tsb-dashboard** (before catalog sync is enabled): the HubRise page (connect, disconnect, the toggles
  incl. `autoRefundOnCancel`, language, variant, preview and import with the diff and the `confirmRetire`
  prompt, backlog with retry, the auth-error, relink-required and update-available banners, forget links),
  the order list badge and detail for `needs_attention` and the HubRise ids, the "managed by HubRise" product
  state driven by `hubriseManaged` and `hubriseLockedFields`, the new error codes (`HUBRISE_MANAGED`,
  `HUBRISE_PREVIEW_REQUIRED`, `HUBRISE_RELINK_REQUIRED`, `HUBRISE_RETIRE_CONFIRM_REQUIRED`,
  `HUBRISE_NOT_CONFIGURED`, `HUBRISE_NOT_CONNECTED`, `HUBRISE_NOT_ENABLED`, `HUBRISE_EMPTY_CATALOG`,
  `HUBRISE_BUSY`, `HUBRISE_SKU_LINKED`, `HUBRISE_UPSTREAM`), and fr, en and zh strings. The OAuth return
  lands on `<APP_DASHBOARD_URL>/hubrise?hubrise=<outcome>`.
- **tsb-core and tsb-mobile**: nothing is required. Optionally read `ProductChoice.isAvailable` to grey
  out a sold-out option (until then the server refuses the order line with the existing `SELECTION_INVALID`),
  and show unavailable products as sold out instead of hiding them.
- **tsb-mcp**: edits of managed products answer `HUBRISE_MANAGED`; the managed flag could be added to
  `get_product`.
- **tsb-infra**: the three secrets (and `HUBRISE_REPLICAS` if it is not 2), `API_BASE_URL` with `/api/v1`.
- **HubRise**: the integration review (a call of about 90 minutes) and the Integration Sheet; contact
  integration@hubrise.com. Test orders are free during development.

# TSB MCP server: implementation plan (step 2)

This plan builds on `docs/mcp/INVENTORY.md`. Nothing is built until you approve it.

## Where it lives

The MCP server is a second binary in `tsb-service`, on branch `feat/mcp-server`. It reaches the backend **only over HTTP** (`POST /api/v1/graphql`), exactly like the dashboard. A test fails the build if any `internal/mcp/...` package imports `tsb-service/internal/modules/...` or `pkg/db`.

```
cmd/tsb-mcp/main.go          flags: --http :8080, --internal :8081 (stdio when --http is absent)
internal/mcp/config/         env parsing + validation
internal/mcp/upstream/       GraphQL client, named documents, client-credentials token source, error mapping
internal/mcp/money/          price_cents <-> upstream decimal string (shopspring/decimal, already a dependency)
internal/mcp/search/         normalisation + ranking
internal/mcp/changes/        SQLite store: pending changes, audit log, apply/undo logic
internal/mcp/tools/          one file per domain: status, products, choices, restaurant, coupons, orders
internal/mcp/internalapi/    apply / reject / get endpoints
deploy/k8s/tsb-mcp/          Deployment, Service, Secret template, PVC
Dockerfile.mcp               distroless/static:nonroot, CGO_ENABLED=0
```

Dependencies:
- `github.com/modelcontextprotocol/go-sdk` v1.8.0
- `modernc.org/sqlite` v1.60.1
- `golang.org/x/oauth2/clientcredentials` (already an indirect dependency)

Go is now 1.27 (commit `334ffdc`).

## Backend change: machine users (separate commit, before the MCP)

- `internal/api/auth/helpers.go` `GetZitadelUserInfo`: also parse `user.machine` (`username`, `name`). For a machine user, return:
  - email `<username>@machine.invalid`, which is unique per machine user and undeliverable by design;
  - `givenName` = machine `name`;
  - `familyName` = `"bot"`.
- Effect: the JIT insert no longer collides on `email=''`, and because the email is non-empty, the middleware stops calling Zitadel on every request.
- Test in `internal/modules/user/application`: a machine user without email is provisioned once and found by `zitadel_user_id` afterwards.

## Service account (Zitadel)

- **Machine user** `tsb-mcp`:
  - access token type **JWT**;
  - a client secret;
  - the `admin` project role. There is no narrower role that can write the catalogue.
- **Token request:** the client-credentials grant against `${ZITADEL_ISSUER}/oauth/v2/token`, with scopes `openid urn:zitadel:iam:org:project:id:${ZITADEL_PROJECT_ID}:aud urn:zitadel:iam:org:projects:roles`. Tokens are cached by `oauth2.ReuseTokenSource` and refreshed before expiry.
- **Backend config:** append the machine user's client id to `ZITADEL_ADMIN_CLIENT_IDS` in tsb-service.
- **Creating the user:** done in the Zitadel Terraform in `tsb-infra`. That is outside this branch; I'll give you the snippet.
- **Docker/k3s internal URL gotcha:** `ZITADEL_INTERNAL_URL` is supported with the public `Host` header, as in tsb-service.

## Tools

**Read**
- `get_status`
- `get_restaurant_config`
- `list_schedule_overrides(from?, to?)`
- `search_products(query, limit=10)`
- `get_product(product_id)`
- `list_categories`
- `search_coupons(query)`
- `get_coupon(coupon_id)`
- `list_orders(from?, to?, status?, limit=20)`
- `get_order(order_id)`
- `get_daily_summary(date?)`
- `get_customer_stats(from?, to?)`: aggregates only, no names or phones

**Low** (applied immediately and audited; repeated identical calls return `no_op: true`)
- `set_product_availability(product_id, available)`
- `set_product_visibility(product_id, visible)`
- `set_preparation_minutes(minutes)`
- `deactivate_coupon(coupon_id)`

**Sensitive** (each one only creates a pending change)
- Products:
  - `propose_price_change(product_id, new_price_cents)`
  - `propose_product_update(product_id, fields...)`
  - `propose_product_creation(...)`
  - `propose_vat_category_change(product_id, vat_category)`
  - `propose_product_image(product_id, image_url, remove_background?)`
  - `propose_bulk_availability(product_ids[], available)`
- Choices:
  - `propose_choice_group_change(...)`
  - `propose_choice_change(...)`
  - `propose_choice_group_deletion(id)`
  - `propose_choice_deletion(id)`
- Restaurant:
  - `propose_restaurant_closure(from?, reopen_at?, reason?)`
  - `propose_restaurant_reopening()`
  - `propose_opening_hours(week)`
  - `propose_ordering_hours(week)`
  - `propose_schedule_override(date, closed, hours?, note?)`
  - `propose_schedule_override_removal(date)`
- Coupons:
  - `propose_coupon_creation(...)`
  - `propose_coupon_update(coupon_id, fields...)`
  - `propose_coupon_activation(coupon_id)`

**Undo**
- `undo_last_change()`

There is no MCP tool that applies or confirms a change, and none that touches order writes. A test asserts that no registered tool's GraphQL documents contain `updateOrder`, `createOrder`, `updatePaymentStatus`, the device-token or live-activity mutations, `updateMe` or `deleteMe`.

### Closure semantics (decision c)

| Owner says | `propose_restaurant_closure` input | Change applied on confirmation |
|---|---|---|
| "close now" | no `reopen_at` | `updateOrderingEnabled(false)`; reopening stays manual |
| "close at 15:00, back at 18:00" | `from: 15:00`, `reopen_at: 18:00` | Override for today: the regular hours minus the 15:00 to 18:00 window, with `reason` as the note |
| "closed until Friday 18:00" | `reopen_at` on a later day | `closed: true` overrides for each full day, then a partial override on the reopen day |

Override rules:
- `reopen_at` must be in the future and at most 7 days ahead.
- Existing overrides on those dates are part of the before-state.

`propose_restaurant_reopening` re-enables ordering if it is off, and removes the overrides for today and later days that were created by an MCP closure (tracked in the audit log). It never removes overrides created in the dashboard.

### Products, orders and summaries

- **Prices:** every price is returned as `price_cents` (integer) plus `currency: "EUR"`. Upstream uses decimal strings, and the conversion is exact (2 decimals; any other precision is rejected).
- **Price guard:** rejects 0, negative values, and changes beyond ±`PRICE_MAX_CHANGE_PCT`% (default 50) of the current price. The error message states the allowed range. The same bounds apply to choice price modifiers, except that 0 is allowed there.
- **`propose_product_image`:**
  - `image_url` must be https;
  - the file must be under 5 MB and of type `image/jpeg`, `image/png` or `image/webp`;
  - it is downloaded at proposal time and stored with the pending change, so what you confirm is what gets uploaded;
  - after applying, the MCP re-checks that the image exists, because upstream only logs upload failures.
- **`list_orders`:**
  - with dates, it uses `orderHistory`, which excludes CANCELLED and FAILED;
  - without dates, or with `status` CANCELLED/FAILED, it uses `orders`, the latest 200 orders, and says so in the output.
- **`get_daily_summary`:** sums `orderHistory` for one Brussels day and excludes PENDING. The output contains the counts by status, `revenue_cents` and `average_cents`.

## Search

- **Normalisation:** NFKD, accents stripped, lower case, and full-width characters folded to half-width.
- **Fields searched:** the name in every language (fr/en/zh/nl), the product code, and the category name.
- **Ranking:**
  1. exact match
  2. prefix match
  3. word-prefix match
  4. substring match
  5. subsequence/edit-distance ≤ 1 for Latin tokens of length ≥ 4
- **Chinese:** substring matching on CJK characters, with no segmentation.
- **Ties:** broken by available first, then by name.
- **Data source:** the catalogue is cached for 30 s and invalidated on any MCP write.

## Pending changes and audit (SQLite, WAL)

```
changes(
  id TEXT PRIMARY KEY,           -- ULID
  tool TEXT, kind TEXT,          -- e.g. propose_price_change / product.price
  entity_type TEXT, entity_id TEXT,
  params JSON,                   -- validated input
  plan JSON,                     -- the exact upstream mutations to run
  before JSON,                   -- relevant upstream fields at proposal time
  before_hash TEXT,              -- compared at apply time for conflicts
  blob BLOB NULL,                -- image bytes for propose_product_image
  summary TEXT, request_context TEXT,
  status TEXT,                   -- pending|applied|rejected|expired|conflict|failed
  undo_of TEXT NULL,             -- set when the change is an undo
  created_at, expires_at, decided_at, error TEXT
)
audit_log(
  id INTEGER PRIMARY KEY, ts, source TEXT,  -- tool name or internal endpoint
  change_id TEXT NULL, entity_type, entity_id,
  before JSON, after JSON, request_context TEXT,
  outcome TEXT,                  -- applied|rejected|failed|undone
  undone_by INTEGER NULL
)
```

- **Apply:**
  1. Load the change.
  2. Check that its status is pending and that it has not expired.
  3. Re-read the upstream state and compare it with `before_hash`. A mismatch sets status `conflict` and returns 409 with the fields that changed.
  4. Run `plan` in order.
  5. Re-read the state and write an audit row.
- **Partial failure** (a multi-mutation plan that fails halfway): status `failed`, plus an audit row that lists what was applied.
- **Concurrency:** one apply runs at a time (a mutex plus a `status = 'pending'` guard in the UPDATE).
- **Undo:** takes the latest audit row that has `outcome = applied`, is less than 30 minutes old and has not been undone.
  - A low change is reverted directly.
  - A sensitive change becomes a new pending change with `undo_of` set.
  - Some changes can't be reverted. Choice deletions and image changes return "cannot be undone". Product and coupon creation are undone by hiding the product or deactivating the coupon.

## Internal API (`--internal :8081`, `INTERNAL_API_TOKEN`)

- `GET /internal/changes/{id}`
- `POST /internal/changes/{id}/apply` with body `{"request_context": "..."}`
- `POST /internal/changes/{id}/reject` with the same body

Responses:

| Status | Meaning |
|---|---|
| 200 | the change, with its new status and summary |
| 401 | wrong token |
| 404 | unknown id |
| 409 | conflict, or already applied/rejected |
| 410 | expired |
| 502 | upstream failure |

This listener is never mounted on the MCP handler. The MCP HTTP transport uses `auth.RequireBearerToken` with `MCP_AUTH_TOKEN`, compared in constant time.

## Configuration

| Variable | Notes |
|---|---|
| `UPSTREAM_BASE_URL` | the backend URL |
| `ZITADEL_ISSUER`, `ZITADEL_CLIENT_ID`, `ZITADEL_CLIENT_SECRET`, `ZITADEL_PROJECT_ID`, `ZITADEL_INTERNAL_URL` | the last one is optional |
| `MCP_AUTH_TOKEN` | required when `--http` is set |
| `INTERNAL_API_TOKEN` | |
| `DB_PATH` | |
| `PENDING_TTL` | default `10m` |
| `PRICE_MAX_CHANGE_PCT` | default 50 |
| `TZ_DEFAULT` | default `Europe/Brussels` |
| `LOG_LEVEL` | |

Logging uses `log/slog`, as the brief asks. That differs from tsb-service, which uses zap, but it keeps the MCP self-contained.

## Tests (`go test -race ./...`)

- **Fake upstream:** an `httptest` GraphQL server that answers by operation name. Fixtures are copied from real tsb-service response shapes.
- **Table-driven unit tests:** search (Chinese, accents, partial words, ranking order), money conversion, price bounds, closure validation and override computation, expiry, conflict hash.
- **Integration:** every tool end to end through `mcp.NewInMemoryTransports`.
- **Internal API:** apply, reject, expired, already applied, wrong token, upstream changed in between, partial failure.
- **Guard tests:** no order-write documents; no imports of the backend's domain packages.

## Commit sequence

1. `fix(user): provision Zitadel machine users with a synthetic email`
2. `feat(mcp): config, upstream client and service account auth`
3. `feat(mcp): money and search packages`
4. `feat(mcp): pending change store and audit log`
5. `feat(mcp): read tools`
6. `feat(mcp): low-risk tools`
7. `feat(mcp): sensitive propose tools and undo`
8. `feat(mcp): internal apply/reject API`
9. `feat(mcp): stdio and streamable HTTP entrypoint`
10. `chore(mcp): Dockerfile, k8s manifests, Makefile, README, .env.example`

## Open questions

1. VAT category edits and product images are in scope as sensitive tools (see the inventory). OK?
2. **Deployment:** the brief asks for plain manifests in `deploy/k8s/tsb-mcp/`, but prod apps are deployed through the Helm chart in `tsb-infra/charts/tokyosushi-apps`. Do you want plain manifests only (as asked), or also a chart entry later?
3. **CI:** should tsb-service's image workflow also build `Dockerfile.mcp`? Each build costs Actions minutes.
4. **Customer stats:** aggregates only (totals, new vs returning, top order counts with no names). OK?

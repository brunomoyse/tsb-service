# tsb-mcp

An MCP server that lets the Tokyo Sushi Bar owner run the dashboard from chat (WeChat ClawBot through an agent service). It exposes narrow, typed tools for the catalogue, opening hours, closures, coupons and order reads.

```
WeChat ClawBot (iLink) / CLI
  -> agent service (LLM + confirmation logic)
  -> tsb-mcp (this)                     cmd/tsb-mcp, internal/mcp
  -> tsb-service GraphQL API            POST /api/v1/graphql (same API as the dashboard)
```

Design notes:

- **Same API as the dashboard.** tsb-mcp only talks to tsb-service over HTTP. Every backend side effect keeps happening: live dashboard updates through pubsub, `staff_audit_log` rows (operation names `Mcp*`), and file service uploads. A guard test (`internal/mcp/architecture_test.go`) fails if MCP code imports the backend's modules or database.
- **Orders are read-only.** No tool can create, change, cancel or refund an order. `TestNoOrderWrites` checks the GraphQL document registry. The tool list test checks the names.
- **Sensitive changes are two-step.**
  - A `propose_*` tool only stores a pending change. It returns `{change_id, summary, summary_zh, expires_at}`.
  - The agent shows the summary to the owner, and on "yes" calls the internal API. No MCP tool can apply a change.
  - Before applying, the current state is read again. If it changed since the proposal (for example, someone edited it in the dashboard), the apply is refused with a conflict.
- **Summaries in English and Chinese.** Every proposal, applied change, audit entry and undo carries `summary` (English, for the model) and `summary_zh` (Chinese, for the owner). Both are built in code from fixed templates (`internal/mcp/actions/zh.go`), never by a model, so the owner confirms exactly what will be applied. Names use the Chinese translation and fall back to the French name.
- **Low-risk changes apply immediately.** Availability, visibility, preparation time and coupon deactivation are applied at once and audited. A repeated identical call is a no-op.

See `INVENTORY.md` for every dashboard action and its risk class, and `PLAN.md` for the design.

## Tools

| Kind | Tools |
|---|---|
| Read | `get_status`, `search_products`, `get_product`, `list_categories`, `get_restaurant_config`, `list_schedule_overrides`, `search_coupons`, `get_coupon`, `list_orders`, `get_order`, `get_daily_summary`, `get_customer_stats` |
| Low risk (applied, audited) | `set_product_availability`, `set_product_visibility`, `set_preparation_minutes`, `deactivate_coupon` |
| Sensitive (pending change) | `propose_price_change`, `propose_bulk_availability`, `propose_product_update`, `propose_product_creation`, `propose_vat_category_change`, `propose_product_image`, `propose_choice_group_change`, `propose_choice_change`, `propose_choice_group_deletion`, `propose_choice_deletion`, `propose_restaurant_closure`, `propose_restaurant_reopening`, `propose_opening_hours`, `propose_ordering_hours`, `propose_schedule_override`, `propose_schedule_override_removal`, `propose_coupon_creation`, `propose_coupon_update`, `propose_coupon_activation` |
| Undo | `undo_last_change`: last change from the past 30 minutes. A low-risk change is reverted directly; a sensitive one becomes a new pending change. Deletions and photos cannot be undone. |

Conventions:

- **Money** is integer cents (`price_cents`, `currency: "EUR"`). tsb-service itself uses decimal strings; the conversion is exact.
- **Times** are ISO 8601. A time without an offset is read in `TZ_DEFAULT` (Europe/Brussels). Dates are `yyyy-mm-dd`.
- **Every write** accepts `request_context`: the owner's original message, stored in the audit log.
- **Errors** are short, safe sentences. Tokens, stack traces and upstream bodies only go to the logs.

### Closures

`propose_restaurant_closure`:

- **Without `reopen_at`:** pauses online ordering (`orderingEnabled=false`) until `propose_restaurant_reopening`.
- **With `reopen_at`** (at most 7 days ahead): writes date overrides that remove the window `[from, reopen_at)` from each day's hours, so the restaurant reopens on its own. For example, "close at 15:00, back at 18:00" becomes today's hours minus 15:00 to 18:00.

`propose_restaurant_reopening` switches ordering back on and restores the overrides created by assistant closures that were not edited in the dashboard since.

## Configuration

All configuration comes from environment variables. See `.env.mcp.example`.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `UPSTREAM_BASE_URL` | yes | | tsb-service API root, e.g. `http://tsb-service:8080/api/v1` |
| `ZITADEL_ISSUER` | yes | | public issuer URL |
| `ZITADEL_CLIENT_ID`, `ZITADEL_CLIENT_SECRET` | yes | | the `tsb-mcp` machine user's client credentials |
| `ZITADEL_PROJECT_ID` | yes | | TSB project id (puts the project in the token `aud`) |
| `ZITADEL_INTERNAL_URL` | no | | in-cluster Zitadel URL (public Host header kept) |
| `MCP_AUTH_TOKEN` | with `--http` | | bearer token for the MCP HTTP endpoint |
| `INTERNAL_API_TOKEN` | yes | | bearer token for the internal API |
| `DB_PATH` | no | `tsb-mcp.db` (`/data/tsb-mcp.db` in the image) | SQLite file |
| `PENDING_TTL` | no | `10m` | lifetime of a pending change |
| `PRICE_MAX_CHANGE_PCT` | no | `50` | max price change in percent |
| `TZ_DEFAULT` | no | `Europe/Brussels` | timezone for times without an offset |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error` (JSON logs on stderr) |

### Service account

tsb-service validates JWT access tokens locally (JWKS). The `admin` role is only accepted from clients listed in `ZITADEL_ADMIN_CLIENT_IDS`. The MCP therefore needs the following.

**In Zitadel** (Terraform: `tsb-infra/terraform/zitadel-mcp.tf`):

- a machine user `tsb-mcp` with **access token type JWT** and a client secret;
- a project grant of the role **`admin`**. There is no narrower role that can write the catalogue.

**In tsb-service:** the machine user's client id is appended to `ZITADEL_ADMIN_CLIENT_IDS`.

**Token request:** tsb-mcp uses the client credentials grant with the scopes `openid`, `urn:zitadel:iam:org:project:id:<project>:aud` and `urn:zitadel:iam:org:projects:roles`. Tokens are cached until they expire. Such a token only has the project id in `aud` (no app client id), so tsb-service accepts the project id as a second audience; the admin role still needs the client id allowlist.

On its first call, tsb-service provisions an app user for the machine user with the placeholder email `tsb-mcp@machine.invalid` (commit `fix(user): provision Zitadel machine users…`).

TOTP is only enforced on interactive dashboard logins and does not apply to the machine user.

## Running

```bash
cp .env.mcp.example .env.mcp      # fill in the values
make mcp-run                      # stdio transport + internal API on :8081
make mcp-run-http                 # Streamable HTTP on :8080/mcp + internal API on :8081
make mcp-test                     # go test -race
make mcp-lint
make mcp-docker                   # distroless, non-root image
```

The binary is `tsb-mcp [--http :8080] [--internal :8081]`:

- **Without `--http`:** it speaks MCP over stdio. Logs go to stderr only.
- **`--http`:** serves `POST/GET /mcp`, which requires `Authorization: Bearer $MCP_AUTH_TOKEN`, and `GET /healthz`.
- **`--internal`:** serves the internal API on a separate listener. Without it, pending changes cannot be applied.

### MCP Inspector

```bash
# stdio
npx @modelcontextprotocol/inspector -- env $(grep -v '^#' .env.mcp | xargs) ./bin/tsb-mcp --internal :8081
# HTTP: start `make mcp-run-http`, open the inspector, choose "Streamable HTTP",
# URL http://localhost:8080/mcp, header Authorization: Bearer <MCP_AUTH_TOKEN>
npx @modelcontextprotocol/inspector
```

## Internal API (agent service)

The agent service calls these endpoints when the owner answers a proposal. They are on the `--internal` listener only, use `Authorization: Bearer $INTERNAL_API_TOKEN`, and are never exposed as MCP tools or resources. Keep that port cluster-internal: there is no ingress for it.

| Method and path | Effect |
|---|---|
| `GET /internal/changes/{id}` | Current state of a change |
| `POST /internal/changes/{id}/apply` | Re-check the state, apply, audit. Body `{"request_context": "是的"}` (optional) |
| `POST /internal/changes/{id}/reject` | Mark as rejected, audit. Same optional body |
| `GET /healthz` | No auth; checks SQLite |

A successful response is `200` with the change:

```json
{"change_id":"chg_…","tool":"propose_price_change","kind":"product.price","status":"applied",
 "summary":"\"Maki box\" (卷寿司套餐): price 13.90 EUR -> 14.50 EUR","summary_zh":"「卷寿司套餐」（套餐）价格：13.90 欧元 → 14.50 欧元","entity_type":"product","entity_id":"…",
 "created_at":"2026-10-03T13:00:00+02:00","expires_at":"2026-10-03T13:10:00+02:00","decided_at":"…","is_undo":false}
```

Errors return `{"error": "<sentence for the owner>", "code": "...", "change": {...}}`:

| Status | Code | When |
|---|---|---|
| 401 | `unauthorized` | Wrong or missing token |
| 404 | `not_found` | Unknown id |
| 409 | `already_applied`, `already_rejected`, `already_conflict`, `already_failed` | The change was already decided |
| 409 | `conflict` | The state changed since the proposal. Propose again |
| 410 | `expired` | Older than `PENDING_TTL` |
| 422 | `rejected` | The change is no longer valid |
| 502 | `upstream` | tsb-service failed. The change is marked `failed` when the failure happened during execution; a failure while re-reading the state leaves it pending so it can be retried |

Suggested agent flow:

1. Call the `propose_*` tool and show `summary_zh` to the owner as is (do not let the model rephrase it).
2. Keep `change_id` with the conversation.
3. On "yes", call `POST /apply`; on "no", call `POST /reject`.
4. On `409 conflict` or `410 expired`, propose again.

## Deployment

- **Image:** `ghcr.io/brunomoyse/tsb-mcp`, built by `.github/workflows/action.yml` next to tsb-service. A push to `main` builds `:latest`. A `v*` tag builds `:production` and `:vX.Y.Z`.
- **Helm:** the app is declared in the `tokyosushi-apps` chart of tsb-infra (`apps.tsb-mcp`), with:
  - one replica (SQLite) and the Recreate strategy;
  - a PVC mounted on `/data`;
  - ports 8080 (MCP) and 8081 (internal), both ClusterIP only.
- **First rollout:** CI upgrades with `--reuse-values`, which ignores new chart defaults, so the app only appears after a one-off `helm upgrade ... --reset-then-reuse-values --atomic` on the VPS (see the Helm notes in tsb-infra). Apply the Terraform secret `tsb-mcp-zitadel` first.
- **Deploy pin:** once the `tsb-mcp` Deployment exists, the tsb-service deploy and rollback jobs pin `apps.tsb-mcp.image.tag` to the release tag. Before that they leave it alone.
- **Rollback limit:** rolling back to a release older than tsb-mcp fails, because that image tag does not exist.
- **Secrets:** the Zitadel credentials and both bearer tokens come from the `tsb-mcp-zitadel` Kubernetes secret, managed by Terraform (`terraform/zitadel-mcp.tf`). The agent service reads `MCP_AUTH_TOKEN` and `INTERNAL_API_TOKEN` from the same secret.

## Tests

```bash
go test -race ./internal/mcp/... ./cmd/tsb-mcp/...
```

- **`fakeupstream`:** an in-memory tsb-service GraphQL API with the real response shapes (decimal strings, `categoryID`, `locale`, DATE-backed overrides).
- **`tools`:** every tool end to end through the SDK's in-memory transport, plus the tool list, safe errors, and checks that no order write is reachable.
- **`actions`:** price bounds, closure planning, expiry, conflicts, idempotence and undo.
- **`internalapi`:** apply, reject, expired, already applied, wrong token, upstream changed, upstream failure.
- **`search` and `money`:** table-driven tests (Chinese, accents, partial words, ranking).

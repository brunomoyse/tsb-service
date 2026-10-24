# pili-api

Backend API of Pili, the restaurant ordering platform.

Repository `nuagemagique-dev/pili-api`, local folder and internal name `tsb-service` (Helm app, Kubernetes deployment). Images: `ghcr.io/nuagemagique-dev/pili-api` and `ghcr.io/nuagemagique-dev/pili-mcp`.

`tsb-service` provides GraphQL and REST endpoints for ordering, catalog data, authentication, and payment/webhook processing.

## Stack

- Go 1.27 + Gin
- gqlgen (GraphQL generation)
- PostgreSQL + sqlx
- Mollie (payments)
- Zitadel OIDC/JWT validation
- zap structured logging

## Main capabilities

- GraphQL queries, mutations, and subscriptions
- Product/category multilingual data resolution (`Accept-Language`)
- Order and payment lifecycle management
- Auth directives (`@auth`, `@admin`) and optional/strict JWT middleware
- Idempotent Mollie webhook handling
- Request-scoped structured logs with request/user context

## Local setup

### 1) Environment

```bash
cp .env.example .env
```

Configure required DB, Zitadel, app URL, and provider credentials from `.env.example`.

### 2) Install and run

```bash
go mod tidy
go run cmd/app/main.go
```

API listens on `http://localhost:8080`.

## Commands

```bash
# Build
go build -o tsb-service cmd/app/main.go

# Tests
go test -v -race ./internal/... ./cmd/... ./pkg/...

# Lint
golangci-lint run --timeout=5m

# GraphQL code generation
go run github.com/99designs/gqlgen generate
```

## API surface

- `HEAD/GET /api/v1/up`
- `POST /api/v1/graphql`
- `GET /api/v1/graphql` (WebSocket subscriptions)
- `POST /api/v1/payments/webhook`
- Auth routes under `/api/v1/auth/*`

## Database migrations

Migrations use `pressly/goose` and live in `migrations/`.

```bash
go run cmd/migrate/main.go -cmd=status
go run cmd/migrate/main.go -cmd=up
go run cmd/migrate/main.go -cmd=down
go run cmd/migrate/main.go -cmd=create add_new_feature
```

In production the migrations of a release run before its pods start (the `tsb-migrate` pre-upgrade
hook of the tsb-infra chart), so the previous release keeps serving on the new schema for the length
of the rollout. A migration must therefore leave the previous release working:

- add columns nullable or with a default; drop or rename a column only in a later release than the
  one that stopped using it;
- queries list their columns (`db.Columns(domain.X{}, alias)`), never `SELECT *` or `alias.*`
  (sqlx refuses a column the struct has no field for); `pkg/db` has a test that enforces it.

## Docker

```bash
docker build -t tsb-service .
docker run --name tsb-service --env-file .env -p 8080:8080 tsb-service
```

The image is multi-stage, includes healthchecks, and ships `tsb-migrate` with SQL migrations.

## Deployment

- Push to `main`: build/test path and latest deployment pipeline.
- Tag `v*`: production image build and OVH deployment. The migrations run in `helm upgrade`, as the
  tsb-infra chart's pre-upgrade hook, before the pods roll; a failed migration fails the deploy and
  leaves the old pods serving.
- Manual rollback: run workflow with target version and migration rollback count.

## Pili repositories

Every Pili repository lives in the GitHub org `nuagemagique-dev`. Local folders kept their earlier names.

| Repository | Local folder | What it is |
|---|---|---|
| **pili-api** (this repository) | `tsb-service` | GraphQL API (Go) and the `pili-mcp` tool server |
| pili-shop | `tsb-core` | Customer web shop, one Nuxt app per brand |
| pili-dashboard | `tsb-dashboard` | Admin dashboard (Nuxt) |
| pili-mobile | `tsb-mobile` | Customer iOS and Android app (Expo) |
| pili-handheld | `moyun-handheld` | Staff handheld app for the Sunmi V3H (Kotlin) |
| pili-pos | `moyun-pos` | SCE 2.0 cash register (Rust engine, Android and Linux) |
| pili-agent | `tsb-agent` | Owner and staff Telegram agent |
| pili-infra | `tsb-infra` | Helm charts, Terraform, SOPS secrets, runbooks |

# tsb-service

Backend API for Tokyo Sushi Bar.

`tsb-service` provides GraphQL and REST endpoints for ordering, catalog data, authentication, and payment/webhook processing.

## Stack

- Go 1.26 + Gin
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

## Docker

```bash
docker build -t tsb-service .
docker run --name tsb-service --env-file .env -p 8080:8080 tsb-service
```

The image is multi-stage, includes healthchecks, and ships `tsb-migrate` with SQL migrations.

## Deployment

- Push to `main`: build/test path and latest deployment pipeline.
- Tag `v*`: production image build and OVH deployment, including migration step.
- Manual rollback: run workflow with target version and migration rollback count.

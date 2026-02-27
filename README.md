# tsb-service

**tsb-service** is a GraphQL API built with **Go** to serve as the backend of a restaurant webshop. The API handles orders, manages products with multi-language support, and processes payments via **Mollie**.

## Features

- **GraphQL API**: Full-featured GraphQL API with queries, mutations, and real-time subscriptions
- **Order Management**: Create, retrieve, and manage customer orders with real-time status updates via WebSocket subscriptions
- **Payment Integration**: Secure payment processing through Mollie with idempotent webhook handling
- **Multi-language Support**: Product and category translations stored in dedicated tables, language resolved from `Accept-Language` header
- **Authentication**: JWT-based dual-token system (access + refresh tokens) with Google OAuth support
- **Role-based Access Control**: `@auth` and `@admin` GraphQL directives for fine-grained authorization
- **Structured Logging**: Request-scoped structured logging with `go.uber.org/zap`
- **Production Hardened**: Rate limiting, security headers, body size limits, CORS, graceful shutdown

## Technologies

- **Go 1.26** with Gin framework
- **gqlgen** for GraphQL code generation
- **PostgreSQL** with sqlx
- **Mollie API** for payment processing
- **Scaleway** for transactional email
- **Google OAuth 2.0** for social login
- **Docker** for containerization (multi-arch AMD64/ARM64)

## Getting Started

### 1. Clone the Repository

```bash
git clone https://github.com/brunomoyse/tsb-service.git
cd tsb-service
```

### 2. Setup Environment Variables

```bash
cp .env.example .env
```

Fill in the `.env` values. See `.env.example` for all required variables.

### 3. Install Dependencies

```bash
go mod tidy
```

### 4. Run the Application

```bash
go run cmd/app/main.go
```

The API starts on `http://localhost:8080`.

## API Endpoints

### GraphQL
- `POST /api/v1/graphql` — GraphQL queries and mutations (optional auth)
- `GET /api/v1/graphql` — WebSocket subscriptions (requires auth)

### Authentication
- `POST /api/v1/login` — Email/password login
- `POST /api/v1/register` — User registration
- `GET /api/v1/verify` — Email verification
- `POST /api/v1/tokens/refresh` — Refresh access token
- `POST /api/v1/logout` — Logout (revoke tokens)
- `GET /api/v1/oauth/google` — Google OAuth initiation
- `GET /api/v1/oauth/google/callback` — Google OAuth callback

### Other
- `HEAD/GET /api/v1/up` — Health check
- `POST /api/v1/payments/webhook` — Mollie payment webhook

## GraphQL Code Generation

After modifying schemas in `internal/api/graphql/schema/*.graphql`:

```bash
go run github.com/99designs/gqlgen generate
```

## Testing

```bash
go test -v -race ./internal/... ./cmd/... ./pkg/...
```

## Linting

```bash
golangci-lint run --timeout=5m
```

## Database Migrations

Uses [pressly/goose](https://github.com/pressly/goose) v3. Migration files in `migrations/`.

```bash
go run cmd/migrate/main.go -cmd=up        # Run pending migrations
go run cmd/migrate/main.go -cmd=down      # Rollback last migration
go run cmd/migrate/main.go -cmd=status    # Check status
go run cmd/migrate/main.go -cmd=create add_new_feature  # Create new migration
```

## Docker

```bash
docker build -t tsb-service .
docker run --name tsb-service --env-file .env -p 8080:8080 tsb-service
```

Multi-stage Dockerfile with health check. Supports multi-arch builds (AMD64/ARM64). Includes migration binary (`tsb-migrate`) and SQL files for in-container migration execution.

## Deployment

### Release
```bash
git tag v1.0.0
git push origin v1.0.0
```
This triggers a GitHub Actions workflow that builds a `:production` + `:v1.0.0` AMD64 image, runs migrations on the production server, and deploys via SSH.

### Rollback
Go to the Actions tab → "Run workflow" → enter the version to rollback to (e.g. `v1.0.0`) and the number of migrations to roll back.

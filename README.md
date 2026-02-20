# tsb-service

**tsb-service** is a GraphQL API built with **Go** to serve as the backend of a restaurant webshop. The API handles orders, manages products with multi-language support, and processes payments via **Mollie**.

## Features

- **GraphQL API**: Full-featured GraphQL API with queries, mutations, and real-time subscriptions
- **Order Management**: Create, retrieve, and manage customer orders with real-time status updates via WebSocket subscriptions
- **Payment Integration**: Secure payment processing through Mollie with idempotent webhook handling
- **Multi-language Support**: Product and category translations stored in dedicated tables, language resolved from `Accept-Language` header
- **Authentication**: JWT-based dual-token system (access + refresh tokens) with Google OAuth support
- **Role-based Access Control**: `@auth` and `@admin` GraphQL directives for fine-grained authorization
- **Structured Logging**: Request-scoped structured logging with `log/slog`
- **Production Hardened**: Rate limiting, security headers, body size limits, CORS, graceful shutdown

## Technologies

- **Go 1.24** with Gin framework
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

## Docker

```bash
docker build -t tsb-service .
docker run --name tsb-service --env-file .env -p 8080:8080 tsb-service
```

Multi-stage Dockerfile with health check. Supports multi-arch builds (AMD64/ARM64).

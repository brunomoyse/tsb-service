# GraphQL Testing Guide

This directory contains comprehensive integration tests for the GraphQL API.

## Prerequisites

- **Docker**: Required for running tests (Dockertest spins up PostgreSQL containers)
- **Go 1.21+**: Ensure you have Go installed

## Test Structure

```
internal/api/graphql/
├── testhelpers/            # Test utilities and helpers
│   ├── database.go         # Dockertest PostgreSQL setup
│   ├── jwt.go              # Test JWT token generation
│   ├── graphql_client.go   # GraphQL test client
│   ├── fixtures.go         # Test data seeding
│   └── mocks.go            # Mock external services
├── resolver_test.go        # Shared test setup
├── product_test.go         # Product query & mutation tests
├── user_test.go            # User query & mutation tests
└── README_TESTING.md       # This file
```

## Running Tests

### Run all GraphQL tests
```bash
go test -v ./internal/api/graphql/...
```

### Run all tests (excluding integration tests)
```bash
# This excludes deliveroo and email service tests
go test -v ./internal/... ./cmd/... ./pkg/...
```

### Run integration tests (deliveroo, email)
```bash
# These require external service credentials
go test -v -tags=integration ./services/...
```

### Run specific test
```bash
go test -v ./internal/api/graphql -run TestProducts
go test -v ./internal/api/graphql -run TestMeQuery
```

### Run with coverage
```bash
go test -v -coverprofile=coverage.out ./internal/api/graphql/...
go tool cover -html=coverage.out
```

### Run with race detector
```bash
go test -v -race ./internal/api/graphql/...
```

## Test Coverage

### Product Module
- ✅ Query all products (with multi-language support)
- ✅ Query single product by ID
- ✅ Query product categories (with multi-language support)
- ✅ Query single category by ID
- ✅ Create product (admin only)
- ✅ Update product (admin only)
- ✅ Authorization tests (@admin directive)

### User Module
- ✅ Query me (@auth directive)
- ✅ Update me (@auth directive)
- ✅ Authorization tests (no token, expired token)

### Authentication
- ✅ Test valid JWT tokens
- ✅ Test expired tokens
- ✅ Test missing tokens
- ✅ Test admin vs regular user permissions

## Test Environment

Tests use:
- **Dockertest**: Automatically spins up PostgreSQL 15 containers
- **Test fixtures**: Pre-seeded data (users, products, categories)
- **Mock services**: External services (Mollie, Email) are mocked
- **Test JWT**: Uses `testhelpers.TestJWTSecret` for token generation

## Key Test Helpers

### Generate Test Tokens
```go
// Regular user token
token, err := testhelpers.GenerateTestAccessToken(userID, false)

// Admin user token
adminToken, err := testhelpers.GenerateTestAccessToken(userID, true)

// Expired token (for testing auth failures)
expiredToken, err := testhelpers.GenerateExpiredToken(userID, false)
```

### Test Data Access
```go
ctx := setupTestContext(t)

// Access fixtures
ctx.Fixtures.RegularUser    // Test user
ctx.Fixtures.AdminUser       // Admin user
ctx.Fixtures.SalmonSushi     // Product
ctx.Fixtures.SushiCategory   // Category
```

## Continuous Integration

Tests run automatically on:
- Every push to any branch
- Every pull request to main/master

See `.github/workflows/test.yml` for CI configuration.

### Branch Protection

To require tests to pass before merging to main:

1. Go to **Settings** → **Branches** → **Branch protection rules**
2. Add rule for `main` or `master`
3. Check **Require status checks to pass before merging**
4. Select **Tests** workflow

## Build Tags

### Integration Tests
Deliveroo and email service tests are tagged with `// +build integration` and require:
- External service credentials (Deliveroo API, email service)
- `.env` file with proper configuration

These tests are **excluded by default** from CI/CD and normal test runs.

To run integration tests manually:
```bash
go test -v -tags=integration ./services/deliveroo/...
```

## Troubleshooting

### Docker not available
```
Error: dial unix /var/run/docker.sock: connect: no such file or directory
```
**Solution**: Ensure Docker is running:
```bash
docker ps  # Should show running containers
```

### Port already in use
Dockertest automatically selects available ports. If you see port conflicts, ensure no other PostgreSQL instances are bound to standard ports.

### Migration failures
If migrations fail, check:
- All `.up.sql` files are in `migrations/` directory
- Files follow naming convention: `NNN_description.up.sql`
- SQL syntax is valid PostgreSQL

## Adding New Tests

1. **Create test file**: `internal/api/graphql/{module}_test.go`
2. **Use shared setup**:
   ```go
   func TestMyFeature(t *testing.T) {
       ctx := setupTestContext(t)
       c := client.New(ctx.Client.Handler())
       // ... write tests
   }
   ```
3. **Add fixtures if needed**: Update `testhelpers/fixtures.go`
4. **Update this README**: Document new test coverage

## Future Enhancements

- [ ] Add order creation tests (with Mollie mocking)
- [ ] Add subscription tests (WebSocket)
- [ ] Add Deliveroo platform integration tests
- [ ] Add address lookup tests
- [ ] Increase coverage to 80%+

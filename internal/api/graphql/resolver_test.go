package graphql_test

import (
	"testing"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/jmoiron/sqlx"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	addressApplication "tsb-service/internal/modules/address/application"
	addressInfrastructure "tsb-service/internal/modules/address/infrastructure"
	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentInfrastructure "tsb-service/internal/modules/payment/infrastructure"
	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	userApplication "tsb-service/internal/modules/user/application"
	userInfrastructure "tsb-service/internal/modules/user/infrastructure"
	"tsb-service/pkg/pubsub"
)

// TestContext holds all test dependencies
type TestContext struct {
	DB       *testhelpers.TestDatabase
	Resolver *resolver.Resolver
	Client   *testhelpers.GraphQLTestClient
	Fixtures *testhelpers.TestFixtures
}

// setupTestContext creates a complete test environment
func setupTestContext(t *testing.T) *TestContext {
	// Setup test database
	testDB := testhelpers.SetupTestDatabase(t)

	// Seed test data
	fixtures := testhelpers.SeedTestData(t, testDB.DB)

	// Create resolver with real services and repositories
	r := createTestResolver(testDB.DB)

	// Create GraphQL test client
	client := testhelpers.NewGraphQLTestClient(r, testhelpers.TestJWTSecret)

	// Register cleanup
	t.Cleanup(func() {
		client.Close()
	})

	return &TestContext{
		DB:       testDB,
		Resolver: r,
		Client:   client,
		Fixtures: fixtures,
	}
}

// createTestResolver creates a resolver with all dependencies wired up
func createTestResolver(db *sqlx.DB) *resolver.Resolver {
	// Create PubSub broker
	broker := pubsub.NewBroker()

	// Create repositories
	addressRepo := addressInfrastructure.NewAddressRepository(db)
	orderRepo := orderInfrastructure.NewOrderRepository(db)
	paymentRepo := paymentInfrastructure.NewPaymentRepository(db)
	productRepo := productInfrastructure.NewProductRepository(db)
	userRepo := userInfrastructure.NewUserRepository(db)

	// Create Mollie client (test mode)
	mollieCfg := mollie.NewAPITestingConfig(true)
	mollieClient, _ := mollie.NewClient(nil, mollieCfg)

	// Create services
	addressService := addressApplication.NewAddressService(addressRepo)
	orderService := orderApplication.NewOrderService(orderRepo)
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient)
	productService := productApplication.NewProductService(productRepo)
	userService := userApplication.NewUserService(userRepo)

	// Create resolver
	return &resolver.Resolver{
		Broker:           broker,
		AddressService:   addressService,
		OrderService:     orderService,
		PaymentService:   paymentService,
		ProductService:   productService,
		UserService:      userService,
		DeliverooService: nil, // Optional - not needed for core tests
	}
}

package graphql_test

import (
	"testing"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	addressApplication "tsb-service/internal/modules/address/application"
	addressInfrastructure "tsb-service/internal/modules/address/infrastructure"
	couponApplication "tsb-service/internal/modules/coupon/application"
	couponInfrastructure "tsb-service/internal/modules/coupon/infrastructure"
	orderApplication "tsb-service/internal/modules/order/application"
	orderInfrastructure "tsb-service/internal/modules/order/infrastructure"
	paymentApplication "tsb-service/internal/modules/payment/application"
	paymentInfrastructure "tsb-service/internal/modules/payment/infrastructure"
	productApplication "tsb-service/internal/modules/product/application"
	productInfrastructure "tsb-service/internal/modules/product/infrastructure"
	restaurantApplication "tsb-service/internal/modules/restaurant/application"
	restaurantInfrastructure "tsb-service/internal/modules/restaurant/infrastructure"
	userApplication "tsb-service/internal/modules/user/application"
	userInfrastructure "tsb-service/internal/modules/user/infrastructure"
	"tsb-service/pkg/db"
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
	r := createTestResolver(testDB)

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
func createTestResolver(testDB *testhelpers.TestDatabase) *resolver.Resolver {
	// Wrap the test DB in a DBPool (both customer and admin use the same connection)
	pool := &db.DBPool{Customer: testDB.DB, Admin: testDB.DB}

	// Create PubSub broker
	broker := pubsub.NewBroker()

	// Create repositories
	addressRepo := addressInfrastructure.NewAddressRepository(pool)
	couponRepo := couponInfrastructure.NewCouponRepository(pool)
	orderRepo := orderInfrastructure.NewOrderRepository(pool)
	paymentRepo := paymentInfrastructure.NewPaymentRepository(pool)
	productRepo := productInfrastructure.NewProductRepository(pool)
	restaurantRepo := restaurantInfrastructure.NewRestaurantRepository(pool)
	userRepo := userInfrastructure.NewUserRepository(pool)

	// Create Mollie client (test mode)
	mollieCfg := mollie.NewAPITestingConfig(true)
	mollieClient, _ := mollie.NewClient(nil, mollieCfg)

	// Create services
	addressService := addressApplication.NewAddressService(addressRepo)
	couponService := couponApplication.NewCouponService(couponRepo)
	orderService := orderApplication.NewOrderService(orderRepo)
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient)
	productService := productApplication.NewProductService(productRepo)
	restaurantService := restaurantApplication.NewRestaurantService(restaurantRepo, true)
	userService := userApplication.NewUserService(userRepo)

	// Create resolver
	return &resolver.Resolver{
		Broker:            broker,
		AddressService:    addressService,
		CouponService:     couponService,
		OrderService:      orderService,
		PaymentService:    paymentService,
		ProductService:    productService,
		RestaurantService: restaurantService,
		UserService:       userService,
	}
}

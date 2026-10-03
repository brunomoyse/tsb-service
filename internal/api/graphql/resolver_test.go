package graphql_test

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/graphql/testhelpers"
	addressApplication "tsb-service/internal/modules/address/application"
	addressDomain "tsb-service/internal/modules/address/domain"
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

// testContextOptions tune the environment of a test; the zero value is the default one.
type testContextOptions struct {
	// EnforceOrderingHours turns the opening-hours / slot gate on. By default the restaurant
	// service runs in dev mode, which skips the gate so tests can order at any time.
	EnforceOrderingHours bool
	// MollieBaseURL points the Mollie client at a stub (testhelpers.MollieStub).
	MollieBaseURL string
}

// setupTestContext creates a complete test environment
func setupTestContext(t *testing.T) *TestContext {
	return setupTestContextWith(t, testContextOptions{})
}

// setupTestContextWith creates a complete test environment with the given options.
func setupTestContextWith(t *testing.T, opts testContextOptions) *TestContext {
	// Setup test database
	testDB := testhelpers.SetupTestDatabase(t)

	// Seed test data
	fixtures := testhelpers.SeedTestData(t, testDB.DB)

	// Create resolver with real services and repositories
	r := createTestResolverWith(t, testDB, opts)

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

// createTestResolverWith creates a resolver with all dependencies wired up.
func createTestResolverWith(t *testing.T, testDB *testhelpers.TestDatabase, opts testContextOptions) *resolver.Resolver {
	// Wrap the test DB in a DBPool (both customer and admin use the same connection)
	pool := &db.DBPool{Customer: testDB.DB, Admin: testDB.DB}

	// Create PubSub broker
	broker := pubsub.NewBroker()

	// Create repositories
	addressCacheRepo := addressInfrastructure.NewAddressCacheRepository(pool)
	couponRepo := couponInfrastructure.NewCouponRepository(pool)
	orderRepo := orderInfrastructure.NewOrderRepository(pool)
	paymentRepo := paymentInfrastructure.NewPaymentRepository(pool)
	productRepo := productInfrastructure.NewProductRepository(pool)
	restaurantRepo := restaurantInfrastructure.NewRestaurantRepository(pool)
	scheduleOverrideRepo := restaurantInfrastructure.NewScheduleOverrideRepository(pool)
	userRepo := userInfrastructure.NewUserRepository(pool)

	// Create Mollie client (test mode)
	mollieCfg := mollie.NewAPITestingConfig(true)
	mollieClient, _ := mollie.NewClient(nil, mollieCfg)
	if opts.MollieBaseURL != "" {
		base, err := url.Parse(opts.MollieBaseURL)
		require.NoError(t, err)
		mollieClient.BaseURL = base
		require.NoError(t, mollieClient.WithAuthenticationValue("test_dummy_token"))
	}

	// Create services (use a mock Google client for tests; real API calls require actual API key)
	// For now, we'll use nil for googleClient since this test doesn't call Autocomplete/Resolve
	googleClient := (*mockGoogleClient)(nil)
	addressService := addressApplication.NewAddressService(addressCacheRepo, googleClient, "fr")
	couponService := couponApplication.NewCouponService(couponRepo)
	orderService := orderApplication.NewOrderService(orderRepo, couponService)
	productService := productApplication.NewProductService(productRepo)
	restaurantService := restaurantApplication.NewRestaurantService(restaurantRepo, scheduleOverrideRepo, !opts.EnforceOrderingHours)
	userService := userApplication.NewUserService(userRepo, nil)
	paymentService := paymentApplication.NewPaymentService(paymentRepo, *mollieClient, orderService, userService, productService)

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

// mockGoogleClient is a stub for testing (no actual Google API calls)
type mockGoogleClient struct{}

func (m *mockGoogleClient) Autocomplete(ctx context.Context, input, sessionToken, language string) ([]addressDomain.Suggestion, error) {
	return nil, nil
}

func (m *mockGoogleClient) PlaceDetails(ctx context.Context, placeID, sessionToken, language string) (*addressDomain.AddressCache, error) {
	return nil, errors.New("mock google client: no place details")
}

func (m *mockGoogleClient) ComputeRoute(ctx context.Context, destLat, destLng float64) (int, int, error) {
	return 0, 0, nil
}

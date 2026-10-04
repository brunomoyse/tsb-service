package graphql_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/apperr"
	addressDomain "tsb-service/internal/modules/address/domain"
	"tsb-service/pkg/utils"
)

// The two public address lookups: they bound their input, are throttled per client IP and report
// what the places provider answered.

func TestAddressLookups(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	q := env.Resolver.Query()
	from := func(ip string) context.Context { return utils.SetClientIP(env.ctxFor("", false, "fr"), ip) }

	t.Run("autocomplete bounds the input between 3 and 200 characters", func(t *testing.T) {
		for name, input := range map[string]string{"too short": "ab", "blank": "    ", "too long": strings.Repeat("é", 201)} {
			_, err := q.AutocompleteAddresses(from("10.0.0.1"), input, "session")
			userErr(t, err, "address search must be between 3 and 200 characters")
			_ = name
		}
		env.Google.suggestions = []addressDomain.Suggestion{{PlaceID: "p1", Description: "Rue du Pont 1, Liège", MainText: "Rue du Pont 1", SecondaryText: "Liège"}}
		got, err := q.AutocompleteAddresses(from("10.0.0.2"), strings.Repeat("é", 200), "session")
		require.NoError(t, err, "200 characters is the longest accepted")
		require.Len(t, got, 1)
		assert.Equal(t, "p1", got[0].PlaceID)
		assert.Equal(t, "Rue du Pont 1", got[0].MainText)
		assert.Equal(t, "Liège", got[0].SecondaryText)
	})

	t.Run("autocomplete is throttled per client IP, and the provider's failure is reported", func(t *testing.T) {
		env.Google.suggestions = nil
		for range 2 {
			got, err := q.AutocompleteAddresses(from("10.0.0.3"), "rue", "s")
			require.NoError(t, err)
			assert.Empty(t, got)
		}
		_, err := q.AutocompleteAddresses(from("10.0.0.3"), "rue", "s")
		requireCode(t, err, apperr.CodeRateLimited)
		_, err = q.AutocompleteAddresses(from("10.0.0.4"), "rue", "s")
		require.NoError(t, err, "another client is not affected")

		env.Google.autoErr = errBoom
		t.Cleanup(func() { env.Google.autoErr = nil })
		_, err = q.AutocompleteAddresses(from("10.0.0.5"), "rue", "s")
		require.ErrorContains(t, err, "failed to autocomplete addresses")
	})

	t.Run("resolveAddress asks the provider once and then serves the cache", func(t *testing.T) {
		street, house, post, city := "Rue du Pont", "1", "4000", "Liège"
		env.Google.place = &addressDomain.AddressCache{PlaceID: "place-new", FormattedAddress: "Rue du Pont 1, 4000 Liège", Lat: 50.6, Lng: 5.5,
			StreetName: &street, HouseNumber: &house, Postcode: &post, MunicipalityName: &city, CountryCode: "BE"}
		got, err := q.ResolveAddress(from("10.0.1.1"), "place-new", "s")
		require.NoError(t, err)
		assert.Equal(t, "place-new", got.ID)
		assert.Equal(t, "Rue du Pont", got.StreetName)
		assert.Equal(t, 1500.0, got.Distance, "the route distance is stored with the address")

		env.Google.place = nil
		again, err := q.ResolveAddress(from("10.0.1.1"), "place-new", "s")
		require.NoError(t, err, "the provider is not asked again")
		assert.Equal(t, got.StreetName, again.StreetName)
	})

	t.Run("resolveAddress is throttled and reports an unknown place", func(t *testing.T) {
		_, err := q.ResolveAddress(from("10.0.1.2"), "nowhere", "s")
		require.ErrorContains(t, err, "failed to resolve address")
		_, err = q.ResolveAddress(from("10.0.1.2"), "nowhere", "s")
		require.Error(t, err)
		_, err = q.ResolveAddress(from("10.0.1.2"), "nowhere", "s")
		requireCode(t, err, apperr.CodeRateLimited)
	})
}

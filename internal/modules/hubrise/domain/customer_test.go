package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"tsb-service/internal/modules/hubrise/domain"
)

func TestCustomerHashesTellWhatChanged(t *testing.T) {
	base := domain.CustomerFields{FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com", Phone: "+32470000000"}

	assert.Equal(t, base.ProfileHash(), base.ProfileHash(), "stable")
	for name, changed := range map[string]domain.CustomerFields{
		"first": {FirstName: "Grace", LastName: "Lovelace", Email: "ada@example.com", Phone: "+32470000000"},
		"last":  {FirstName: "Ada", LastName: "Hopper", Email: "ada@example.com", Phone: "+32470000000"},
		"email": {FirstName: "Ada", LastName: "Lovelace", Email: "grace@example.com", Phone: "+32470000000"},
		"phone": {FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com"},
	} {
		assert.NotEqual(t, base.ProfileHash(), changed.ProfileHash(), name)
	}
	// The parts are separated, so moving a character between two fields is a change.
	assert.NotEqual(t,
		domain.CustomerFields{FirstName: "ab", LastName: "c"}.ProfileHash(),
		domain.CustomerFields{FirstName: "a", LastName: "bc"}.ProfileHash())

	// The address hash is empty without an address and ignores the profile.
	assert.False(t, base.HasAddress())
	assert.Empty(t, base.AddressHash())
	withAddress := base
	withAddress.Address1, withAddress.PostalCode, withAddress.City = "Rue du Test 12", "4000", "Liege"
	assert.True(t, withAddress.HasAddress())
	assert.NotEmpty(t, withAddress.AddressHash())
	assert.Equal(t, base.ProfileHash(), withAddress.ProfileHash())
	moved := withAddress
	moved.DeliveryNotes = "Second floor"
	assert.NotEqual(t, withAddress.AddressHash(), moved.AddressHash())
	for _, f := range []domain.CustomerFields{{Address1: "x"}, {PostalCode: "4000"}, {City: "Liege"}} {
		assert.True(t, f.HasAddress())
	}
}

func TestCustomerContentHashJoinsAndSplits(t *testing.T) {
	joined := domain.JoinCustomerHash("profile", "address")
	assert.Equal(t, "profile|address", joined)
	p, a := domain.SplitCustomerHash(joined)
	assert.Equal(t, "profile", p)
	assert.Equal(t, "address", a)

	p, a = domain.SplitCustomerHash(domain.JoinCustomerHash("profile", ""))
	assert.Equal(t, "profile", p)
	assert.Empty(t, a)

	// A customer never written has no hash at all.
	p, a = domain.SplitCustomerHash("")
	assert.Empty(t, p)
	assert.Empty(t, a)
	p, a = domain.SplitCustomerHash("not a hash")
	assert.Empty(t, p)
	assert.Empty(t, a)
}

func TestCustomerDedupeKey(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	assert.Equal(t, "cust:11111111-2222-3333-4444-555555555555", domain.CustomerDedupeKey(id))
}

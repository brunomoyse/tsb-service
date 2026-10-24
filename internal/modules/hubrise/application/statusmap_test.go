package application

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

func TestRemoteStatusFor(t *testing.T) {
	cases := map[string]hubrise.OrderStatus{
		domain.LocalConfirmed:      hubrise.StatusAccepted,
		domain.LocalPreparing:      hubrise.StatusInPreparation,
		domain.LocalAwaitingPickUp: hubrise.StatusAwaitingCollection,
		domain.LocalOutForDelivery: hubrise.StatusInDelivery,
		domain.LocalPickedUp:       hubrise.StatusCompleted,
		domain.LocalDelivered:      hubrise.StatusCompleted,
		domain.LocalCancelled:      hubrise.StatusCancelled,
		domain.LocalPending:        "",
		domain.LocalFailed:         "",
		"SOMETHING_ELSE":           "",
	}
	for local, want := range cases {
		assert.Equal(t, want, RemoteStatusFor(local), local)
	}
}

func TestInitialRemoteStatusIsNewForAnUntouchedOrder(t *testing.T) {
	assert.Equal(t, hubrise.StatusNew, initialRemoteStatus(domain.LocalPending))
	assert.Equal(t, hubrise.StatusInPreparation, initialRemoteStatus(domain.LocalPreparing))
	assert.Equal(t, hubrise.OrderStatus(""), initialRemoteStatus(domain.LocalFailed))
}

func TestStatusPairsIsACopy(t *testing.T) {
	pairs := StatusPairs()
	assert.Len(t, pairs, 7)
	pairs[0].Remote = "tampered"
	assert.NotEqual(t, "tampered", StatusPairs()[0].Remote)
	for _, p := range StatusPairs() {
		assert.True(t, hubrise.OrderStatus(p.Remote).Valid(), p.Remote)
		assert.False(t, domain.IsTerminalLocal(p.Local) && p.Remote == string(hubrise.StatusNew))
	}
}

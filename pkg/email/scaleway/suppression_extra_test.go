package scaleway

import (
	"errors"
	"testing"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestIsSuppressedFailsOpenOnStoreError(t *testing.T) {
	isolateGlobals(t)
	store := newRecordingStore()
	store.lookupErr = errors.New("connection reset")
	suppressionStore = store

	core, logs := observer.New(zap.WarnLevel)
	defer zap.ReplaceGlobals(zap.New(core))()

	require.False(t, isSuppressed("anyone@example.com"), "a lookup failure must never block a transactional email")
	require.Len(t, logs.FilterMessage("email suppression lookup failed; sending anyway").All(), 1)
}

func TestIsSuppressedNormalizesBeforeLookup(t *testing.T) {
	isolateGlobals(t)
	store := newRecordingStore()
	store.suppressed["bounced@example.com"] = "hard_bounce"
	suppressionStore = store

	require.True(t, isSuppressed("  Bounced@Example.COM "))
	require.Equal(t, []string{"bounced@example.com"}, store.lookupCalls)
	require.False(t, isSuppressed("fine@example.com"))
}

func TestFilterSuppressedRecipientsEmptyAndNilEntries(t *testing.T) {
	isolateGlobals(t)
	suppressionStore = newRecordingStore()

	kept, has := filterSuppressedRecipients(&temv1alpha1.CreateEmailRequest{})
	require.True(t, has, "a request with no To is passed through unchanged")
	require.Empty(t, kept)

	kept, has = filterSuppressedRecipients(&temv1alpha1.CreateEmailRequest{To: []*temv1alpha1.CreateEmailRequestAddress{nil}})
	require.True(t, has)
	require.Len(t, kept, 1)
}

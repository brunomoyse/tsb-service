package testhelpers_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/testhelpers"
)

func TestFakeClock(t *testing.T) {
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	c := testhelpers.NewFakeClock(start)
	assert.True(t, c.Now().Equal(start))
	c.Advance(90 * time.Second)
	assert.True(t, c.Now().Equal(start.Add(90*time.Second)))
	c.Set(start)
	assert.True(t, c.Now().Equal(start))
}

func TestTestCodecRoundTrips(t *testing.T) {
	c := testhelpers.TestCodec(t)
	blob, err := c.Encrypt("abc")
	require.NoError(t, err)
	got, err := c.Decrypt(blob)
	require.NoError(t, err)
	assert.Equal(t, "abc", got)
}

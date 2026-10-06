package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"tsb-service/pkg/utils"
)

func TestBindToTokenExpiry(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)

	t.Run("user token gets a deadline at exp", func(t *testing.T) {
		ctx := utils.SetTokenExpiry(context.Background(), exp)
		deadline, ok := bindToTokenExpiry(ctx).Deadline()
		assert.True(t, ok)
		assert.True(t, deadline.Equal(exp))
	})

	t.Run("POS device token keeps the socket open", func(t *testing.T) {
		ctx := utils.SetIsPOS(utils.SetTokenExpiry(context.Background(), exp), true)
		_, ok := bindToTokenExpiry(ctx).Deadline()
		assert.False(t, ok)
	})

	t.Run("no expiry recorded means no deadline", func(t *testing.T) {
		_, ok := bindToTokenExpiry(context.Background()).Deadline()
		assert.False(t, ok)
	})
}

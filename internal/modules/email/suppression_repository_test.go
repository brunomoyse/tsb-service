package email

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/pkg/db"
)

func TestSuppressionRepository(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	repo := NewSuppressionRepository(&db.DBPool{Customer: tdb.DB, Admin: tdb.DB})
	ctx := t.Context()

	t.Run("unknown address is not suppressed", func(t *testing.T) {
		ok, err := repo.IsSuppressed(ctx, "nobody@example.com")
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("suppressed address is reported, case and whitespace insensitive", func(t *testing.T) {
		require.NoError(t, repo.Suppress(ctx, "  Bounce@Example.COM ", "hard bounce: mailbox unknown"))

		for _, variant := range []string{"bounce@example.com", "BOUNCE@EXAMPLE.COM", " bounce@example.com\n"} {
			ok, err := repo.IsSuppressed(ctx, variant)
			require.NoError(t, err)
			require.True(t, ok, variant)
		}

		// Stored normalized, with the default source.
		var email, source string
		require.NoError(t, tdb.DB.QueryRowContext(ctx, `SELECT email, source FROM email_suppressions`).Scan(&email, &source))
		require.Equal(t, "bounce@example.com", email)
		require.Equal(t, "scaleway_poll", source)
	})

	t.Run("suppress is idempotent and keeps the original reason", func(t *testing.T) {
		require.NoError(t, repo.Suppress(ctx, "bounce@example.com", "second reason"))
		var n int
		var reason string
		require.NoError(t, tdb.DB.QueryRowContext(ctx, `SELECT count(*), min(reason) FROM email_suppressions WHERE email = 'bounce@example.com'`).Scan(&n, &reason))
		require.Equal(t, 1, n)
		require.Equal(t, "hard bounce: mailbox unknown", reason)
	})

	t.Run("other addresses are unaffected", func(t *testing.T) {
		ok, err := repo.IsSuppressed(ctx, "other@example.com")
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("database errors are surfaced", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := repo.IsSuppressed(cctx, "x@example.com")
		require.Error(t, err)
		require.Error(t, repo.Suppress(cctx, "x@example.com", "r"))
	})
}

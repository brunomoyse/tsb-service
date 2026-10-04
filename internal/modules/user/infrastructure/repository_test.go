package infrastructure

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
	"tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/db"
)

func ptr[T any](v T) *T { return &v }

func TestUserRepository(t *testing.T) {
	tdb := testhelpers.SetupTestDatabase(t)
	pool := &db.DBPool{Customer: tdb.DB, Admin: tdb.DB}
	repo := NewUserRepository(pool)
	ctx := t.Context()

	t.Run("Save normalises the email and returns the generated id", func(t *testing.T) {
		u := &domain.User{FirstName: "Ada", LastName: "Lovelace", Email: "  Ada@Example.COM ", PhoneNumber: ptr("+32470123456"), ZitadelUserID: ptr("zit-ada")}
		id, err := repo.Save(ctx, u)
		require.NoError(t, err)
		assert.NotEqual(t, uuid.Nil, id)
		assert.Equal(t, id, u.ID)
		assert.Equal(t, "ada@example.com", u.Email)

		got, err := repo.FindByID(ctx, id.String())
		require.NoError(t, err)
		assert.Equal(t, "Ada", got.FirstName)
		assert.Equal(t, "ada@example.com", got.Email)
		assert.Equal(t, "+32470123456", *got.PhoneNumber)
		assert.True(t, got.NotifyMarketing, "new accounts default to opted in")
		assert.True(t, got.NotifyOrderUpdates)
	})

	t.Run("Save turns a unique violation into ErrDuplicateUser", func(t *testing.T) {
		_, err := repo.Save(ctx, &domain.User{FirstName: "A", LastName: "B", Email: "dup@example.com"})
		require.NoError(t, err)
		_, err = repo.Save(ctx, &domain.User{FirstName: "C", LastName: "D", Email: " DUP@example.com"})
		require.ErrorIs(t, err, domain.ErrDuplicateUser)
	})

	t.Run("Save passes other database errors through untouched", func(t *testing.T) {
		// A zitadel id of the wrong kind is not a unique violation; use an invalid byte sequence.
		_, err := repo.Save(ctx, &domain.User{FirstName: "bad\x00name", LastName: "x", Email: "nul@example.com"})
		require.Error(t, err)
		assert.NotErrorIs(t, err, domain.ErrDuplicateUser)
	})

	t.Run("lookups by email (any capitalisation), id and zitadel id", func(t *testing.T) {
		id, err := repo.Save(ctx, &domain.User{FirstName: "Grace", LastName: "Hopper", Email: "grace@example.com", ZitadelUserID: ptr("zit-grace")})
		require.NoError(t, err)

		byMail, err := repo.FindByEmail(ctx, " GRACE@example.com ")
		require.NoError(t, err)
		assert.Equal(t, id, byMail.ID)

		byZit, err := repo.FindByZitadelID(ctx, "zit-grace")
		require.NoError(t, err)
		assert.Equal(t, id, byZit.ID)

		_, err = repo.FindByEmail(ctx, "nobody@example.com")
		assert.ErrorIs(t, err, sql.ErrNoRows)
		_, err = repo.FindByZitadelID(ctx, "zit-nobody")
		assert.ErrorIs(t, err, sql.ErrNoRows)
		_, err = repo.FindByID(ctx, uuid.NewString())
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("UpdateUser persists every editable field and returns the stored row", func(t *testing.T) {
		id, err := repo.Save(ctx, &domain.User{FirstName: "Alan", LastName: "Turing", Email: "alan@example.com"})
		require.NoError(t, err)
		got, err := repo.FindByID(ctx, id.String())
		require.NoError(t, err)

		got.FirstName, got.LastName = "Alan M.", "Turing-Jr"
		got.Email = " ALAN.NEW@Example.com"
		got.PhoneNumber = ptr("+32499000000")
		got.DefaultPlaceID = ptr("place-42")
		got.NotifyMarketing, got.NotifyOrderUpdates = false, false
		got.ZitadelUserID = ptr("zit-alan")

		updated, err := repo.UpdateUser(ctx, got)
		require.NoError(t, err)
		assert.Equal(t, "Alan M.", updated.FirstName)
		assert.Equal(t, "Turing-Jr", updated.LastName)
		assert.Equal(t, "alan.new@example.com", updated.Email)
		assert.Equal(t, "+32499000000", *updated.PhoneNumber)
		assert.Equal(t, "place-42", *updated.DefaultPlaceID)
		assert.False(t, updated.NotifyMarketing)
		assert.False(t, updated.NotifyOrderUpdates)
		assert.Equal(t, "zit-alan", *updated.ZitadelUserID)
	})

	t.Run("UpdateUser reports a clash with another account's email", func(t *testing.T) {
		a, err := repo.Save(ctx, &domain.User{FirstName: "A", LastName: "A", Email: "a1@example.com"})
		require.NoError(t, err)
		_, err = repo.Save(ctx, &domain.User{FirstName: "B", LastName: "B", Email: "b1@example.com"})
		require.NoError(t, err)
		u, err := repo.FindByID(ctx, a.String())
		require.NoError(t, err)
		u.Email = "b1@example.com"
		_, err = repo.UpdateUser(ctx, u)
		require.Error(t, err)
	})

	t.Run("AnonymizeForDeletion erases personal data, keeps the row and the zitadel link, drops push tokens", func(t *testing.T) {
		id, err := repo.Save(ctx, &domain.User{FirstName: "Eve", LastName: "Online", Email: "eve@example.com", PhoneNumber: ptr("+32470000001"), ZitadelUserID: ptr("zit-eve")})
		require.NoError(t, err)
		other, err := repo.Save(ctx, &domain.User{FirstName: "Other", LastName: "One", Email: "other@example.com", PhoneNumber: ptr("+32470000002")})
		require.NoError(t, err)
		for _, owner := range []uuid.UUID{id, other} {
			_, err = tdb.DB.ExecContext(ctx, `INSERT INTO device_push_tokens (user_id, device_token, platform) VALUES ($1, 'tok', 'ios')`, owner)
			require.NoError(t, err)
		}
		u, err := repo.FindByID(ctx, id.String())
		require.NoError(t, err)
		u.NotifyMarketing, u.NotifyOrderUpdates, u.DefaultPlaceID = true, true, ptr("place-1")
		_, err = repo.UpdateUser(ctx, u)
		require.NoError(t, err)

		require.NoError(t, repo.AnonymizeForDeletion(ctx, id.String()))

		got, err := repo.FindByID(ctx, id.String())
		require.NoError(t, err)
		assert.Equal(t, "Anonyme", got.FirstName)
		assert.Equal(t, "Anonyme", got.LastName)
		assert.Equal(t, "deleted+"+id.String()+"@deleted.invalid", got.Email)
		assert.Nil(t, got.PhoneNumber)
		assert.Nil(t, got.AddressID)
		assert.Nil(t, got.DefaultPlaceID)
		assert.False(t, got.NotifyMarketing)
		assert.False(t, got.NotifyOrderUpdates)
		assert.NotNil(t, got.DeletionRequestedAt)
		require.NotNil(t, got.ZitadelUserID, "kept so a stale token cannot resurrect the account")
		assert.Equal(t, "zit-eve", *got.ZitadelUserID)

		var tokens int
		require.NoError(t, tdb.DB.GetContext(ctx, &tokens, `SELECT count(*) FROM device_push_tokens WHERE user_id = $1`, id))
		assert.Zero(t, tokens)
		require.NoError(t, tdb.DB.GetContext(ctx, &tokens, `SELECT count(*) FROM device_push_tokens WHERE user_id = $1`, other))
		assert.Equal(t, 1, tokens, "other users keep their tokens")

		untouched, err := repo.FindByID(ctx, other.String())
		require.NoError(t, err)
		assert.Equal(t, "Other", untouched.FirstName)
	})

	t.Run("BatchGetUsersByOrderIDs groups the customer of each order", func(t *testing.T) {
		fx := testhelpers.SeedTestData(t, tdb.DB)
		var o1, o2 uuid.UUID
		for _, dst := range []*uuid.UUID{&o1, &o2} {
			require.NoError(t, tdb.DB.GetContext(ctx, dst, `
				INSERT INTO orders (user_id, order_status, order_type, is_online_payment, total_price, language)
				VALUES ($1, 'PENDING', 'PICKUP', false, 10, 'fr') RETURNING id`, fx.RegularUser.ID))
		}

		got, err := repo.BatchGetUsersByOrderIDs(ctx, []string{o1.String(), o2.String(), uuid.NewString()})
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.Len(t, got[o1.String()], 1)
		assert.Equal(t, fx.RegularUser.ID, got[o1.String()][0].ID)
		assert.Equal(t, "John", got[o2.String()][0].FirstName)

		none, err := repo.BatchGetUsersByOrderIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, none)
	})

	t.Run("a closed connection is reported by every method", func(t *testing.T) {
		conn := testhelpers.ClosedDB(t)
		closed := NewUserRepository(&db.DBPool{Customer: conn, Admin: conn})

		_, err := closed.Save(ctx, &domain.User{Email: "x@example.com"})
		assert.Error(t, err)
		assert.NotErrorIs(t, err, domain.ErrDuplicateUser)
		_, err = closed.FindByEmail(ctx, "x")
		assert.Error(t, err)
		_, err = closed.FindByID(ctx, uuid.NewString())
		assert.Error(t, err)
		_, err = closed.FindByZitadelID(ctx, "x")
		assert.Error(t, err)
		_, err = closed.UpdateUser(ctx, &domain.User{})
		assert.Error(t, err)
		assert.ErrorContains(t, closed.AnonymizeForDeletion(ctx, uuid.NewString()), "anonymize user")
		_, err = closed.BatchGetUsersByOrderIDs(ctx, []string{uuid.NewString()})
		assert.ErrorContains(t, err, "failed to batch")
	})

	t.Run("AnonymizeForDeletion reports a failure removing push tokens", func(t *testing.T) {
		id, err := repo.Save(ctx, &domain.User{FirstName: "Z", LastName: "Z", Email: "z@example.com"})
		require.NoError(t, err)
		_, err = tdb.DB.ExecContext(ctx, `ALTER TABLE device_push_tokens RENAME TO device_push_tokens_gone`)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = tdb.DB.Exec(`ALTER TABLE device_push_tokens_gone RENAME TO device_push_tokens`) })

		assert.ErrorContains(t, repo.AnonymizeForDeletion(ctx, id.String()), "delete device push tokens")
	})
}

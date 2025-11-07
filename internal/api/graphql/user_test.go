package graphql_test

import (
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/testhelpers"
)

// TestMeQuery tests the me query (requires @auth)
func TestMeQuery(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Query me with valid token", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		// Generate token for regular user
		token, err := testhelpers.GenerateTestAccessToken(ctx.Fixtures.RegularUser.ID.String(), false)
		require.NoError(t, err)

		var resp struct {
			Me struct {
				ID        string
				FirstName string
				LastName  string
				Email     string
			}
		}

		query := `
			query {
				me {
					id
					firstName
					lastName
					email
				}
			}
		`

		c.MustPost(query, &resp, client.AddHeader("Authorization", "Bearer "+token))

		// Verify user data
		assert.Equal(t, ctx.Fixtures.RegularUser.ID.String(), resp.Me.ID)
		assert.Equal(t, "John", resp.Me.FirstName)
		assert.Equal(t, "Doe", resp.Me.LastName)
		assert.Equal(t, "john@example.com", resp.Me.Email)
	})

	t.Run("Query me without token should fail", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			Me struct {
				ID string
			}
		}

		query := `
			query {
				me {
					id
				}
			}
		`

		err := c.Post(query, &resp)

		// Should fail due to @auth directive
		require.Error(t, err)
		assert.Contains(t, err.Error(), "UNAUTHENTICATED")
	})

	t.Run("Query me with expired token should fail", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		// Generate expired token
		expiredToken, err := testhelpers.GenerateExpiredToken(ctx.Fixtures.RegularUser.ID.String(), false)
		require.NoError(t, err)

		var resp struct {
			Me struct {
				ID string
			}
		}

		query := `
			query {
				me {
					id
				}
			}
		`

		err = c.Post(query, &resp, client.AddHeader("Authorization", "Bearer "+expiredToken))

		// Should fail due to expired token
		require.Error(t, err)
	})
}

// TestUpdateMe tests the updateMe mutation (requires @auth)
func TestUpdateMe(t *testing.T) {
	ctx := setupTestContext(t)

	t.Run("Update own profile with valid token", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		// Generate token for regular user
		token, err := testhelpers.GenerateTestAccessToken(ctx.Fixtures.RegularUser.ID.String(), false)
		require.NoError(t, err)

		var resp struct {
			UpdateMe struct {
				ID        string
				FirstName string
				LastName  string
			}
		}

		mutation := `
			mutation($input: UpdateUserInput!) {
				updateMe(input: $input) {
					id
					firstName
					lastName
				}
			}
		`

		newFirstName := "Johnny"
		newLastName := "Doeson"
		input := map[string]interface{}{
			"firstName": newFirstName,
			"lastName":  newLastName,
		}

		c.MustPost(mutation, &resp,
			client.Var("input", input),
			client.AddHeader("Authorization", "Bearer "+token),
		)

		// Verify user was updated
		assert.Equal(t, ctx.Fixtures.RegularUser.ID.String(), resp.UpdateMe.ID)
		assert.Equal(t, "Johnny", resp.UpdateMe.FirstName)
		assert.Equal(t, "Doeson", resp.UpdateMe.LastName)
	})

	t.Run("Update profile without token should fail", func(t *testing.T) {
		c := client.New(ctx.Client.Handler())

		var resp struct {
			UpdateMe struct {
				ID string
			}
		}

		mutation := `
			mutation($input: UpdateUserInput!) {
				updateMe(input: $input) {
					id
				}
			}
		`

		input := map[string]interface{}{
			"firstName": "Hacker",
		}

		err := c.Post(mutation, &resp, client.Var("input", input))

		// Should fail due to @auth directive
		require.Error(t, err)
		assert.Contains(t, err.Error(), "UNAUTHENTICATED")
	})
}

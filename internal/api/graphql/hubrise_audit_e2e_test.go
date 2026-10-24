package graphql_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise/hubrisetest"
)

type auditRow struct {
	Action    string  `db:"action"`
	ActorKind string  `db:"actor_kind"`
	Variables *string `db:"variables"`
	Success   bool    `db:"success"`
	Error     *string `db:"error"`
}

func (r *e2eRig) auditRows() []auditRow {
	r.t.Helper()
	var rows []auditRow
	require.NoError(r.t, r.DB.DB.SelectContext(r.t.Context(), &rows,
		`SELECT action, actor_kind, variables::text AS variables, success, error FROM staff_audit_log ORDER BY id`))
	return rows
}

// Connecting and disconnecting HubRise leave their own entries in the staff audit log, and the
// GraphQL mutations of the admin API are recorded like every other staff mutation. None of it
// carries a secret: not the access token, not the client secret, not the authorization code.
func TestHubriseConnectionAndAdminMutationsAreAuditedWithoutSecrets(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	srv := hubrisetest.New(t)
	r := env.withHubriseInstalled(t, srv)
	// The admin goes through the real GraphQL endpoint (the handler of main, with the audit
	// extension), as the dashboard does.
	ep := newEndpoint(t, env, env.Resolver)
	admin := signZitadelToken(t, ep.key, env.Fixtures.AdminUser.ID.String(), true, time.Hour)
	mutate := func(query string) graphqlResponseWithExtensions {
		t.Helper()
		resp := ep.post(t, admin, query)
		require.Empty(t, resp.Errors, "%s: %+v", query, resp.Errors)
		return resp
	}

	// The OAuth round trip, keeping the one-time values to look for them in the log afterwards.
	var started struct{ StartHubriseConnection string }
	require.NoError(t, json.Unmarshal(mutate(`mutation { startHubriseConnection }`).Data, &started))
	authURL := started.StartHubriseConnection
	state, code := browse(t, srv, authURL)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(r.web.URL + "/api/v1/hubrise/oauth/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	mutate(`mutation { updateHubriseSettings(input: { enabled: true, pushOrders: true }) { enabled } }`)
	mutate(`mutation { previewHubriseCatalogSync(mode: DRY_RUN) { catalogHash } }`)
	mutate(`mutation { disconnectHubrise(forgetLinks: true) { connected } }`)

	rows := r.auditRows()
	byAction := map[string][]auditRow{}
	for _, row := range rows {
		byAction[row.Action] = append(byAction[row.Action], row)
	}

	t.Run("connect and disconnect have their own entries", func(t *testing.T) {
		require.Len(t, byAction["hubrise.connect"], 1)
		assert.True(t, byAction["hubrise.connect"][0].Success)
		assert.Equal(t, "admin", byAction["hubrise.connect"][0].ActorKind)

		require.Len(t, byAction["hubrise.disconnect"], 1)
		disconnect := byAction["hubrise.disconnect"][0]
		assert.True(t, disconnect.Success)
		assert.Equal(t, "admin", disconnect.ActorKind, "attributed to the admin who clicked")
		require.NotNil(t, disconnect.Variables)
		var details map[string]any
		require.NoError(t, json.Unmarshal([]byte(*disconnect.Variables), &details))
		assert.Equal(t, true, details["forget_links"])
		assert.Equal(t, true, details["revoked"], "the token was revoked at HubRise")
		assert.Contains(t, details, "links_forgotten")
	})

	t.Run("every admin mutation is recorded as a staff mutation", func(t *testing.T) {
		for _, action := range []string{
			"graphql:startHubriseConnection", "graphql:updateHubriseSettings",
			"graphql:previewHubriseCatalogSync", "graphql:disconnectHubrise",
		} {
			require.Len(t, byAction[action], 1, action)
			assert.True(t, byAction[action][0].Success, action)
			assert.Equal(t, "admin", byAction[action][0].ActorKind, action)
		}
		assert.Empty(t, byAction["graphql:hubriseStatus"], "queries are not audited")
	})

	t.Run("no secret reaches the log", func(t *testing.T) {
		secrets := map[string]string{
			"access token": hubrisetest.Token, "client secret": hubrisetest.ClientSecret,
			"authorization code": code, "oauth state": state,
		}
		for _, row := range rows {
			text := row.Action
			if row.Variables != nil {
				text += " " + *row.Variables
			}
			if row.Error != nil {
				text += " " + *row.Error
			}
			for what, secret := range secrets {
				require.NotEmpty(t, secret, what)
				assert.False(t, strings.Contains(text, secret), "the %s is in the audit entry %s", what, row.Action)
			}
		}
	})
}

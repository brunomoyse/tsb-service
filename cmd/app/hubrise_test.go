package main

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"tsb-service/internal/api/graphql/resolver"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
	orderApplication "tsb-service/internal/modules/order/application"
	productApplication "tsb-service/internal/modules/product/application"
	userApplication "tsb-service/internal/modules/user/application"
	"tsb-service/pkg/db"
)

// The services initHubrise installs observers on, without a database.
type orderServiceSpy struct {
	orderApplication.OrderService
	observer orderApplication.StatusObserver
}

func (s *orderServiceSpy) SetStatusObserver(o orderApplication.StatusObserver) { s.observer = o }

type userServiceSpy struct {
	userApplication.UserService
	observer userApplication.ProfileObserver
}

func (s *userServiceSpy) SetProfileObserver(o userApplication.ProfileObserver) { s.observer = o }

type productServiceSpy struct {
	productApplication.ProductService
	observer productApplication.MenuObserver
}

func (s *productServiceSpy) SetMenuObserver(o productApplication.MenuObserver) { s.observer = o }

// hubriseEnv sets the whole HubRise environment of a test (every variable, so nothing leaks in from
// the machine running the tests).
func hubriseEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range []string{
		"HUBRISE_CLIENT_ID", "HUBRISE_CLIENT_SECRET", "HUBRISE_TOKEN_KEY", "HUBRISE_API_BASE_URL",
		"HUBRISE_OAUTH_BASE_URL", "HUBRISE_REPLICAS", "API_BASE_URL", "ASSISTANT_ALERT_EMAIL",
	} {
		t.Setenv(name, env[name])
	}
}

func logsOf(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)
	return logs
}

func TestInitHubrise(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(hubtest.TestTokenKey)
	full := map[string]string{
		"HUBRISE_CLIENT_ID": "id", "HUBRISE_CLIENT_SECRET": "very-secret", "HUBRISE_TOKEN_KEY": key,
		"API_BASE_URL": "https://shop.example.test/api/v1",
	}
	with := func(over map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range full {
			env[k] = v
		}
		for k, v := range over {
			env[k] = v
		}
		return env
	}

	tests := []struct {
		name    string
		env     map[string]string
		on      bool
		level   zapcore.Level // level of the line logged, when logged is not empty
		logged  string
		quiet   bool // nothing at all is logged above debug
		secrets []string
	}{
		{name: "nothing set is a restaurant without HubRise, silently", env: map[string]string{}, quiet: true},
		{
			name: "a partial configuration only warns", level: zapcore.WarnLevel, logged: "incomplete configuration",
			env:     map[string]string{"HUBRISE_CLIENT_ID": "id", "HUBRISE_CLIENT_SECRET": "very-secret"},
			secrets: []string{"very-secret"},
		},
		{
			name: "a malformed token key is logged as an error and never stops the shop", level: zapcore.ErrorLevel,
			logged: "invalid configuration", env: with(map[string]string{"HUBRISE_TOKEN_KEY": "not-a-key"}),
			secrets: []string{"very-secret", "not-a-key"},
		},
		{
			name: "a malformed base URL is an error and the integration stays off", level: zapcore.ErrorLevel,
			logged: "invalid configuration", env: with(map[string]string{"HUBRISE_API_BASE_URL": "ftp://nope"}),
			secrets: []string{"very-secret"},
		},
		{
			name:  "an unusable public API address makes Install fail and the integration stays off",
			level: zapcore.ErrorLevel, logged: "cannot install", env: with(map[string]string{"API_BASE_URL": "not a url"}),
			secrets: []string{"very-secret"},
		},
		{
			name: "a complete configuration installs the integration", on: true, level: zapcore.InfoLevel,
			logged: "integration available", env: with(nil), secrets: []string{"very-secret", key},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hubriseEnv(t, tc.env)
			logs := logsOf(t)
			orders, users, products := &orderServiceSpy{}, &userServiceSpy{}, &productServiceSpy{}
			r := &resolver.Resolver{OrderService: orders, UserService: users, ProductService: products}
			router := gin.New()

			rt := initHubrise(router.Group("/api/v1"), r, &db.DBPool{Admin: &sqlx.DB{}}, nil, "https://dash.example.test/")

			if !tc.on {
				assert.Nil(t, rt)
				assert.Nil(t, r.Hubrise, "no hooks")
				assert.Nil(t, r.HubriseAdmin)
				assert.Nil(t, orders.observer, "no status observer")
				assert.Nil(t, users.observer, "no profile observer")
				assert.Nil(t, products.observer, "no menu observer")
				assert.Empty(t, router.Routes(), "no HubRise route")
			} else {
				require.NotNil(t, rt)
				t.Cleanup(rt.Stop)
				assert.NotNil(t, r.Hubrise)
				assert.NotNil(t, orders.observer)
				assert.NotNil(t, users.observer)
				assert.NotNil(t, products.observer, "menu edits reach the publisher")
				var routes []string
				for _, route := range router.Routes() {
					routes = append(routes, route.Method+" "+route.Path)
				}
				assert.ElementsMatch(t, []string{"POST /api/v1/hubrise/callback", "GET /api/v1/hubrise/oauth/callback"}, routes)
			}

			var noisy []observer.LoggedEntry
			for _, e := range logs.All() {
				if e.Level >= zapcore.InfoLevel {
					noisy = append(noisy, e)
				}
				all := fmt.Sprint(e.Message, e.ContextMap())
				for _, s := range tc.secrets {
					assert.NotContains(t, all, s, "a secret reached the log")
				}
			}
			if tc.quiet {
				assert.Empty(t, noisy, "an unconfigured shop says nothing")
				return
			}
			require.Len(t, noisy, 1, "exactly one line")
			assert.Equal(t, tc.level, noisy[0].Level)
			assert.Contains(t, noisy[0].Message, tc.logged)
		})
	}
}

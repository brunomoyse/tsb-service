package application_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/interfaces"
	pkghubrise "tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

const dashboardURL = "https://pili.example.test/hubrise"

// oauthRig is the OAuth callback endpoint on a router, next to an admin on the mock.
type oauthRig struct {
	*adminRig
	router   *gin.Engine
	failures *countingLimiter
}

func newOAuthRig(t *testing.T, options ...envOption) *oauthRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := &oauthRig{adminRig: newAdminRig(t, options...), failures: &countingLimiter{allowed: 30}}
	r.mount(r.admin, dashboardURL)
	return r
}

func (r *oauthRig) mount(admin *application.Admin, dashboard string) {
	r.router = gin.New()
	interfaces.NewOAuthHandler(admin, dashboard, r.failures).Register(r.router)
}

// browse plays the browser: it asks the authorization server for approval, and returns the path and
// query HubRise redirects it to (our callback).
func (r *oauthRig) browse(adminID uuid.UUID) string {
	r.t.Helper()
	authURL, err := r.admin.StartConnection(r.t.Context(), adminID)
	require.NoError(r.t, err)
	client := *r.srv.HTTPClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(authURL)
	require.NoError(r.t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(r.t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(r.t, err)
	require.Equal(r.t, "/api/v1/hubrise/oauth/callback", loc.Path)
	return "/hubrise/oauth/callback?" + loc.RawQuery
}

func (r *oauthRig) get(target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	r.router.ServeHTTP(rec, req)
	return rec
}

func landedOn(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "pili.example.test", loc.Host)
	assert.Equal(t, "/hubrise", loc.Path)
	return loc.Query()
}

func TestTheBrowserComesBackConnectedAndLandsOnTheDashboard(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	adminID := uuid.New()

	rec := r.get(r.browse(adminID))

	assert.Equal(t, interfaces.OutcomeConnected, landedOn(t, rec).Get("hubrise"))
	require.True(t, r.conn().Connected())
	assert.NotNil(t, r.srv.Callback(hubrisetest.Token))
	entries := r.audit.all()
	require.Len(t, entries, 1)
	assert.Equal(t, adminID.String(), entries[0].UserID)
	assert.NotContains(t, rec.Header().Get("Location"), hubrisetest.Token, "no secret travels in the redirect")
	assert.Empty(t, r.failures.keys, "a good callback counts against nobody")
}

func TestTheRedirectKeepsTheQueryOfTheDashboardAddress(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	r.mount(r.admin, "https://pili.example.test/hubrise?tab=connection")
	q := landedOn(t, r.get(r.browse(uuid.New())))
	assert.Equal(t, "connection", q.Get("tab"))
	assert.Equal(t, interfaces.OutcomeConnected, q.Get("hubrise"))
}

func TestTheBrowserIsToldWhenARelinkIsNeeded(t *testing.T) {
	r := newOAuthRig(t)
	r.link(r.fx.SalmonSushi.ID, "OLD-SKU")
	r.srv.SetOAuthTarget(&hubrisetest.OAuthTarget{LocationID: "loc-2", LocationName: "Other", CatalogID: "cat-2", CatalogName: "Other"})

	rec := r.get(r.browse(uuid.New()))

	assert.Equal(t, interfaces.OutcomeRelinkNeeded, landedOn(t, rec).Get("hubrise"))
	assert.False(t, r.conn().Enabled)
}

func TestAnAdminWhoRefusesComesBackDenied(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	state, _ := r.authorize(uuid.New())

	rec := r.get("/hubrise/oauth/callback?error=access_denied&state=" + url.QueryEscape(state))

	assert.Equal(t, interfaces.OutcomeDenied, landedOn(t, rec).Get("hubrise"))
	_, err := r.mod.Connections.Get(t.Context())
	assert.Error(t, err)
}

func TestACallbackWithoutStateOrCodeIsAnInvalidState(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	for _, target := range []string{
		"/hubrise/oauth/callback",
		"/hubrise/oauth/callback?code=abc",
		"/hubrise/oauth/callback?state=abc",
		"/hubrise/oauth/callback?state=",
	} {
		assert.Equal(t, interfaces.OutcomeInvalidState, landedOn(t, r.get(target)).Get("hubrise"), target)
	}
	assert.Len(t, r.failures.keys, 4, "each counts against the client address")
	assert.Equal(t, "203.0.113.9", r.failures.keys[0])
}

func TestAnUnknownOrReplayedStateIsRefusedAndCounted(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	target := r.browse(uuid.New())
	require.Equal(t, interfaces.OutcomeConnected, landedOn(t, r.get(target)).Get("hubrise"))

	assert.Equal(t, interfaces.OutcomeInvalidState, landedOn(t, r.get(target)).Get("hubrise"), "replay")
	assert.Equal(t, interfaces.OutcomeInvalidState, landedOn(t, r.get("/hubrise/oauth/callback?code=x&state=forged")).Get("hubrise"))
	assert.Len(t, r.failures.keys, 2)
}

func TestForgedStatesAreRateLimited(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	r.failures.allowed = 1

	assert.Equal(t, interfaces.OutcomeInvalidState, landedOn(t, r.get("/hubrise/oauth/callback?code=x&state=a")).Get("hubrise"))
	rec := r.get("/hubrise/oauth/callback?code=x&state=b")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))

	rec = r.get("/hubrise/oauth/callback")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "the same limit for a request with nothing in it")
}

func TestARefusedCodeComesBackAsAFailedAuthorization(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	state, _ := r.authorize(uuid.New())

	rec := r.get("/hubrise/oauth/callback?code=forged&state=" + url.QueryEscape(state))

	assert.Equal(t, interfaces.OutcomeNotAuthorized, landedOn(t, rec).Get("hubrise"))
	assert.NotContains(t, rec.Header().Get("Location"), "forged")
}

type failingCodec struct{}

func (failingCodec) Encrypt(string) ([]byte, error) { return nil, errors.New("no key") }
func (failingCodec) Decrypt([]byte) (string, error) { return "", errors.New("no key") }

func TestAnInternalFailureComesBackAsAnError(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	broken := application.NewAdmin(application.AdminDeps{
		Config: application.AdminConfig{RedirectURL: adminAPIBase + "/x", CallbackURL: adminCallback},
		OAuth:  r.srv.NewOAuth(t), Connections: r.mod.Connections, OAuthStates: r.mod.OAuthStates, Links: r.mod.CatalogLinks,
		Orders: r.mod.Orders, Outbox: r.mod.Outbox, Codec: failingCodec{}, Catalog: r.catalog.Service, Clock: r.clock,
		TokenClient: func(token string) (*pkghubrise.Client, error) {
			return pkghubrise.New(r.srv.APIBaseURL(), token, pkghubrise.WithHTTPClient(r.srv.HTTPClient()))
		},
	})
	r.mount(broken, dashboardURL)
	state, code := func() (string, string) {
		authURL, err := broken.StartConnection(t.Context(), uuid.New())
		require.NoError(t, err)
		u, _ := url.Parse(authURL)
		return u.Query().Get("state"), r.srv.IssueAuthCode()
	}()

	rec := r.get("/hubrise/oauth/callback?code=" + code + "&state=" + url.QueryEscape(state))

	assert.Equal(t, interfaces.OutcomeError, landedOn(t, rec).Get("hubrise"))
	_, err := r.mod.Connections.Get(t.Context())
	assert.Error(t, err, "nothing was stored")
}

func TestAMisconfiguredDashboardAddressIsNotARedirect(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	for _, bad := range []string{"", "not a url", "/relative"} {
		r.mount(r.admin, bad)
		rec := r.get("/hubrise/oauth/callback")
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, bad)
	}
}

func TestTheOAuthCallbackWorksWithoutALimiter(t *testing.T) {
	r := newOAuthRig(t, withoutConnection())
	r.router = gin.New()
	interfaces.NewOAuthHandler(r.admin, dashboardURL, nil).Register(r.router)
	assert.Equal(t, interfaces.OutcomeInvalidState, landedOn(t, r.get("/hubrise/oauth/callback?code=x&state=y")).Get("hubrise"))
}

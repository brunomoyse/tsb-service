package hubrise_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func TestNewOAuthValidatesItsInput(t *testing.T) {
	for _, tc := range []struct{ base, id, secret string }{
		{"", "id", "s"}, {"manager.hubrise.com", "id", "s"}, {"https://m/oauth2/v1", "", "s"}, {"https://m/oauth2/v1", "id", ""},
	} {
		_, err := hubrise.NewOAuth(tc.base, tc.id, tc.secret)
		assert.Error(t, err, "%+v", tc)
	}
}

func TestAuthorizeURL(t *testing.T) {
	o, err := hubrise.NewOAuth("https://manager.hubrise.com/oauth2/v1/", "cid.clients.hubrise.com", "s3cret")
	require.NoError(t, err)
	raw := o.AuthorizeURL("https://api.test/api/v1/hubrise/oauth/callback", hubrise.DefaultScope, "nonce-1")
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "manager.hubrise.com", u.Host)
	assert.Equal(t, "/oauth2/v1/authorize", u.Path)
	q := u.Query()
	assert.Equal(t, "cid.clients.hubrise.com", q.Get("client_id"))
	assert.Equal(t, "https://api.test/api/v1/hubrise/oauth/callback", q.Get("redirect_uri"))
	assert.Equal(t, hubrise.DefaultScope, q.Get("scope"))
	assert.Equal(t, "nonce-1", q.Get("state"))
	assert.NotContains(t, raw, "s3cret")
	assert.Contains(t, hubrise.DefaultScope, "inventory.write")
	assert.Contains(t, hubrise.DefaultScope, "customer_list.write")
	assert.Contains(t, hubrise.DefaultScope, "catalog.write", "the dashboard-owned menu is published with catalog.write")

	noState := o.AuthorizeURL("https://x/cb", "profile", "")
	assert.NotContains(t, noState, "state=")
}

// The exchange is exactly what docs/hubrise/02-authentication.md specifies: POST /token, the client
// in HTTP Basic, the code in a form body.
func TestExchangeCodeRequestShape(t *testing.T) {
	var gotAuth, gotType, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotType, gotPath = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		respond(200, `{"access_token":"tok-abc","token_type":"Bearer","account_id":"a1","location_id":"l1","catalog_id":"c1","customer_list_id":"cl1","account_name":"Acc","location_name":"Loc","catalog_name":"Cat","customer_list_name":"Cust"}`)(w, r)
	}))
	t.Cleanup(srv.Close)
	o, err := hubrise.NewOAuth(srv.URL+"/oauth2/v1", "cid", "csecret")
	require.NoError(t, err)

	tok, err := o.ExchangeCode(context.Background(), "the code&=")
	require.NoError(t, err)
	assert.Equal(t, "/oauth2/v1/token", gotPath)
	assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("cid:csecret")), gotAuth)
	assert.Equal(t, "application/x-www-form-urlencoded", gotType)
	assert.Equal(t, "code=the+code%26%3D", gotBody)
	assert.Equal(t, hubrise.TokenResponse{
		AccessToken: "tok-abc", TokenType: "Bearer", AccountID: "a1", LocationID: "l1", CatalogID: "c1", CustomerListID: "cl1",
		AccountName: "Acc", LocationName: "Loc", CatalogName: "Cat", CustomerListName: "Cust",
	}, *tok)
}

func TestExchangeCodeAgainstTheMock(t *testing.T) {
	srv := hubrisetest.New(t)
	o := srv.NewOAuth(t)

	code := srv.IssueAuthCode()
	tok, err := o.ExchangeCode(ctx, code)
	require.NoError(t, err)
	assert.Equal(t, hubrisetest.Token, tok.AccessToken)
	assert.Equal(t, hubrisetest.LocationID, tok.LocationID)
	assert.Equal(t, hubrisetest.CatalogID, tok.CatalogID)
	assert.Equal(t, hubrisetest.CustomerListID, tok.CustomerListID)
	assert.Equal(t, hubrisetest.CatalogName, tok.CatalogName)
	assert.Contains(t, tok.String(), "location=15n44-1")

	// The token works.
	c, err := hubrise.New(srv.APIBaseURL(), tok.AccessToken, hubrise.WithHTTPClient(srv.HTTPClient()))
	require.NoError(t, err)
	_, err = c.GetLocation(ctx)
	require.NoError(t, err)

	// A code is single use.
	_, err = o.ExchangeCode(ctx, code)
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindInvalid, he.Kind)
	assert.Equal(t, 400, he.Status)
	_, err = o.ExchangeCode(ctx, "made-up")
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err))
}

func TestWrongClientCredentialsAreUnauthorizedAndNeverShown(t *testing.T) {
	srv := hubrisetest.New(t)
	o, err := hubrise.NewOAuth(srv.OAuthBaseURL(), hubrisetest.ClientID, "wrong-secret-value", hubrise.WithOAuthHTTPClient(srv.HTTPClient()))
	require.NoError(t, err)
	code := srv.IssueAuthCode()

	_, err = o.ExchangeCode(ctx, code)
	assert.Equal(t, hubrise.KindUnauthorized, hubrise.KindOf(err))
	assert.NotContains(t, err.Error(), "wrong-secret-value")
	assert.NotContains(t, err.Error(), code)
	err = o.Revoke(ctx, hubrisetest.Token)
	assert.Equal(t, hubrise.KindUnauthorized, hubrise.KindOf(err))
}

func TestRevokeMakesTheTokenUnusable(t *testing.T) {
	srv := hubrisetest.New(t)
	o := srv.NewOAuth(t)
	c := srv.NewClient(t)
	_, err := c.GetLocation(ctx)
	require.NoError(t, err)

	require.NoError(t, o.Revoke(ctx, hubrisetest.Token))
	assert.True(t, srv.TokenRevoked(hubrisetest.Token))
	_, err = c.GetLocation(ctx)
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindUnauthorized, he.Kind)
	assert.True(t, he.ConnectionLevel())

	err = o.Revoke(ctx, "unknown-token")
	assert.Equal(t, hubrise.KindInvalid, hubrise.KindOf(err))

	// Authorizing the location again gives the same token back, working again.
	_, err = o.ExchangeCode(ctx, srv.IssueAuthCode())
	require.NoError(t, err)
	_, err = c.GetLocation(ctx)
	require.NoError(t, err)

	srv.RevokeToken(hubrisetest.Token)
	_, err = c.GetLocation(ctx)
	assert.Equal(t, hubrise.KindUnauthorized, hubrise.KindOf(err))
}

func TestAnUnknownTokenIsUnauthorized(t *testing.T) {
	srv := hubrisetest.New(t)
	c, err := hubrise.New(srv.APIBaseURL(), "never-issued", hubrise.WithHTTPClient(srv.HTTPClient()))
	require.NoError(t, err)
	_, err = c.GetLocation(ctx)
	assert.Equal(t, hubrise.KindUnauthorized, hubrise.KindOf(err))
}

func TestTheMockAuthorizeEndpointPlaysTheUser(t *testing.T) {
	srv := hubrisetest.New(t)
	o := srv.NewOAuth(t)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get(o.AuthorizeURL("https://shop.test/cb", hubrise.DefaultScope, "st4te"))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "shop.test", loc.Host)
	assert.Equal(t, "st4te", loc.Query().Get("state"))
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)

	tok, err := o.ExchangeCode(ctx, code)
	require.NoError(t, err)
	assert.Equal(t, hubrisetest.Token, tok.AccessToken)

	// The installed app flow shows the code instead of redirecting.
	resp, err = noFollow.Get(srv.OAuthBaseURL() + "/authorize?client_id=" + hubrisetest.ClientID + "&scope=profile&redirect_uri=urn:ietf:wg:oauth:2.0:oob")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.NotEmpty(t, body)

	// Mistakes are refused.
	for _, q := range []string{
		"client_id=other&scope=profile&redirect_uri=https://x/cb",
		"client_id=" + hubrisetest.ClientID + "&redirect_uri=https://x/cb",
		"client_id=" + hubrisetest.ClientID + "&scope=profile",
		"client_id=" + hubrisetest.ClientID + "&scope=profile&redirect_uri=%25zz",
	} {
		resp, err = noFollow.Get(srv.OAuthBaseURL() + "/authorize?" + q)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, 400, resp.StatusCode, q)
	}
}

func TestOAuthTransportAndProtocolFailures(t *testing.T) {
	// Not JSON, then JSON without a token.
	bodies := []string{`<html>`, `{"account_id":"a"}`}
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(200, bodies[i])(w, r)
		i++
	}))
	o, err := hubrise.NewOAuth(srv.URL, "cid", "csecret")
	require.NoError(t, err)
	for range bodies {
		_, err = o.ExchangeCode(ctx, "code")
		var he *hubrise.Error
		require.ErrorAs(t, err, &he)
		assert.Equal(t, hubrise.KindProtocol, he.Kind)
	}

	srv.Close()
	_, err = o.ExchangeCode(ctx, "code")
	assert.Equal(t, hubrise.KindNetwork, hubrise.KindOf(err))
	assert.NotContains(t, err.Error(), "csecret")
	assert.Equal(t, hubrise.KindNetwork, hubrise.KindOf(o.Revoke(ctx, "tok")))
}

func TestOAuthRefusesRedirectsToAnotherHost(t *testing.T) {
	var hit bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	o, err := hubrise.NewOAuth(srv.URL, "cid", "csecret")
	require.NoError(t, err)
	_, err = o.ExchangeCode(ctx, "code")
	assert.Equal(t, hubrise.KindRedirect, hubrise.KindOf(err))
	assert.False(t, hit, "the client credentials must never reach another host")
}

func TestScopeParsing(t *testing.T) {
	assert.Equal(t, []string{"orders.write", "catalog.read"}, hubrise.ScopePermissions("location[orders.write, catalog.read]"))
	assert.Equal(t, []string{"a.b", "c.d"}, hubrise.ScopePermissions("a.b,c.d"))
	assert.Equal(t, []string{"a.b"}, hubrise.ScopePermissions("account[a.b"))
	assert.Empty(t, hubrise.ScopePermissions(""))
	assert.Empty(t, hubrise.ScopePermissions("location[]"))

	assert.True(t, hubrise.ScopeHas(hubrise.DefaultScope, "catalog.write"))
	assert.False(t, hubrise.ScopeHas("location[orders.write,customer_list.write,catalog.read,inventory.read,inventory.write]", "catalog.write"), "the scope of the first release")
	assert.True(t, hubrise.ScopeHas("location[catalog.*]", "catalog.write"))
	assert.False(t, hubrise.ScopeHas("location[catalogs.write]", "catalog.write"))
	assert.False(t, hubrise.ScopeHas("", "catalog.write"))
}

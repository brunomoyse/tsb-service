package hubrise_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/hubrise/hubrisetest"
)

func TestInjectedStatusFaults(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   hubrise.Kind
	}{
		{401, hubrise.KindUnauthorized}, {403, hubrise.KindForbidden}, {404, hubrise.KindNotFound},
		{422, hubrise.KindInvalid}, {429, hubrise.KindRateLimited}, {500, hubrise.KindServer}, {503, hubrise.KindServer},
	} {
		srv := hubrisetest.New(t)
		c := srv.NewClient(t)
		srv.AddFault(hubrisetest.Fault{Method: "POST", Path: "/v1/location/orders", Status: tc.status, Times: 1, RetryAfter: 12 * time.Second})

		_, err := c.GetLocation(ctx)
		require.NoError(t, err, "a fault only hits the requests it matches")

		_, err = c.CreateOrder(ctx, webshopOrder("f1"))
		var he *hubrise.Error
		require.ErrorAs(t, err, &he, tc.status)
		assert.Equal(t, tc.want, he.Kind, tc.status)
		assert.Equal(t, tc.status, he.Status)
		if tc.status == 429 {
			assert.Equal(t, 12*time.Second, he.RetryAfter)
		}
		assert.Empty(t, srv.Orders(), "a request that failed with a status was not processed")

		// (a 429 paused the first client's limiter for Retry-After: use a fresh one)
		_, err = srv.NewClient(t).CreateOrder(ctx, webshopOrder("f1"))
		require.NoError(t, err, "Times: 1 fails one request only")
	}
}

func TestFaultsWithoutTimesLastUntilCleared(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	srv.AddFault(hubrisetest.Fault{Status: 503})
	for range 3 {
		_, err := c.GetLocation(ctx)
		assert.Equal(t, hubrise.KindServer, hubrise.KindOf(err))
	}
	srv.ClearFaults()
	_, err := c.GetLocation(ctx)
	require.NoError(t, err)
}

func TestARequestTheServerNeverAnswersTimesOut(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t, hubrise.WithHTTPClient(&http.Client{Timeout: 100 * time.Millisecond, Transport: srv.HTTPClient().Transport}))
	srv.AddFault(hubrisetest.Fault{Timeout: true, Times: 1})
	_, err := c.GetLocation(ctx)
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindTimeout, he.Kind)
	assert.True(t, he.Retryable())
	require.Eventually(t, func() bool { return len(srv.Requests()) == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Zero(t, srv.Requests()[0].Status, "never answered")
}

func TestASlowServerStillAnswers(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	srv.AddFault(hubrisetest.Fault{Delay: 30 * time.Millisecond, Times: 1})
	start := time.Now()
	_, err := c.GetLocation(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)

	// A delay cut short by the client giving up.
	short := srv.NewClient(t, hubrise.WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond, Transport: srv.HTTPClient().Transport}))
	srv.AddFault(hubrisetest.Fault{Delay: time.Hour, Times: 1})
	_, err = short.GetLocation(ctx)
	assert.Equal(t, hubrise.KindTimeout, hubrise.KindOf(err))
}

// The case that makes the private_ref lookup necessary: HubRise created the order, the answer
// never arrived.
func TestAResponseDroppedAfterProcessing(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)
	srv.AddFault(hubrisetest.Fault{Method: "POST", Path: "/v1/location/orders", DropAfter: true, Times: 1})

	_, err := c.CreateOrder(ctx, webshopOrder("lost-1"))
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindNetwork, he.Kind)
	assert.True(t, he.Retryable())

	assert.Len(t, srv.Orders(), 1, "the order exists at HubRise")
	reqs := srv.RequestsTo("POST", "/v1/location/orders")
	require.Len(t, reqs, 1)
	assert.True(t, reqs[0].Dropped)

	found, err := c.FindOrderByPrivateRef(ctx, "lost-1")
	require.NoError(t, err)
	require.NotNil(t, found, "the lookup by private_ref adopts the order instead of posting it twice")
	assert.Equal(t, "lost-1", found.PrivateRef)
}

func TestMockRateLimits(t *testing.T) {
	srv := hubrisetest.New(t)
	c := srv.NewClient(t)

	srv.SetLimits(hubrisetest.Limits{PerMinute: 3})
	for range 3 {
		_, err := c.GetLocation(ctx)
		require.NoError(t, err)
	}
	_, err := c.GetLocation(ctx)
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindRateLimited, he.Kind)
	assert.Greater(t, he.RetryAfter, time.Duration(0), "HubRise says when to come back")

	// The 10 a minute bucket is shared by the order list and the catalog read. (A 429 paused the
	// previous client's limiter for Retry-After: use a fresh one.)
	srv.SetLimits(hubrisetest.DefaultLimits())
	c = srv.NewClient(t)
	for i := range 6 {
		_, err = c.FindOrderByPrivateRef(ctx, "x")
		require.NoError(t, err, i)
	}
	for i := range 4 {
		_, err = c.GetCatalog(ctx, hubrisetest.CatalogID)
		require.NoError(t, err, i)
	}
	_, err = c.GetCatalog(ctx, hubrisetest.CatalogID)
	assert.Equal(t, hubrise.KindRateLimited, hubrise.KindOf(err))
	c = srv.NewClient(t)
	_, err = c.FindOrderByPrivateRef(ctx, "x")
	assert.Equal(t, hubrise.KindRateLimited, hubrise.KindOf(err))
	c = srv.NewClient(t)
	_, err = c.GetOrder(ctx, "ghost")
	assert.Equal(t, hubrise.KindNotFound, hubrise.KindOf(err), "single order reads are not in the bucket")

	// The hourly and daily windows count too.
	srv2 := hubrisetest.New(t)
	srv2.SetLimits(hubrisetest.Limits{PerHour: 1})
	_, err = srv2.NewClient(t).GetLocation(ctx)
	require.NoError(t, err)
	_, err = srv2.NewClient(t).GetLocation(ctx)
	assert.Equal(t, hubrise.KindRateLimited, hubrise.KindOf(err))
	srv2.SetLimits(hubrisetest.Limits{PerDay: 1})
	_, err = srv2.NewClient(t).GetLocation(ctx)
	assert.Equal(t, hubrise.KindRateLimited, hubrise.KindOf(err))
}

func TestMockRateLimitWindowsSlide(t *testing.T) {
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	srv := hubrisetest.New(t, hubrisetest.WithClock(func() time.Time { return clock }))
	c := srv.NewClient(t)
	srv.SetLimits(hubrisetest.Limits{PerMinute: 1})
	_, err := c.GetLocation(ctx)
	require.NoError(t, err)
	_, err = c.GetLocation(ctx)
	require.Error(t, err)
	clock = clock.Add(61 * time.Second)
	_, err = srv.NewClient(t).GetLocation(ctx)
	require.NoError(t, err)
}

func TestRawProtocolErrorsOfTheMock(t *testing.T) {
	srv := hubrisetest.New(t)
	tok := hubrisetest.Token

	// Writes must be JSON.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/location/orders", stringsReader(`status=new`))
	req.Header.Set("X-Access-Token", tok)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := srv.HTTPClient().Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, 415, resp.StatusCode, string(body))
	assert.Contains(t, string(body), "unsupported_media_type")

	// An unknown field is refused, an invalid JSON too.
	for body, want := range map[string]string{
		`{"status":"new","bogus":1}`: "bogus",
		`{"status":"new","items":[{"product_name":"x","price":"1.00 EUR","quantity":"1","options":[{"option_list_name":"l","name":"n","color":"red"}]}]}`: "color",
		`{"status":`: "invalid JSON",
		``:           "invalid JSON",
	} {
		req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/location/orders", stringsReader(body))
		req.Header.Set("X-Access-Token", tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err = srv.HTTPClient().Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, 422, resp.StatusCode, body)
		assert.Contains(t, string(b), want, body)
	}

	// An unmatched route is HubRise's routing error, a missing token a 401.
	resp = rawRequest(t, srv, http.MethodGet, "/v1/nothing/here", tok, "")
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 404, resp.StatusCode)
	assert.Contains(t, string(b), "routing_error")
	resp = rawRequest(t, srv, http.MethodGet, "/v1/location", "", "")
	assert.Equal(t, 401, resp.StatusCode)
	resp = rawRequest(t, srv, http.MethodDelete, "/v1/location", tok, "")
	assert.Equal(t, 404, resp.StatusCode, "no such method on that route")

	// hide_data returns the catalog without its content.
	resp = rawRequest(t, srv, http.MethodGet, "/v1/catalogs/"+hubrisetest.CatalogID+"?hide_data=true", tok, "")
	b, _ = io.ReadAll(resp.Body)
	assert.NotContains(t, string(b), `"data"`)
}

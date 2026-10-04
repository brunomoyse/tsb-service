package infrastructure

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripFunc stands in for Google: the client talks to fixed https URLs, so the transport is the
// boundary to fake.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, body string) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
func (failingBody) Close() error             { return nil }

func clientWith(rt http.RoundTripper, radius float64) *GoogleClient {
	return NewGoogleClient("secret-key", 50.6, 5.5, radius, &http.Client{Transport: rt}).(*GoogleClient)
}

func TestNewGoogleClientDefaultsToATimeout(t *testing.T) {
	c := NewGoogleClient("k", 0, 0, 0, nil).(*GoogleClient)
	require.NotNil(t, c.httpClient)
	assert.Positive(t, c.httpClient.Timeout)
}

func TestAutocomplete(t *testing.T) {
	const okBody = `{"suggestions":[
		{"placePrediction":{"placeId":"p1","text":{"text":"Rue Neuve 12, 4000 Liège"},"structuredFormat":{"mainText":{"text":"Rue Neuve 12"},"secondaryText":{"text":"4000 Liège"}}}},
		{"placePrediction":{"placeId":"p2","text":{"text":"Rue Vieille 3"}}}]}`

	t.Run("maps predictions and sends the key, session and radius restriction", func(t *testing.T) {
		var got *http.Request
		var sent map[string]any
		c := clientWith(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			b, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(b, &sent))
			return reply(http.StatusOK, okBody)(r)
		}), 25000)

		out, err := c.Autocomplete(t.Context(), "rue neuve", "sess-1", "nl")
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, "p1", out[0].PlaceID)
		assert.Equal(t, "Rue Neuve 12, 4000 Liège", out[0].Description)
		assert.Equal(t, "Rue Neuve 12", out[0].MainText)
		assert.Equal(t, "4000 Liège", out[0].SecondaryText)
		assert.Equal(t, "p2", out[1].PlaceID)
		assert.Empty(t, out[1].MainText)

		assert.Equal(t, "places.googleapis.com", got.URL.Host)
		assert.Equal(t, "/v1/places:autocomplete", got.URL.Path)
		assert.Equal(t, "secret-key", got.Header.Get("X-Goog-Api-Key"))
		assert.Equal(t, "rue neuve", sent["input"])
		assert.Equal(t, "sess-1", sent["sessionToken"])
		assert.Equal(t, "nl", sent["languageCode"])
		assert.Equal(t, []any{"BE"}, sent["includedRegionCodes"])
		circle := sent["locationRestriction"].(map[string]any)["circle"].(map[string]any)
		assert.EqualValues(t, 25000, circle["radius"])
		assert.EqualValues(t, 50.6, circle["center"].(map[string]any)["latitude"])
	})

	t.Run("a zero radius sends no location restriction", func(t *testing.T) {
		var sent map[string]any
		c := clientWith(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &sent)
			return reply(http.StatusOK, `{}`)(r)
		}), 0)
		out, err := c.Autocomplete(t.Context(), "x", "s", "fr")
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.NotContains(t, sent, "locationRestriction")
	})

	t.Run("failures", func(t *testing.T) {
		cases := map[string]struct {
			rt   http.RoundTripper
			want string
		}{
			"transport error":   {roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: refused") }), "http request"},
			"unreadable body":   {roundTripFunc(func(r *http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: failingBody{}, Request: r}, nil }), "read response"},
			"non-200":           {reply(http.StatusForbidden, `{"error":"key"}`), "google API returned 403"},
			"invalid JSON":      {reply(http.StatusOK, `not json`), "parse response"},
			"server error":      {reply(http.StatusInternalServerError, ``), "google API returned 500"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := clientWith(tc.rt, 0).Autocomplete(t.Context(), "x", "s", "fr")
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
			})
		}
	})
}

func TestPlaceDetails(t *testing.T) {
	const okBody = `{"id":"place-1","formattedAddress":"Rue Neuve 12, 4000 Liège, Belgique",
		"location":{"latitude":50.64,"longitude":5.57},
		"addressComponents":[
			{"longText":"12","types":["street_number"]},
			{"longText":"Rue Neuve","types":["route"]},
			{"longText":"Rue Ignorée","types":["route"]},
			{"longText":"4000","types":["postal_code"]},
			{"longText":"Liège","types":["locality","political"]},
			{"longText":"Autre","types":["postal_town"]},
			{"longText":"Belgique","types":["country"]}]}`

	t.Run("maps the structured address, keeps the first of each component and the raw payload", func(t *testing.T) {
		var got *http.Request
		c := clientWith(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			return reply(http.StatusOK, okBody)(r)
		}), 0)

		a, err := c.PlaceDetails(t.Context(), "place-1", "sess", "fr")
		require.NoError(t, err)
		assert.Equal(t, "place-1", a.PlaceID)
		assert.Equal(t, "Rue Neuve 12, 4000 Liège, Belgique", a.FormattedAddress)
		assert.InDelta(t, 50.64, a.Lat, 1e-9)
		assert.InDelta(t, 5.57, a.Lng, 1e-9)
		require.NotNil(t, a.StreetName)
		assert.Equal(t, "Rue Neuve", *a.StreetName, "the first route wins")
		assert.Equal(t, "12", *a.HouseNumber)
		assert.Equal(t, "4000", *a.Postcode)
		assert.Equal(t, "Liège", *a.MunicipalityName, "the first locality/postal_town wins")
		assert.Nil(t, a.BoxNumber)
		assert.Equal(t, "BE", a.CountryCode)
		assert.Zero(t, a.DistanceMeters, "the caller fills the route distance")
		assert.JSONEq(t, okBody, string(a.RawPlaceDetails))
		assert.False(t, a.CreatedAt.IsZero())

		assert.Equal(t, "secret-key", got.Header.Get("X-Goog-Api-Key"))
		assert.Equal(t, "id,formattedAddress,location,addressComponents", got.Header.Get("X-Goog-FieldMask"))
		assert.Equal(t, "fr", got.URL.Query().Get("languageCode"))
	})

	t.Run("a place without components has no structured fields", func(t *testing.T) {
		a, err := clientWith(reply(http.StatusOK, `{"id":"p","formattedAddress":"Somewhere"}`), 0).PlaceDetails(t.Context(), "p", "s", "fr")
		require.NoError(t, err)
		assert.Nil(t, a.StreetName)
		assert.Nil(t, a.HouseNumber)
		assert.Nil(t, a.Postcode)
		assert.Nil(t, a.MunicipalityName)
	})

	t.Run("a postal_town is used when there is no locality", func(t *testing.T) {
		a, err := clientWith(reply(http.StatusOK, `{"id":"p","addressComponents":[{"longText":"Seraing","types":["postal_town"]}]}`), 0).PlaceDetails(t.Context(), "p", "s", "fr")
		require.NoError(t, err)
		assert.Equal(t, "Seraing", *a.MunicipalityName)
	})

	t.Run("failures", func(t *testing.T) {
		cases := map[string]struct {
			rt   http.RoundTripper
			want string
		}{
			"transport error": {roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("refused") }), "http request"},
			"unreadable body": {roundTripFunc(func(r *http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: failingBody{}, Request: r}, nil }), "read response"},
			"not found":       {reply(http.StatusNotFound, `{}`), "google API returned 404"},
			"invalid JSON":    {reply(http.StatusOK, `{`), "parse response"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := clientWith(tc.rt, 0).PlaceDetails(t.Context(), "p", "s", "fr")
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
			})
		}
	})
}

func TestComputeRoute(t *testing.T) {
	t.Run("returns distance and duration, sending origin and destination", func(t *testing.T) {
		var got *http.Request
		var sent map[string]any
		c := clientWith(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &sent)
			return reply(http.StatusOK, `{"routes":[{"distanceMeters":4200,"duration":"540s"}]}`)(r)
		}), 0)

		dist, dur, err := c.ComputeRoute(t.Context(), 50.7, 5.6)
		require.NoError(t, err)
		assert.Equal(t, 4200, dist)
		assert.Equal(t, 540, dur)
		assert.Equal(t, "routes.googleapis.com", got.URL.Host)
		assert.Equal(t, "routes.distanceMeters,routes.duration", got.Header.Get("X-Goog-FieldMask"))
		assert.Equal(t, "DRIVE", sent["travelMode"])
		origin := sent["origin"].(map[string]any)["location"].(map[string]any)["latLng"].(map[string]any)
		dest := sent["destination"].(map[string]any)["location"].(map[string]any)["latLng"].(map[string]any)
		assert.EqualValues(t, 50.6, origin["latitude"])
		assert.EqualValues(t, 5.6, dest["longitude"])
	})

	t.Run("a missing or unreadable duration is zero seconds, not an error", func(t *testing.T) {
		for _, body := range []string{
			`{"routes":[{"distanceMeters":100}]}`,
			`{"routes":[{"distanceMeters":100,"duration":"soon"}]}`,
			`{"routes":[{"distanceMeters":100,"duration":"1.5s"}]}`,
		} {
			dist, dur, err := clientWith(reply(http.StatusOK, body), 0).ComputeRoute(t.Context(), 1, 1)
			require.NoError(t, err, body)
			assert.Equal(t, 100, dist)
			assert.Zero(t, dur, body)
		}
	})

	t.Run("failures", func(t *testing.T) {
		cases := map[string]struct {
			rt   http.RoundTripper
			want string
		}{
			"transport error": {roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("refused") }), "http request"},
			"unreadable body": {roundTripFunc(func(r *http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: failingBody{}, Request: r}, nil }), "read response"},
			"quota":           {reply(http.StatusTooManyRequests, `{}`), "google API returned 429"},
			"invalid JSON":    {reply(http.StatusOK, `[`), "parse response"},
			"no route":        {reply(http.StatusOK, `{"routes":[]}`), "no route found"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				dist, dur, err := clientWith(tc.rt, 0).ComputeRoute(t.Context(), 1, 1)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
				assert.Zero(t, dist)
				assert.Zero(t, dur)
			})
		}
	})
}

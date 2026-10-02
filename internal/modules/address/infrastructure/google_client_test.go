package infrastructure

import (
	"net/http"
	"strings"
	"testing"
)

type captureTransport struct{ got *http.Request }

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.got = req
	return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

// The place id comes from an unauthenticated client: it must stay ONE path segment of the Places
// URL, whatever it contains.
func TestPlaceDetailsEscapesThePlaceID(t *testing.T) {
	tr := &captureTransport{}
	c := NewGoogleClient("key", 0, 0, 0, &http.Client{Transport: tr})

	_, _ = c.PlaceDetails(t.Context(), "../../v1/places:searchText?x=1#frag", "tok&en", "fr")

	if tr.got == nil {
		t.Fatal("no request sent")
	}
	if tr.got.URL.Path != "/v1/places/../../v1/places:searchText?x=1#frag" {
		t.Errorf("decoded path = %q", tr.got.URL.Path)
	}
	raw := tr.got.URL.EscapedPath()
	if !strings.HasPrefix(raw, "/v1/places/") || strings.Count(raw, "/") != 3 {
		t.Errorf("escaped path %q has more than one segment after /v1/places/", raw)
	}
	if got := tr.got.URL.Query().Get("sessionToken"); got != "tok&en" {
		t.Errorf("sessionToken = %q", got)
	}
	if tr.got.URL.Query().Get("x") != "" {
		t.Error("the place id injected a query parameter")
	}
}

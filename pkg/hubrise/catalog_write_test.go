package hubrise_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/hubrise"
)

const rawData = `{"products":[{"ref":"A","name":"A","x_unknown":{"k":[1,2.50,null]},"skus":[{"ref":"A","price":"1.00 EUR"}]}],"x_top":true}`

func TestGetCatalogRawKeepsTheContentByteForByte(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET /v1/catalogs/c1", r.Method+" "+r.URL.Path)
		assert.Equal(t, secretToken, r.Header.Get("X-Access-Token"))
		respond(200, `{"id":"c1","location_id":"l1","account_id":"a1","name":"Menu","created_at":"2026-10-24T10:00:00Z","data":`+rawData+`}`)(w, r)
	})
	got, err := c.GetCatalogRaw(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, "c1", got.ID)
	assert.Equal(t, "l1", got.LocationID)
	assert.Equal(t, "Menu", got.Name)
	assert.JSONEq(t, rawData, string(got.Data))
	assert.Contains(t, string(got.Data), `2.50`, "numbers are not rewritten")
}

func TestReplaceCatalogSendsOnlyDataAndReturnsTheNewContent(t *testing.T) {
	var method, path, ctype, body string
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		respond(200, `{"id":"c1","name":"Menu","data":`+rawData+`}`)(w, r)
	})
	got, err := c.ReplaceCatalog(ctx, "c1", json.RawMessage(rawData))
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, method)
	assert.Equal(t, "/v1/catalogs/c1", path)
	assert.Equal(t, "application/json", ctype)
	var sent map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &sent))
	assert.Len(t, sent, 1, "no name is sent: the catalog is never renamed")
	assert.JSONEq(t, rawData, string(sent["data"]))
	assert.JSONEq(t, rawData, string(got.Data))
}

func TestReplaceCatalogRefusesEmptyData(t *testing.T) {
	c, srv := stub(t, respond(200, `{}`))
	_, err := c.ReplaceCatalog(ctx, "c1", nil)
	require.Error(t, err)
	assert.Equal(t, hubrise.KindProtocol, hubrise.KindOf(err))
	_ = srv
}

func TestCatalogWriteErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   hubrise.Kind
	}{
		{401, `{"error_type":"unauthorized"}`, hubrise.KindUnauthorized},
		{403, `{"error_type":"forbidden"}`, hubrise.KindForbidden},
		{404, `{"error_type":"not_found"}`, hubrise.KindNotFound},
		{429, `{"error_type":"too_many_requests"}`, hubrise.KindRateLimited},
		{500, `{"error_type":"internal_error"}`, hubrise.KindServer},
		{503, ``, hubrise.KindServer},
	} {
		c, _ := stub(t, respond(tc.status, tc.body))
		_, err := c.ReplaceCatalog(ctx, "c1", json.RawMessage(rawData))
		assert.Equal(t, tc.want, hubrise.KindOf(err), tc.status)
		_, err = c.GetCatalogRaw(ctx, "c1")
		assert.Equal(t, tc.want, hubrise.KindOf(err), tc.status)
	}
}

func TestReplaceCatalog422CarriesTheFieldErrorsAndNeverTheToken(t *testing.T) {
	c, _ := stub(t, respond(422, `{"error_type":"unprocessable_entity","message":"bad `+secretToken+`","errors":[{"field":"data.products[0].skus","message":"must have one"},{"field":"x `+secretToken+`","message":"m"}]}`))
	_, err := c.ReplaceCatalog(ctx, "c1", json.RawMessage(rawData))
	var he *hubrise.Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, hubrise.KindInvalid, he.Kind)
	require.Len(t, he.Fields, 2)
	assert.Equal(t, "data.products[0].skus", he.Fields[0].Field)
	assert.NotContains(t, err.Error(), secretToken)
	assert.NotContains(t, he.Fields[1].Field, secretToken)
}

func TestReplaceCatalogTransportErrorNeverHoldsTheToken(t *testing.T) {
	c, srv := stub(t, respond(200, `{}`))
	srv.Close()
	_, err := c.ReplaceCatalog(ctx, "c1", json.RawMessage(rawData))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretToken)
}

func TestReplaceCatalogIsNotInTheCatalogBucketButTheReadIs(t *testing.T) {
	l := hubrise.NewLimiter(hubrise.Limits{CatalogPerMinute: 1})
	c, _ := stub(t, respond(200, `{"id":"c1","data":{}}`), hubrise.WithLimiter(l))
	for range 5 {
		_, err := c.ReplaceCatalog(ctx, "c1", json.RawMessage(`{}`))
		require.NoError(t, err, "a PUT spends nothing of the 10 a minute list bucket")
	}
	_, err := c.GetCatalogRaw(ctx, "c1")
	require.NoError(t, err, "the first read takes the single slot")
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = c.GetCatalogRaw(short, "c1")
	require.Error(t, err, "the second read within the minute is held back by the bucket")
	_, err = c.ReplaceCatalog(ctx, "c1", json.RawMessage(`{}`))
	require.NoError(t, err, "while the PUT still goes through")
}

func TestListImagesFollowsThePagesAndFiltersByPrivateRef(t *testing.T) {
	var queries []string
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		assert.Equal(t, "/v1/catalogs/c1/images", r.URL.Path)
		if r.URL.Query().Get("cursor") == "" {
			w.Header().Set("X-Cursor-Next", "next1")
			respond(200, `[{"id":"i1","type":"image/jpeg","size":10,"md5":"aa","private_ref":"r1"}]`)(w, r)
			return
		}
		respond(200, `[{"id":"i2","type":"image/png","size":20,"md5":"bb","private_ref":"r1"}]`)(w, r)
	})
	got, err := c.ListImages(ctx, "c1", "r1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "i2", got[1].ID)
	require.Len(t, queries, 2)
	for _, q := range queries {
		assert.Contains(t, q, "private_ref=r1")
	}
	assert.Contains(t, queries[1], "cursor=next1")
}

func TestListImagesStopsAtThePageCap(t *testing.T) {
	calls := 0
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Cursor-Next", "again")
		respond(200, `[]`)(w, r)
	})
	_, err := c.ListImages(ctx, "c1", "")
	require.NoError(t, err)
	assert.Equal(t, 50, calls)
}

func TestListImagesError(t *testing.T) {
	c, _ := stub(t, respond(403, `{"error_type":"forbidden"}`))
	_, err := c.ListImages(ctx, "c1", "")
	assert.Equal(t, hubrise.KindForbidden, hubrise.KindOf(err))
}

func TestUploadImageSendsTheRawBytesWithTheImageType(t *testing.T) {
	var ctype, ref string
	var body []byte
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/catalogs/c1/images", r.URL.Path)
		ctype, ref = r.Header.Get("Content-Type"), r.URL.Query().Get("private_ref")
		body, _ = io.ReadAll(r.Body)
		respond(201, `{"id":"i9","type":"image/png","size":4,"md5":"cc","private_ref":"tsb-x"}`)(w, r)
	})
	img, err := c.UploadImage(ctx, "c1", "tsb-x", "image/png", []byte{1, 2, 3, 4})
	require.NoError(t, err)
	assert.Equal(t, "i9", img.ID)
	assert.Equal(t, "image/png", ctype)
	assert.Equal(t, "tsb-x", ref)
	assert.Equal(t, []byte{1, 2, 3, 4}, body)
}

func TestUploadImageWithoutPrivateRefAndEmptyData(t *testing.T) {
	c, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		assert.False(t, r.URL.Query().Has("private_ref"))
		respond(201, `{"id":"i1"}`)(w, r)
	})
	_, err := c.UploadImage(ctx, "c1", "", "image/jpeg", []byte{9})
	require.NoError(t, err)
	_, err = c.UploadImage(ctx, "c1", "x", "image/jpeg", nil)
	assert.Equal(t, hubrise.KindProtocol, hubrise.KindOf(err))
}

func TestUploadImageErrorsAreClassified(t *testing.T) {
	for status, want := range map[int]hubrise.Kind{401: hubrise.KindUnauthorized, 403: hubrise.KindForbidden, 404: hubrise.KindNotFound, 422: hubrise.KindInvalid, 429: hubrise.KindRateLimited, 500: hubrise.KindServer} {
		c, _ := stub(t, respond(status, `{"error_type":"x","message":"`+strings.Repeat("m", 3)+`"}`))
		_, err := c.UploadImage(ctx, "c1", "r", "image/png", []byte{1})
		assert.Equal(t, want, hubrise.KindOf(err), status)
		assert.NotContains(t, err.Error(), secretToken)
	}
}

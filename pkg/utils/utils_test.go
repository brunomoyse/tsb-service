package utils

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextHelpers(t *testing.T) {
	bg := context.Background()

	t.Run("defaults", func(t *testing.T) {
		assert.Equal(t, "fr", GetLang(bg), "French is the default language")
		assert.Empty(t, GetUserID(bg))
		assert.Empty(t, GetZitadelSub(bg))
		assert.Empty(t, GetClientIP(bg))
		assert.False(t, GetIsAdmin(bg))
		assert.False(t, GetIsPOS(bg))
		assert.False(t, GetIsStaff(bg))
		assert.True(t, GetTokenExpiry(bg).IsZero())
	})

	t.Run("values round-trip", func(t *testing.T) {
		exp := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
		ctx := SetLang(bg, "nl")
		ctx = SetUserID(ctx, "u-1")
		ctx = SetZitadelSub(ctx, "sub-1")
		ctx = SetClientIP(ctx, "203.0.113.1")
		ctx = SetTokenExpiry(ctx, exp)
		assert.Equal(t, "nl", GetLang(ctx))
		assert.Equal(t, "u-1", GetUserID(ctx))
		assert.Equal(t, "sub-1", GetZitadelSub(ctx))
		assert.Equal(t, "203.0.113.1", GetClientIP(ctx))
		assert.True(t, exp.Equal(GetTokenExpiry(ctx)))
	})

	t.Run("an empty language reads as the default", func(t *testing.T) {
		assert.Equal(t, "fr", GetLang(SetLang(bg, "")))
	})

	t.Run("staff is admin or POS, and only those", func(t *testing.T) {
		assert.True(t, GetIsStaff(SetIsAdmin(bg, true)))
		assert.True(t, GetIsStaff(SetIsPOS(bg, true)))
		assert.True(t, GetIsAdmin(SetIsAdmin(bg, true)))
		assert.False(t, GetIsAdmin(SetIsPOS(bg, true)), "a POS device is not an admin")
		assert.False(t, GetIsPOS(SetIsAdmin(bg, true)), "an admin is not a POS device")
		assert.False(t, GetIsStaff(SetIsPOS(SetIsAdmin(bg, false), false)))
	})

	t.Run("a value of the wrong type reads as the zero value", func(t *testing.T) {
		ctx := context.WithValue(bg, UserIDKey, 42)
		ctx = context.WithValue(ctx, IsAdminKey, "yes")
		assert.Empty(t, GetUserID(ctx))
		assert.False(t, GetIsAdmin(ctx))
	})
}

func TestParseCode(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name  string
		code  *string
		alpha string
		num   int
	}{
		{"nil", nil, "", 0},
		{"letter and number", s("A10"), "A", 10},
		{"several letters", s("AB7"), "AB", 7},
		{"only the first number counts", s("b2x9"), "b", 2},
		{"number only", s("12"), "", 12},
		{"letters only", s("ABC"), "ABC", 0},
		{"empty", s(""), "", 0},
		{"a letter after the number is not part of the prefix", s("7A"), "", 7},
		{"leading zeros", s("S007"), "S", 7},
		{"a number too large to parse falls back to zero", s("A99999999999999999999"), "A", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alpha, num := ParseCode(tc.code)
			assert.Equal(t, tc.alpha, alpha)
			assert.Equal(t, tc.num, num)
		})
	}
}

func TestFormatDecimal(t *testing.T) {
	const nbsp = " "
	cases := map[string]string{
		"0":           "0,00",
		"5":           "5,00",
		"5.5":         "5,50",
		"12.345":      "12,35",
		"12.344":      "12,34",
		"999.99":      "999,99",
		"1000":        "1" + nbsp + "000,00",
		"1234.56":     "1" + nbsp + "234,56",
		"1234567.891": "1" + nbsp + "234" + nbsp + "567,89",
		"-0.5":        "-0,50",
		"-1234.5":     "-1" + nbsp + "234,50",
		"100":         "100,00",
		"0.005":       "0,01",
		"-0.004":      "0,00", // a negative that rounds to zero does not print as "-0,00"
		"-0.005":      "-0,01",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, FormatDecimal(decimal.RequireFromString(in)))
		})
	}
}

func TestValidateSlug(t *testing.T) {
	for _, ok := range []string{"sushi", "sushi-saumon", "a1-b2-c3", "7", strings.Repeat("a", 128)} {
		assert.NoError(t, validateSlug(ok), ok)
	}
	bad := map[string]string{
		"":                       "slug is empty",
		strings.Repeat("a", 129): "slug too long",
		"Sushi":                  "invalid characters",
		"sushi saumon":           "invalid characters",
		"-sushi":                 "invalid characters",
		"sushi-":                 "invalid characters",
		"sushi--saumon":          "invalid characters",
		"../etc/passwd":          "invalid characters",
		"a/b":                    "invalid characters",
		"a?b=c":                  "invalid characters",
		"sushi\n":                "invalid characters",
		"sushi_saumon":           "invalid characters",
		"sushi.png":              "invalid characters",
		"é":                      "invalid characters",
	}
	for in, msg := range bad {
		assert.ErrorContains(t, validateSlug(in), msg, in)
	}
}

// fileService is a fake of the file service recording its requests.
type fileService struct {
	srv      *httptest.Server
	method   string
	path     string
	fields   map[string]string
	file     []byte
	filename string
	status   int
	body     string
	hits     int
}

func newFileService(t *testing.T) *fileService {
	t.Helper()
	f := &fileService{status: http.StatusOK, fields: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits++
		f.method, f.path = r.Method, r.URL.EscapedPath()
		if mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(mt, "multipart/") {
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(p)
				if p.FileName() != "" {
					f.file, f.filename = data, p.FileName()
				} else {
					f.fields[p.FormName()] = string(data)
				}
			}
		}
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.srv.Close)
	t.Setenv("FILE_SERVICE_URL", f.srv.URL)
	return f
}

func TestUploadProductImage(t *testing.T) {
	ctx := context.Background()

	t.Run("a plain upload posts the file and slug to /upload", func(t *testing.T) {
		f := newFileService(t)
		err := UploadProductImage(ctx, strings.NewReader("png-bytes"), "salmon.png", "sushi-saumon", false)
		require.NoError(t, err)
		assert.Equal(t, http.MethodPost, f.method)
		assert.Equal(t, "/upload", f.path)
		assert.Equal(t, []byte("png-bytes"), f.file)
		assert.Equal(t, "salmon.png", f.filename)
		assert.Equal(t, "sushi-saumon", f.fields["product_slug"])
	})

	t.Run("with background removal the processed endpoint is used and its status checked", func(t *testing.T) {
		f := newFileService(t)
		f.body = `{"status":"ok","trim_applied":true}`
		require.NoError(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", true))
		assert.Equal(t, "/images/upload/processed", f.path)

		f.body = `{"status":"error"}`
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", true), `file service returned status "error"`)

		f.body = `not json`
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", true), "decode upload response")
	})

	t.Run("a plain upload does not read the response body", func(t *testing.T) {
		f := newFileService(t)
		f.body = "anything"
		require.NoError(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", false))
	})

	t.Run("a non-200 answer is an error", func(t *testing.T) {
		f := newFileService(t)
		f.status = http.StatusInternalServerError
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", false), "status 500")
	})

	t.Run("configuration and input problems never reach the file service", func(t *testing.T) {
		f := newFileService(t)
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "../evil", false), "invalid image key")
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "", false), "invalid image key")
		t.Setenv("FILE_SERVICE_URL", "")
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", false), "FILE_SERVICE_URL env var not set")
		assert.Zero(t, f.hits)
	})

	t.Run("an unreadable source is reported", func(t *testing.T) {
		f := newFileService(t)
		err := UploadProductImage(ctx, failingReader{}, "a.png", "slug", false)
		require.ErrorContains(t, err, "copy file bytes")
		assert.Zero(t, f.hits)
	})

	t.Run("an unreachable file service or a cancelled context", func(t *testing.T) {
		f := newFileService(t)
		f.srv.Close()
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", false), "upload request failed")

		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		newFileService(t)
		require.ErrorContains(t, UploadProductImage(cancelled, strings.NewReader("x"), "a.png", "slug", false), "upload request failed")
	})

	t.Run("an invalid service URL is a request-construction error", func(t *testing.T) {
		t.Setenv("FILE_SERVICE_URL", "http://bad host")
		require.ErrorContains(t, UploadProductImage(ctx, strings.NewReader("x"), "a.png", "slug", false), "build upload request")
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestRenameProductImage(t *testing.T) {
	ctx := context.Background()

	f := newFileService(t)
	require.NoError(t, RenameProductImage(ctx, "old-slug", "new-slug"))
	assert.Equal(t, http.MethodPost, f.method)
	assert.Equal(t, "/rename", f.path)
	assert.Equal(t, map[string]string{"old_slug": "old-slug", "new_slug": "new-slug"}, f.fields)

	f.status = http.StatusNotFound
	require.ErrorContains(t, RenameProductImage(ctx, "old-slug", "new-slug"), "status 404")

	hits := f.hits
	require.ErrorContains(t, RenameProductImage(ctx, "Bad Slug", "new-slug"), "invalid old slug")
	require.ErrorContains(t, RenameProductImage(ctx, "old-slug", "new/slug"), "invalid new slug")
	assert.Equal(t, hits, f.hits)

	t.Setenv("FILE_SERVICE_URL", "")
	require.ErrorContains(t, RenameProductImage(ctx, "a", "b"), "FILE_SERVICE_URL env var not set")

	t.Setenv("FILE_SERVICE_URL", "http://bad host")
	require.ErrorContains(t, RenameProductImage(ctx, "a", "b"), "build rename request")

	f.srv.Close()
	t.Setenv("FILE_SERVICE_URL", f.srv.URL)
	require.ErrorContains(t, RenameProductImage(ctx, "a", "b"), "rename request failed")
}

func TestDeleteProductImage(t *testing.T) {
	ctx := context.Background()

	f := newFileService(t)
	require.NoError(t, DeleteProductImage(ctx, "sushi-saumon"))
	assert.Equal(t, http.MethodDelete, f.method)
	assert.Equal(t, "/delete/sushi-saumon", f.path)

	f.status = http.StatusNoContent
	require.NoError(t, DeleteProductImage(ctx, "sushi-saumon"), "204 is success too")

	f.status = http.StatusInternalServerError
	require.ErrorContains(t, DeleteProductImage(ctx, "sushi-saumon"), "status 500")
	f.status = http.StatusNotFound
	require.ErrorContains(t, DeleteProductImage(ctx, "sushi-saumon"), "status 404")

	hits := f.hits
	require.ErrorContains(t, DeleteProductImage(ctx, "../x"), "invalid slug")
	assert.Equal(t, hits, f.hits)

	t.Setenv("FILE_SERVICE_URL", "")
	require.ErrorContains(t, DeleteProductImage(ctx, "a"), "FILE_SERVICE_URL env var not set")

	t.Setenv("FILE_SERVICE_URL", "http://bad host")
	require.ErrorContains(t, DeleteProductImage(ctx, "a"), "build delete request")

	f.srv.Close()
	t.Setenv("FILE_SERVICE_URL", f.srv.URL)
	require.ErrorContains(t, DeleteProductImage(ctx, "a"), "delete request failed")
}

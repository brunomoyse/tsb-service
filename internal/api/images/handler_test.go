package images

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/pkg/utils"
)

// upstream is a fake file service recording what it was sent.
type upstream struct {
	srv      *httptest.Server
	hits     int
	path     string
	filename string
	content  []byte
	fieldSet bool
}

func newUpstream(t *testing.T, respond func(w http.ResponseWriter)) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits++
		u.path = r.URL.Path
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && strings.HasPrefix(mt, "multipart/") {
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				if p.FormName() == "image" {
					u.fieldSet = true
					u.filename = p.FileName()
					u.content, _ = io.ReadAll(p)
				}
			}
		}
		respond(w)
	}))
	t.Cleanup(u.srv.Close)
	t.Setenv("FILE_SERVICE_URL", u.srv.URL)
	return u
}

func multipartBody(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

func call(t *testing.T, admin bool, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/images/preview", func(c *gin.Context) {
		c.Request = c.Request.WithContext(utils.SetIsAdmin(c.Request.Context(), admin))
		PreviewHandler(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/images/preview", body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPreviewHandler_Success(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nprocessed-bytes")
	up := newUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("X-Original-Width", "800")
		w.Header().Set("X-Original-Height", "600")
		w.Header().Set("X-Post-Rembg-Width", "790")
		w.Header().Set("X-Post-Rembg-Height", "590")
		w.Header().Set("X-Post-Trim-Width", "400")
		w.Header().Set("X-Post-Trim-Height", "300")
		w.Header().Set("X-Trim-Applied", "true")
		w.Header().Set("X-Internal-Secret", "must-not-leak")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(png)
	})
	body, ct := multipartBody(t, "image", "salmon.jpg", []byte("jpeg-bytes"))

	rec := call(t, true, body, ct)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, png, rec.Body.Bytes())
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"), "the result is always served as png")
	for k, v := range map[string]string{
		"X-Original-Width": "800", "X-Original-Height": "600", "X-Post-Rembg-Width": "790",
		"X-Post-Rembg-Height": "590", "X-Post-Trim-Width": "400", "X-Post-Trim-Height": "300", "X-Trim-Applied": "true",
	} {
		assert.Equal(t, v, rec.Header().Get(k), k)
	}
	assert.Empty(t, rec.Header().Get("X-Internal-Secret"), "only the documented dimension headers are forwarded")

	assert.Equal(t, 1, up.hits)
	assert.Equal(t, "/images/preview/processed", up.path)
	assert.True(t, up.fieldSet)
	assert.Equal(t, "salmon.jpg", up.filename)
	assert.Equal(t, []byte("jpeg-bytes"), up.content, "the image is forwarded unchanged")
}

func TestPreviewHandler_OnlyForwardsHeadersTheServiceSent(t *testing.T) {
	newUpstream(t, func(w http.ResponseWriter) { w.Header().Set("X-Original-Width", "10"); _, _ = w.Write([]byte("x")) })
	body, ct := multipartBody(t, "image", "a.png", []byte("a"))
	rec := call(t, true, body, ct)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "10", rec.Header().Get("X-Original-Width"))
	_, present := rec.Header()["X-Trim-Applied"]
	assert.False(t, present, "absent headers are not invented")
}

func TestPreviewHandler_Refusals(t *testing.T) {
	t.Run("non-admins are refused before anything else", func(t *testing.T) {
		up := newUpstream(t, func(http.ResponseWriter) {})
		body, ct := multipartBody(t, "image", "a.png", []byte("a"))
		rec := call(t, false, body, ct)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.JSONEq(t, `{"error":"admin access required"}`, rec.Body.String())
		assert.Zero(t, up.hits)
	})

	t.Run("an unconfigured file service is 503", func(t *testing.T) {
		t.Setenv("FILE_SERVICE_URL", "")
		body, ct := multipartBody(t, "image", "a.png", []byte("a"))
		rec := call(t, true, body, ct)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.JSONEq(t, `{"error":"file service not configured"}`, rec.Body.String())
	})

	t.Run("a request without the image field is 400", func(t *testing.T) {
		up := newUpstream(t, func(http.ResponseWriter) {})
		for name, mk := range map[string]func() (io.Reader, string){
			"wrong field name": func() (io.Reader, string) { b, ct := multipartBody(t, "file", "a.png", []byte("a")); return b, ct },
			"not multipart":    func() (io.Reader, string) { return strings.NewReader(`{"image":"x"}`), "application/json" },
			"no content type":  func() (io.Reader, string) { return strings.NewReader("x"), "" },
		} {
			b, ct := mk()
			rec := call(t, true, b, ct)
			assert.Equal(t, http.StatusBadRequest, rec.Code, name)
			assert.JSONEq(t, `{"error":"image field required"}`, rec.Body.String(), name)
		}
		assert.Zero(t, up.hits)
	})

	t.Run("an upload over the size limit is refused", func(t *testing.T) {
		up := newUpstream(t, func(http.ResponseWriter) {})
		body, ct := multipartBody(t, "image", "huge.png", bytes.Repeat([]byte("x"), maxPreviewSize+1024))
		rec := call(t, true, body, ct)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Zero(t, up.hits, "an oversized upload never reaches the file service")
	})
}

func TestPreviewHandler_UpstreamFailures(t *testing.T) {
	post := func(t *testing.T) *httptest.ResponseRecorder {
		body, ct := multipartBody(t, "image", "a.png", []byte("a"))
		return call(t, true, body, ct)
	}

	t.Run("an unreachable file service is 502", func(t *testing.T) {
		up := newUpstream(t, func(http.ResponseWriter) {})
		up.srv.Close()
		rec := post(t)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.JSONEq(t, `{"error":"file service unreachable"}`, rec.Body.String())
	})

	t.Run("an invalid file service URL is a 500 before any request", func(t *testing.T) {
		t.Setenv("FILE_SERVICE_URL", "http://bad host")
		rec := post(t)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.JSONEq(t, `{"error":"failed to build upstream request"}`, rec.Body.String())
	})

	t.Run("503 means background removal is not available on this instance", func(t *testing.T) {
		newUpstream(t, func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("rembg missing"))
		})
		rec := post(t)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.JSONEq(t, `{"error":"background removal not available on this instance"}`, rec.Body.String())
	})

	t.Run("any other status is passed on without leaking the body", func(t *testing.T) {
		for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
			newUpstream(t, func(w http.ResponseWriter) { w.WriteHeader(status); _, _ = w.Write([]byte("traceback: secret path")) })
			rec := post(t)
			assert.Equal(t, status, rec.Code)
			assert.NotContains(t, rec.Body.String(), "secret path")
			assert.Contains(t, rec.Body.String(), "file service returned")
		}
	})
}

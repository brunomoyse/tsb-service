package images

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"tsb-service/pkg/utils"
)

const maxPreviewSize = 5 << 20 // 5 MB

// PreviewHandler proxies an uploaded image to the file service's
// background-removal preview endpoint (/images/preview/processed) and streams
// the processed PNG back to the caller without uploading anything to S3.
//
// Dimension metadata returned by the file service is forwarded as X-* headers
// so the dashboard can display before/after information without a second round trip.
func PreviewHandler(c *gin.Context) {
	if !utils.GetIsAdmin(c.Request.Context()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin access required"})
		return
	}

	fileSvc := os.Getenv("FILE_SERVICE_URL")
	if fileSvc == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "file service not configured"})
		return
	}

	// Apply a per-endpoint body limit; the global 1 MB middleware is skipped for
	// this path (see main.go) to allow image payloads up to 5 MB.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPreviewSize)

	file, header, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "image field required"})
		return
	}
	defer func() { _ = file.Close() }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("image", header.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build upstream request"})
		return
	}
	if _, err = io.Copy(part, file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read image"})
		return
	}
	if err = writer.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build upstream request"})
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		fileSvc+"/images/preview/processed", &body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build upstream request"})
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "file service unreachable"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		for _, h := range []string{
			"X-Original-Width", "X-Original-Height",
			"X-Post-Rembg-Width", "X-Post-Rembg-Height",
			"X-Post-Trim-Width", "X-Post-Trim-Height",
			"X-Trim-Applied",
		} {
			if v := resp.Header.Get(h); v != "" {
				c.Header(h, v)
			}
		}
		c.DataFromReader(http.StatusOK, resp.ContentLength, "image/png", resp.Body, nil)
	case http.StatusServiceUnavailable:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "background removal not available on this instance"})
	default:
		c.JSON(resp.StatusCode, gin.H{"error": fmt.Sprintf("file service returned %d", resp.StatusCode)})
	}
}

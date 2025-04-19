package utils

import (
	"bytes"
	"context"
	"fmt"
	"github.com/shopspring/decimal"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type contextKey string

const LangKey contextKey = "lang"
const UserIDKey contextKey = "userID"
const IsAdminKey contextKey = "isAdmin"

// SetLang stores the language in the context.
func SetLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, LangKey, lang)
}

// GetLang retrieves the language from the context.
func GetLang(ctx context.Context) string {
	lang, _ := ctx.Value(LangKey).(string)
	if lang == "" {
		return "fr"
	}
	return lang
}

func SetUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, UserIDKey, userID)
}

func GetUserID(ctx context.Context) string {
	userID, _ := ctx.Value(UserIDKey).(string)
	if userID == "" {
		return ""
	}
	return userID
}

func SetIsAdmin(ctx context.Context, isAdmin bool) context.Context {
	return context.WithValue(ctx, IsAdminKey, isAdmin)
}

func GetIsAdmin(ctx context.Context) bool {
	isAdmin, _ := ctx.Value(IsAdminKey).(bool)
	return isAdmin
}

var (
	alphaRegexp = regexp.MustCompile(`^[A-Za-z]+`)
	numRegexp   = regexp.MustCompile(`\d+`)
)

// ParseCode takes a pointer to a code (e.g., "A10")
// and returns the alphabetical prefix (e.g., "A") and numeric part (10).
func ParseCode(code *string) (string, int) {
	if code == nil {
		// No code? Return empty alpha and 0 for the numeric part
		return "", 0
	}

	alpha := alphaRegexp.FindString(*code)
	numStr := numRegexp.FindString(*code)
	num := 0
	if numStr != "" {
		if n, err := strconv.Atoi(numStr); err == nil {
			num = n
		}
	}
	return alpha, num
}

func FormatDecimal(d decimal.Decimal) string {
	return strings.Replace(d.StringFixed(2), ".", ",", 1)
}

func UploadProductImage(ctx context.Context, src io.Reader, filename string, slug *string) error {
	fileSvc := os.Getenv("FILE_SERVICE_URL")
	if fileSvc == "" {
		return fmt.Errorf("FILE_SERVICE_URL env var not set")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Create the file part
	part, err := writer.CreateFormFile("image", filename)
	if err != nil {
		return fmt.Errorf("create multipart part: %w", err)
	}

	// Copy the file content
	if _, err := io.Copy(part, src); err != nil {
		return fmt.Errorf("copy file bytes: %w", err)
	}

	// Optional slug field
	if slug != nil {
		if err := writer.WriteField("product_slug", *slug); err != nil {
			return fmt.Errorf("write slug field: %w", err)
		}
	}

	// Close the writer to finalise the multipart body
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	// Build the request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fileSvc+"/upload", &body)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Fire the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("file‑service responded %s: %s", resp.Status, string(b))
	} else {
		fmt.Printf("File uploaded successfully.\n")
	}

	return nil
}

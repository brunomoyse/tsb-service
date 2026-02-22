package middleware

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"tsb-service/pkg/utils"

	"github.com/gin-gonic/gin"
)

// LanguageExtractor extracts and normalizes the Accept-Language header
func LanguageExtractor() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract the "Accept-Language" header
		acceptLanguage := c.GetHeader("Accept-Language")

		// Supported languages: add "zh" for Chinese
		supportedLanguages := []string{"fr", "en", "zh"}

		// Find the best match
		bestMatch := findBestLanguageMatch(acceptLanguage, supportedLanguages)

		// Ensure only "fr", "zh" or "en" is returned
		// Default to English if best match is neither French nor Chinese
		if bestMatch != "fr" && bestMatch != "zh" {
			bestMatch = "en"
		}

		// Use the shared SetLang function.
		ctx := utils.SetLang(c.Request.Context(), bestMatch)
		c.Request = c.Request.WithContext(ctx)

		// Continue to the next handler
		c.Next()
	}
}

// findBestLanguageMatch extracts the base language (e.g., "en-GB" → "en") and finds the best match
func findBestLanguageMatch(headerValue string, supportedLanguages []string) string {
	languagesWithQuality := parseAcceptLanguage(headerValue)

	// Normalize and filter languages
	var commonLanguages []languageQuality
	for _, langQuality := range languagesWithQuality {
		baseLang := strings.Split(langQuality.Language, "-")[0] // Normalize (e.g., en-GB → en)
		for _, supportedLang := range supportedLanguages {
			if baseLang == supportedLang {
				commonLanguages = append(commonLanguages, languageQuality{
					Language: supportedLang, // Always store one of the supported languages
					Quality:  langQuality.Quality,
				})
				break
			}
		}
	}

	// Sort by quality factor in descending order
	slices.SortFunc(commonLanguages, func(a, b languageQuality) int {
		return cmp.Compare(b.Quality, a.Quality)
	})

	// Instead of always forcing French when present,
	// simply return the best match from our supported list if available.
	if len(commonLanguages) > 0 {
		return commonLanguages[0].Language
	}

	return "" // If nothing matches, the default will be set in the middleware
}

// languageQuality struct holds language and its quality factor
type languageQuality struct {
	Language string
	Quality  float64
}

// parseAcceptLanguage parses the Accept-Language header and returns a list of languages with quality factors
func parseAcceptLanguage(headerValue string) []languageQuality {
	var languagesWithQuality []languageQuality
	for lang := range strings.SplitSeq(headerValue, ",") {
		lang = strings.TrimSpace(lang)
		parts := strings.Split(lang, ";")

		language := parts[0]
		quality := 1.0 // Default quality factor

		if len(parts) > 1 && strings.HasPrefix(parts[1], "q=") {
			qualityStr := strings.TrimPrefix(parts[1], "q=")
			parsedQuality, err := strconv.ParseFloat(qualityStr, 64)
			if err == nil {
				quality = parsedQuality
			}
		}

		languagesWithQuality = append(languagesWithQuality, languageQuality{
			Language: language,
			Quality:  quality,
		})
	}

	return languagesWithQuality
}

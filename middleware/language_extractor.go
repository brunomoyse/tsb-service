package middleware

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// LanguageExtractor extracts and normalizes the Accept-Language header
func LanguageExtractor() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract the "Accept-Language" header
		acceptLanguage := c.GetHeader("Accept-Language")
		fmt.Println("Accept-Language:", acceptLanguage)

		// Supported languages
		supportedLanguages := []string{"fr", "en"}

		// Find the best match
		bestMatch := findBestLanguageMatch(acceptLanguage, supportedLanguages)

		// Ensure only "fr" or "en" is returned
		if bestMatch != "fr" {
			bestMatch = "en" // Default to English if it's not French
		}

		fmt.Println("Best match:", bestMatch)
		// Store the language in the context
		c.Set("lang", bestMatch)

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
					Language: supportedLang, // Always store "fr" or "en"
					Quality:  langQuality.Quality,
				})
				break
			}
		}
	}

	// Sort by quality factor in descending order
	sort.Slice(commonLanguages, func(i, j int) bool {
		return commonLanguages[i].Quality > commonLanguages[j].Quality
	})

	// If French exists, return "fr"; otherwise, return "en" (default)
	for _, lang := range commonLanguages {
		if lang.Language == "fr" {
			return "fr"
		}
	}

	return "en" // Default to English if no French match
}

// languageQuality struct holds language and its quality factor
type languageQuality struct {
	Language string
	Quality  float64
}

// parseAcceptLanguage parses the Accept-Language header and returns a list of languages with quality factors
func parseAcceptLanguage(headerValue string) []languageQuality {
	languages := strings.Split(headerValue, ",")

	var languagesWithQuality []languageQuality
	for _, lang := range languages {
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

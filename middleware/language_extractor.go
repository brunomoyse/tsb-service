package middleware

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// LanguageExtractor extracts the Accept-Language header and stores the best match in the context
func LanguageExtractor() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract the "Accept-Language" header
		acceptLanguage := c.GetHeader("Accept-Language")
		if acceptLanguage == "" {
			// Default language if no "Accept-Language" header is provided
			acceptLanguage = "fr"
		}

		// Supported languages
		supportedLanguages := []string{"fr", "en"} // French, English

		// Find the best match based on quality factors
		bestMatch := findBestLanguageMatch(acceptLanguage, supportedLanguages)

		// Store the language in the context
		c.Set("lang", bestMatch)

		// Continue to the next handler
		c.Next()
	}
}

// findBestLanguageMatch processes the Accept-Language header and returns the best language match
func findBestLanguageMatch(headerValue string, supportedLanguages []string) string {
	languagesWithQuality := parseAcceptLanguage(headerValue)

	// Filter the languages by supported languages
	var commonLanguages []languageQuality
	for _, langQuality := range languagesWithQuality {
		for _, supportedLang := range supportedLanguages {
			if strings.HasPrefix(langQuality.Language, supportedLang) {
				commonLanguages = append(commonLanguages, langQuality)
				break
			}
		}
	}

	// Sort by quality factor in descending order
	sort.Slice(commonLanguages, func(i, j int) bool {
		return commonLanguages[i].Quality > commonLanguages[j].Quality
	})

	// Return the best match or default to "fr" if none found
	if len(commonLanguages) > 0 {
		return commonLanguages[0].Language
	}
	return "fr"
}

// languageQuality struct to hold language and its quality factor
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

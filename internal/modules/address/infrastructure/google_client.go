package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/address/domain"
)

type GoogleClient struct {
	apiKey      string
	originLat   float64
	originLng   float64
	httpClient  *http.Client
}

func NewGoogleClient(apiKey string, originLat, originLng float64, httpClient *http.Client) domain.GoogleClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &GoogleClient{
		apiKey:     apiKey,
		originLat:  originLat,
		originLng:  originLng,
		httpClient: httpClient,
	}
}

func (c *GoogleClient) Autocomplete(ctx context.Context, input, sessionToken, language string) ([]domain.Suggestion, error) {
	reqBody := map[string]interface{}{
		"input":                  input,
		"sessionToken":           sessionToken,
		"languageCode":           language,
		"includedRegionCodes":    []string{"BE"},
		"includedPrimaryTypes":   []string{"street_address", "premise", "subpremise", "route"},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://places.googleapis.com/v1/places:autocomplete",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Goog-Api-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		zap.L().Error("Google Places API error", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("google API returned %d", resp.StatusCode)
	}

	var apiResp struct {
		Suggestions []struct {
			PlacePrediction struct {
				PlaceID        string `json:"placeId"`
				Text           struct {
					Text string `json:"text"`
				} `json:"text"`
				StructuredFormat struct {
					MainText struct {
						Text string `json:"text"`
					} `json:"mainText"`
					SecondaryText struct {
						Text string `json:"text"`
					} `json:"secondaryText"`
				} `json:"structuredFormat"`
			} `json:"placePrediction"`
		} `json:"suggestions"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	suggestions := make([]domain.Suggestion, len(apiResp.Suggestions))
	for i, s := range apiResp.Suggestions {
		suggestions[i] = domain.Suggestion{
			PlaceID:       s.PlacePrediction.PlaceID,
			Description:   s.PlacePrediction.Text.Text,
			MainText:      s.PlacePrediction.StructuredFormat.MainText.Text,
			SecondaryText: s.PlacePrediction.StructuredFormat.SecondaryText.Text,
		}
	}

	return suggestions, nil
}

func (c *GoogleClient) PlaceDetails(ctx context.Context, placeID, sessionToken, language string) (*domain.AddressCache, error) {
	url := fmt.Sprintf("https://places.googleapis.com/v1/places/%s?languageCode=%s&sessionToken=%s",
		placeID, language, sessionToken)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Goog-Api-Key", c.apiKey)
	req.Header.Set("X-Goog-FieldMask", "id,formattedAddress,location,addressComponents")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		zap.L().Error("Google Places API error", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("google API returned %d", resp.StatusCode)
	}

	var apiResp struct {
		ID               string `json:"id"`
		FormattedAddress string `json:"formattedAddress"`
		Location         struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
		AddressComponents []struct {
			LongText string   `json:"longText"`
			ShortText string  `json:"shortText"`
			Types    []string `json:"types"`
		} `json:"addressComponents"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Walk address components to fill in structured fields
	var streetName, houseNumber, postcode, municipalityName *string
	for _, comp := range apiResp.AddressComponents {
		// Look for route (street name)
		if containsType(comp.Types, "route") && streetName == nil {
			streetName = &comp.LongText
		}
		// Look for street_number (house number)
		if containsType(comp.Types, "street_number") && houseNumber == nil {
			houseNumber = &comp.LongText
		}
		// Look for postal_code
		if containsType(comp.Types, "postal_code") && postcode == nil {
			postcode = &comp.LongText
		}
		// Look for locality (city) or postal_town
		if (containsType(comp.Types, "locality") || containsType(comp.Types, "postal_town")) && municipalityName == nil {
			municipalityName = &comp.LongText
		}
	}

	now := time.Now()
	cache := &domain.AddressCache{
		PlaceID:          apiResp.ID,
		FormattedAddress: apiResp.FormattedAddress,
		Lat:              apiResp.Location.Latitude,
		Lng:              apiResp.Location.Longitude,
		StreetName:       streetName,
		HouseNumber:      houseNumber,
		BoxNumber:        nil, // Google doesn't provide box numbers
		Postcode:         postcode,
		MunicipalityName: municipalityName,
		CountryCode:      "BE",
		DistanceMeters:   0, // Will be set by caller
		DurationSeconds:  0, // Will be set by caller
		RawPlaceDetails:  body,
		CreatedAt:        now,
		RefreshedAt:      now,
	}

	return cache, nil
}

func (c *GoogleClient) ComputeRoute(ctx context.Context, destLat, destLng float64) (distanceMeters int, durationSeconds int, err error) {
	reqBody := map[string]interface{}{
		"origin": map[string]interface{}{
			"location": map[string]interface{}{
				"latLng": map[string]float64{
					"latitude":  c.originLat,
					"longitude": c.originLng,
				},
			},
		},
		"destination": map[string]interface{}{
			"location": map[string]interface{}{
				"latLng": map[string]float64{
					"latitude":  destLat,
					"longitude": destLng,
				},
			},
		},
		"travelMode":         "DRIVE",
		"routingPreference":  "TRAFFIC_AWARE",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return 0, 0, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://routes.googleapis.com/directions/v2:computeRoutes",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, 0, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("X-Goog-Api-Key", c.apiKey)
	req.Header.Set("X-Goog-FieldMask", "routes.distanceMeters,routes.duration")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("http request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		zap.L().Error("Google Routes API error", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return 0, 0, fmt.Errorf("google API returned %d", resp.StatusCode)
	}

	var apiResp struct {
		Routes []struct {
			DistanceMeters int    `json:"distanceMeters"`
			Duration       string `json:"duration"`
		} `json:"routes"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return 0, 0, fmt.Errorf("parse response: %w", err)
	}

	if len(apiResp.Routes) == 0 {
		return 0, 0, fmt.Errorf("no route found")
	}

	route := apiResp.Routes[0]
	durationSeconds = 0
	if route.Duration != "" {
		// Duration is in format "540s" — trim trailing 's'
		durationStr := strings.TrimSuffix(route.Duration, "s")
		if val, err := strconv.Atoi(durationStr); err == nil {
			durationSeconds = val
		}
	}

	return route.DistanceMeters, durationSeconds, nil
}

func containsType(types []string, target string) bool {
	for _, t := range types {
		if t == target {
			return true
		}
	}
	return false
}

package domain

import "time"

type AddressCache struct {
	PlaceID          string    `db:"place_id"`
	FormattedAddress string    `db:"formatted_address"`
	Lat              float64   `db:"lat"`
	Lng              float64   `db:"lng"`
	StreetName       *string   `db:"street_name"`
	HouseNumber      *string   `db:"house_number"`
	BoxNumber        *string   `db:"box_number"`
	Postcode         *string   `db:"postcode"`
	MunicipalityName *string   `db:"municipality_name"`
	CountryCode      string    `db:"country_code"`
	DistanceMeters   int       `db:"distance_meters"`
	DurationSeconds  int       `db:"duration_seconds"`
	RawPlaceDetails  []byte    `db:"raw_place_details"`
	CreatedAt        time.Time `db:"created_at"`
	RefreshedAt      time.Time `db:"refreshed_at"`
}

// Address is the unified domain type returned to resolvers.
// For new Google-backed addresses, ID is the place_id.
// For legacy orders with denormalized-only data, ID is empty.
type Address struct {
	ID               string
	PlaceID          string
	StreetName       string
	HouseNumber      string
	BoxNumber        *string
	Postcode         string
	MunicipalityName string
	Distance         float64 // meters
	Lat              *float64
	Lng              *float64
	Duration         *int // seconds
}

type Suggestion struct {
	PlaceID       string
	Description   string
	MainText      string
	SecondaryText string
}

package infrastructure

import (
	"context"
	"database/sql"
	"errors"

	"tsb-service/internal/modules/address/domain"
	"tsb-service/pkg/db"
)

type AddressCacheRepository struct {
	pool *db.DBPool
}

func NewAddressCacheRepository(pool *db.DBPool) domain.AddressCacheRepository {
	return &AddressCacheRepository{pool: pool}
}

func (r *AddressCacheRepository) GetByPlaceID(ctx context.Context, placeID string) (*domain.AddressCache, error) {
	var cache domain.AddressCache
	err := r.pool.ForContext(ctx).GetContext(ctx, &cache, `
		SELECT place_id, formatted_address, lat, lng, street_name, house_number, box_number,
		       postcode, municipality_name, country_code, distance_meters, duration_seconds,
		       raw_place_details, created_at, refreshed_at
		FROM address_cache
		WHERE place_id = $1
	`, placeID)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cache, nil
}

func (r *AddressCacheRepository) Upsert(ctx context.Context, entry *domain.AddressCache) error {
	_, err := r.pool.ForContext(ctx).ExecContext(ctx, `
		INSERT INTO address_cache (
			place_id, formatted_address, lat, lng, street_name, house_number, box_number,
			postcode, municipality_name, country_code, distance_meters, duration_seconds,
			raw_place_details, created_at, refreshed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (place_id) DO UPDATE SET
			formatted_address = EXCLUDED.formatted_address,
			lat = EXCLUDED.lat,
			lng = EXCLUDED.lng,
			street_name = EXCLUDED.street_name,
			house_number = EXCLUDED.house_number,
			box_number = EXCLUDED.box_number,
			postcode = EXCLUDED.postcode,
			municipality_name = EXCLUDED.municipality_name,
			distance_meters = EXCLUDED.distance_meters,
			duration_seconds = EXCLUDED.duration_seconds,
			raw_place_details = EXCLUDED.raw_place_details,
			refreshed_at = EXCLUDED.refreshed_at
	`,
		entry.PlaceID, entry.FormattedAddress, entry.Lat, entry.Lng, entry.StreetName,
		entry.HouseNumber, entry.BoxNumber, entry.Postcode, entry.MunicipalityName,
		entry.CountryCode, entry.DistanceMeters, entry.DurationSeconds,
		entry.RawPlaceDetails, entry.CreatedAt, entry.RefreshedAt,
	)
	return err
}

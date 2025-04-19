package infrastructure

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"tsb-service/internal/modules/address/domain"
)

type AddressRepository struct {
	db *sqlx.DB
}

func NewAddressRepository(db *sqlx.DB) domain.AddressRepository {
	return &AddressRepository{db: db}
}

func (r *AddressRepository) SearchStreetNames(ctx context.Context, query string) ([]*domain.Street, error) {
	sqlQuery := `
		SELECT street_id, streetname_fr, municipality_name_fr, postcode
		FROM streets
		WHERE streetname_fr_unaccent % lower(unaccent($1))
		ORDER BY similarity(streetname_fr_unaccent, lower(unaccent($1))) DESC
		LIMIT 5;
	`

	var streetRows []domain.Street
	if err := r.db.SelectContext(ctx, &streetRows, sqlQuery, query); err != nil {
		return nil, err
	}

	// Return early if no results
	if len(streetRows) == 0 {
		return []*domain.Street{}, nil
	}

	// Convert to []*domain.Street
	streets := make([]*domain.Street, len(streetRows))
	for i := range streetRows {
		streets[i] = &streetRows[i]
	}

	return streets, nil
}

func (r *AddressRepository) GetDistinctHouseNumbers(ctx context.Context, streetID string) ([]string, error) {
	sqlQuery := `
		SELECT house_number
		FROM (
			SELECT DISTINCT house_number,
				   (regexp_replace(house_number, '[^0-9]', '', 'g'))::int AS house_number_num
			FROM addresses
			WHERE street_id = $1
		) AS sub
		ORDER BY house_number_num, house_number;
	`
	var houseNumbers []string
	if err := r.db.SelectContext(ctx, &houseNumbers, sqlQuery, streetID); err != nil {
		return nil, err
	}
	return houseNumbers, nil
}

func (r *AddressRepository) GetBoxNumbers(ctx context.Context, streetID, houseNumber string) ([]*string, error) {
	sqlQuery := `
		SELECT box_number
		FROM addresses
		WHERE street_id = $1 AND house_number = $2
		ORDER BY box_number ASC NULLS FIRST;
	`
	// Use sql.NullString to safely scan nullable columns.
	var nullStrings []sql.NullString
	if err := r.db.SelectContext(ctx, &nullStrings, sqlQuery, streetID, houseNumber); err != nil {
		return nil, err
	}

	// Convert sql.NullString to []*string.
	var boxNumbers []*string
	for _, ns := range nullStrings {
		if ns.Valid {
			// Create a new variable to take its address.
			s := ns.String
			boxNumbers = append(boxNumbers, &s)
		} else {
			// Append nil if the column is NULL.
			boxNumbers = append(boxNumbers, nil)
		}
	}
	return boxNumbers, nil
}

// GetFinalAddress retrieves the final address record based on streetID, houseNumber, and boxNumber.
// If boxNumber is an empty string, it is ignored.
func (r *AddressRepository) GetFinalAddress(ctx context.Context, streetID string, houseNumber string, boxNumber *string) (*domain.Address, error) {
	sqlQuery := `
		SELECT a.address_id, a.streetname_fr, a.house_number, a.box_number, a.municipality_name_fr, a.postcode, 
		       COALESCE(ad.distance, 10000) AS distance
		FROM addresses a
		LEFT JOIN address_distance ad ON a.address_id = ad.address_id
		WHERE a.street_id = $1 
		  AND a.house_number = $2 
		  AND ( ($3::text IS NULL AND a.box_number IS NULL) OR (a.box_number = $3::text) )
		LIMIT 1;
	`
	var addr domain.Address
	if err := r.db.GetContext(ctx, &addr, sqlQuery, streetID, houseNumber, boxNumber); err != nil {
		return nil, fmt.Errorf("failed to get final address: %w", err)
	}
	return &addr, nil
}

func (r *AddressRepository) GetAddressByID(ctx context.Context, ID string) (*domain.Address, error) {
	sqlQuery := `
		SELECT a.address_id, a.streetname_fr, a.house_number, a.box_number, a.municipality_name_fr, a.postcode, COALESCE(ad.distance, 10000) AS distance
		FROM addresses a
		LEFT JOIN address_distance ad ON a.address_id = ad.address_id
		WHERE a.address_id = $1;
	`
	var addr domain.Address
	if err := r.db.GetContext(ctx, &addr, sqlQuery, ID); err != nil {
		return nil, fmt.Errorf("failed to get address by ID: %w", err)
	}
	return &addr, nil
}

func (r *AddressRepository) BatchGetAddressesByOrderIDs(ctx context.Context, orderIDs []string) (map[string][]*domain.Address, error) {
	if len(orderIDs) == 0 {
		return map[string][]*domain.Address{}, nil
	}

	// 1) include o.id AS order_id so we know which order each address row belongs to
	sqlQuery := `
    SELECT
      o.id                    AS order_id,
      a.address_id            AS address_id,
      a.streetname_fr         AS streetname_fr,
      a.house_number          AS house_number,
      a.box_number            AS box_number,
      a.municipality_name_fr  AS municipality_name_fr,
      a.postcode              AS postcode,
      COALESCE(ad.distance, 10000) AS distance
    FROM addresses AS a
    JOIN orders    AS o   ON o.address_id = a.address_id
    LEFT JOIN address_distance AS ad ON ad.address_id = a.address_id
    WHERE o.id = ANY($1);
    `

	// 2) define a temporary row type to scan into
	type addressRow struct {
		OrderID        string `db:"order_id"`
		domain.Address        // embeds all the address fields
	}

	var rows []addressRow
	// 3) use pq.Array to pass your []string as a PostgresSQL text[]
	if err := r.db.SelectContext(ctx, &rows, sqlQuery, pq.Array(orderIDs)); err != nil {
		return nil, fmt.Errorf("failed to get addresses by order IDs: %w", err)
	}

	// 4) group by OrderID
	addressMap := make(map[string][]*domain.Address, len(rows))
	for _, row := range rows {
		// take the embedded Address by pointer
		addr := row.Address
		addressMap[row.OrderID] = append(addressMap[row.OrderID], &addr)
	}

	return addressMap, nil
}

func (r *AddressRepository) BatchGetAddressesByUserIDs(ctx context.Context, userIDs []string) (map[string][]*domain.Address, error) {
	if len(userIDs) == 0 {
		return map[string][]*domain.Address{}, nil
	}

	sqlQuery := `
	SELECT
	  u.id                    AS user_id,
	  a.address_id            AS address_id,
	  a.streetname_fr         AS streetname_fr,
	  a.house_number          AS house_number,
	  a.box_number            AS box_number,
	  a.municipality_name_fr  AS municipality_name_fr,
	  a.postcode              AS postcode,
	  COALESCE(ad.distance, 10000) AS distance
	FROM addresses AS a
	JOIN users     AS u   ON u.address_id = a.address_id
	LEFT JOIN address_distance AS ad ON ad.address_id = a.address_id
	WHERE u.id = ANY($1);
	`

	type addressRow struct {
		UserID         string `db:"user_id"`
		domain.Address        // embeds all the address fields
	}

	var rows []addressRow
	if err := r.db.SelectContext(ctx, &rows, sqlQuery, pq.Array(userIDs)); err != nil {
		return nil, fmt.Errorf("failed to get addresses by user IDs: %w", err)
	}

	addressMap := make(map[string][]*domain.Address, len(rows))
	for _, row := range rows {
		addr := row.Address
		addressMap[row.UserID] = append(addressMap[row.UserID], &addr)
	}

	return addressMap, nil
}

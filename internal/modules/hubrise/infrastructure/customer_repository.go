package infrastructure

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"tsb-service/internal/modules/hubrise/domain"
)

const customerLinkColumns = `user_id, hubrise_customer_id, content_hash, synced_at, anonymised_at, created_at, updated_at`

type customerLinkRepository struct{ store }

// NewCustomerLinkRepository returns the store of the links to the HubRise customer list.
func NewCustomerLinkRepository(db *sqlx.DB) domain.CustomerLinkRepository {
	return &customerLinkRepository{newStore(db)}
}

func (r *customerLinkRepository) Get(ctx context.Context, userID uuid.UUID) (*domain.CustomerLink, error) {
	var l domain.CustomerLink
	if err := r.get(ctx, "get customer link", &l,
		`SELECT `+customerLinkColumns+` FROM hubrise_customers WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}
	return &l, nil
}

// Begin inserts the link when it is missing. The no-op update on conflict makes RETURNING hand back
// the existing row too, and xmax tells the two cases apart (it is non-zero only on the updated row).
func (r *customerLinkRepository) Begin(ctx context.Context, userID uuid.UUID) (*domain.CustomerLink, bool, error) {
	var row struct {
		domain.CustomerLink
		Existed bool `db:"existed"`
	}
	if err := r.get(ctx, "begin customer link", &row, `
		INSERT INTO hubrise_customers (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING `+customerLinkColumns+`, (xmax <> 0) AS existed`, userID); err != nil {
		return nil, false, err
	}
	return &row.CustomerLink, row.Existed, nil
}

func (r *customerLinkRepository) SetCustomer(ctx context.Context, userID uuid.UUID, hubriseCustomerID, contentHash string, at time.Time) error {
	return r.exec(ctx, "set customer", `
		UPDATE hubrise_customers SET hubrise_customer_id = $2, content_hash = $3, synced_at = $4, updated_at = $4
		WHERE user_id = $1`, userID, hubriseCustomerID, contentHash, at)
}

func (r *customerLinkRepository) SetHash(ctx context.Context, userID uuid.UUID, contentHash string, at time.Time) error {
	return r.exec(ctx, "set customer hash", `
		UPDATE hubrise_customers SET content_hash = $2, synced_at = $3, updated_at = $3 WHERE user_id = $1`,
		userID, contentHash, at)
}

func (r *customerLinkRepository) Forget(ctx context.Context, userID uuid.UUID) error {
	return r.exec(ctx, "forget customer", `
		UPDATE hubrise_customers SET hubrise_customer_id = NULL, content_hash = '', synced_at = NULL, updated_at = now()
		WHERE user_id = $1`, userID)
}

func (r *customerLinkRepository) MarkAnonymised(ctx context.Context, userID uuid.UUID, at time.Time) error {
	return r.exec(ctx, "mark customer anonymised", `
		UPDATE hubrise_customers SET anonymised_at = $2, updated_at = $2 WHERE user_id = $1`, userID, at)
}

type customerSource struct{ store }

// NewCustomerSource returns the reader of the shop's users.
func NewCustomerSource(db *sqlx.DB) domain.CustomerSource { return &customerSource{newStore(db)} }

func (s *customerSource) DeletedNotErased(ctx context.Context, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.sel(ctx, "list deleted customers not erased", &ids, `
		SELECT l.user_id FROM hubrise_customers l
		JOIN users u ON u.id = l.user_id
		WHERE u.deletion_requested_at IS NOT NULL AND l.anonymised_at IS NULL
		  AND NOT EXISTS (
		        SELECT 1 FROM hubrise_outbox o
		        WHERE o.kind = 'customer_anonymise' AND o.dedupe_key = 'cust:' || l.user_id::text
		          AND o.state IN ('pending', 'sending', 'parked'))
		ORDER BY u.deletion_requested_at, l.user_id
		LIMIT $1`, limit)
	return ids, err
}

func (s *customerSource) Profile(ctx context.Context, userID uuid.UUID) (*domain.CustomerProfile, error) {
	var row struct {
		FirstName string `db:"first_name"`
		LastName  string `db:"last_name"`
		Email     string `db:"email"`
		Phone     string `db:"phone_number"`
		Deleted   bool   `db:"deleted"`
	}
	if err := s.get(ctx, "get customer profile", &row, `
		SELECT COALESCE(first_name, '') AS first_name, COALESCE(last_name, '') AS last_name,
		       COALESCE(email, '') AS email, COALESCE(phone_number, '') AS phone_number,
		       (deletion_requested_at IS NOT NULL) AS deleted
		FROM users WHERE id = $1`, userID); err != nil {
		return nil, err
	}
	return &domain.CustomerProfile{
		UserID: userID, FirstName: row.FirstName, LastName: row.LastName, Email: row.Email, Phone: row.Phone, Deleted: row.Deleted,
	}, nil
}

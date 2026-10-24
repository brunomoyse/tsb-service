package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CustomerLink ties a shop user to its customer in the HubRise customer list. A link without a
// HubRise customer id records that a creation was attempted: the customer may exist already (the
// answer may have been lost), so it is looked up by private_ref (the user id) before it is created.
type CustomerLink struct {
	UserID uuid.UUID `db:"user_id"`
	// HubriseCustomerID is nil until the creation succeeded (or the customer was found again).
	HubriseCustomerID *string `db:"hubrise_customer_id"`
	// ContentHash is "<profile hash>|<address hash>" of what was last written to HubRise.
	ContentHash string     `db:"content_hash"`
	SyncedAt    *time.Time `db:"synced_at"`
	// AnonymisedAt is set once the HubRise customer was anonymised: the customer is never created
	// or changed again.
	AnonymisedAt *time.Time `db:"anonymised_at"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

// CustomerLinkRepository stores the links to the customer list.
type CustomerLinkRepository interface {
	// Get returns ErrNotFound when the user has no link.
	Get(ctx context.Context, userID uuid.UUID) (*CustomerLink, error)
	// Begin makes sure the link exists before a creation is attempted. existed is false when it was
	// just inserted, so no HubRise customer can exist yet and the lookup can be skipped.
	Begin(ctx context.Context, userID uuid.UUID) (link *CustomerLink, existed bool, err error)
	// SetCustomer stores the HubRise customer id with the hash of what was written and the time.
	SetCustomer(ctx context.Context, userID uuid.UUID, hubriseCustomerID, contentHash string, at time.Time) error
	// SetHash stores the hash of what was just written to the HubRise customer.
	SetHash(ctx context.Context, userID uuid.UUID, contentHash string, at time.Time) error
	// Forget drops the HubRise customer id, as the customer is gone at HubRise: the next sync looks
	// the customer up again and creates it if needed. The hash is cleared.
	Forget(ctx context.Context, userID uuid.UUID) error
	// MarkAnonymised records that the HubRise customer was anonymised; it does nothing without a link.
	MarkAnonymised(ctx context.Context, userID uuid.UUID, at time.Time) error
}

// CustomerProfile is what the shop knows about a user and HubRise keeps in the customer list.
type CustomerProfile struct {
	UserID    uuid.UUID
	FirstName string
	LastName  string
	Email     string
	Phone     string
	// Deleted is true once the account was deleted (anonymised): the profile is then not worth
	// sending anywhere.
	Deleted bool
}

// CustomerSource reads the users of the shop. Like OrderSource it is the one place where the hubrise
// module touches a table it does not own.
type CustomerSource interface {
	// Profile returns the current profile of the user; ErrNotFound when there is no such user.
	Profile(ctx context.Context, userID uuid.UUID) (*CustomerProfile, error)
	// DeletedNotErased lists, oldest deletion first, the users whose account was deleted and whose
	// HubRise customer was neither anonymised nor has an anonymisation queued: the erasure the
	// deletion hook failed to queue.
	DeletedNotErased(ctx context.Context, limit int) ([]uuid.UUID, error)
}

// CustomerPayloadVersion is the version of the payloads of the customer outbox rows.
const CustomerPayloadVersion = 1

// CustomerPayload is the payload of a customer row: it names the user, whose profile is read when
// the row is sent (so a row that waited sends the latest profile, and a burst of edits is one PATCH).
type CustomerPayload struct {
	Version int       `json:"v"`
	UserID  uuid.UUID `json:"user_id"`
}

// CustomerAnonymisePayload is the payload of a customer_anonymise row. It carries the HubRise ids
// itself, so the row can be sent whatever became of the user and of the link.
type CustomerAnonymisePayload struct {
	Version           int       `json:"v"`
	UserID            uuid.UUID `json:"user_id"`
	HubriseCustomerID string    `json:"hubrise_customer_id"`
	CustomerListID    string    `json:"customer_list_id"`
}

// CustomerDedupeKey is the dedupe key of the outbox rows of one user: they are sent in order.
func CustomerDedupeKey(userID uuid.UUID) string { return "cust:" + userID.String() }

// CustomerFields are the fields of a customer the shop writes to HubRise: the profile, and the
// address of the delivery order being pushed.
type CustomerFields struct {
	FirstName, LastName, Email, Phone string
	// The address of a delivery order; empty for a pickup order, which leaves the stored address alone.
	Address1, PostalCode, City, Latitude, Longitude, DeliveryNotes string
}

// HasAddress reports whether the fields carry the address of a delivery.
func (f CustomerFields) HasAddress() bool {
	return f.Address1 != "" || f.PostalCode != "" || f.City != ""
}

// ProfileHash is the hash of the profile part of the fields.
func (f CustomerFields) ProfileHash() string {
	return hashOf(f.FirstName, f.LastName, f.Email, f.Phone)
}

// AddressHash is the hash of the address part, empty when there is no address.
func (f CustomerFields) AddressHash() string {
	if !f.HasAddress() {
		return ""
	}
	return hashOf(f.Address1, f.PostalCode, f.City, f.Latitude, f.Longitude, f.DeliveryNotes)
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0}) // a separator, so ("a","bc") and ("ab","c") differ
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// JoinCustomerHash builds the stored content hash from its two parts.
func JoinCustomerHash(profile, address string) string { return profile + "|" + address }

// SplitCustomerHash is the inverse of JoinCustomerHash; both parts are empty for a value that is not
// one (a customer never written).
func SplitCustomerHash(hash string) (profile, address string) {
	profile, address, ok := strings.Cut(hash, "|")
	if !ok {
		return "", ""
	}
	return profile, address
}

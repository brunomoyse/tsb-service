package domain

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	CreatedAt       time.Time  `db:"created_at" json:"createdAt"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updatedAt"`
	FirstName       string     `db:"first_name" json:"firstName"`
	LastName        string     `db:"last_name" json:"lastName"`
	Email           string     `db:"email" json:"email"`
	PhoneNumber     *string    `db:"phone_number" json:"phoneNumber"`
	AddressID       *string    `db:"address_id" json:"addressId"`
	DefaultPlaceID  *string    `db:"default_place_id" json:"defaultPlaceId,omitempty"`
	NotifyMarketing bool       `db:"notify_marketing" json:"notifyMarketing"`
	NotifyOrderUpdates  bool       `db:"notify_order_updates" json:"notifyOrderUpdates"`
	DeletionRequestedAt *time.Time `db:"deletion_requested_at" json:"deletionRequestedAt"`
	ZitadelUserID       *string    `db:"zitadel_user_id" json:"-"`
	RRN                 *string    `db:"rrn" json:"rrn,omitempty"`

	// POS-only columns. Present so `SELECT *` from users doesn't fail; not
	// exposed through GraphQL (pin_hash is sensitive).
	PinHash           *string    `db:"pin_hash" json:"-"`
	PinUpdatedAt      *time.Time `db:"pin_updated_at" json:"-"`
	FailedPinAttempts int        `db:"failed_pin_attempts" json:"-"`
	PinLockedUntil    *time.Time `db:"pin_locked_until" json:"-"`
}

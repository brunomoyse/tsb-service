package domain

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("hubrise: not found")
	// ErrNotConnected means there is no connection row, or it holds no access token.
	ErrNotConnected = errors.New("hubrise: not connected")
	// ErrTokenUnreadable means the stored access token cannot be decrypted (HUBRISE_TOKEN_KEY changed
	// or lost, or the stored bytes are damaged). Only reconnecting to HubRise helps.
	ErrTokenUnreadable = errors.New("hubrise: the stored access token cannot be decrypted")
	// ErrInvalidLink means a catalog link does not name exactly the local row of its entity type.
	ErrInvalidLink = errors.New("hubrise: invalid catalog link")

	// ErrNotEnabled means the integration is switched off, so nothing is imported.
	ErrNotEnabled = errors.New("hubrise: the integration is not enabled")
	// ErrEmptyCatalog means the HubRise catalog has no product to import. An empty or half loaded
	// catalog must never retire the whole menu.
	ErrEmptyCatalog = errors.New("hubrise: the catalog has no product")
	// ErrImportInProgress means another import (on this or another instance) is running.
	ErrImportInProgress = errors.New("hubrise: a catalog import is already running")
	// ErrPreviewRequired means sync_catalog cannot be enabled before a successful dry run of the
	// current catalog, less than a day old.
	ErrPreviewRequired = errors.New("hubrise: preview the current catalog before enabling catalog sync")
	// ErrRelinkRequired means the connection points at another HubRise location or catalog than the
	// links were made for: forget the old links before enabling.
	ErrRelinkRequired = errors.New("hubrise: the connection changed location or catalog, forget the old links first")
	// ErrRetireConfirmRequired means the import would retire a large part of the menu and nobody
	// confirmed it.
	ErrRetireConfirmRequired = errors.New("hubrise: the import would retire a large part of the menu, confirm it")
	// ErrManaged means an edit touches a field HubRise owns.
	ErrManaged = errors.New("hubrise: this is managed by HubRise")
	// ErrSKULinked means a SKU is already linked to another local product.
	ErrSKULinked = errors.New("hubrise: the SKU is already linked to another product")
	// ErrInvalidState means the OAuth state of a callback is unknown, already used, expired, or was
	// started by somebody else. The authorization is dropped.
	ErrInvalidState = errors.New("hubrise: the authorization state is unknown, used or expired")
	// ErrAuthorizationFailed means HubRise refused the authorization code (spent, expired) or the
	// client credentials, or the connection could not be set up with the token it returned.
	ErrAuthorizationFailed = errors.New("hubrise: the authorization could not be completed")
)

// InvalidSettingError is the refusal of a settings value. It carries the setting's name and a
// message safe to show the operator.
type InvalidSettingError struct {
	Field   string
	Message string
}

func (e *InvalidSettingError) Error() string {
	return "hubrise: invalid setting " + e.Field + ": " + e.Message
}

// ManagedError is the refusal of an edit of HubRise-owned fields. It matches ErrManaged.
type ManagedError struct {
	// Fields are the owned fields the edit touched.
	Fields []string
}

func (e *ManagedError) Error() string {
	return fmt.Sprintf("%s: %s", ErrManaged.Error(), strings.Join(e.Fields, ", "))
}

func (e *ManagedError) Is(target error) bool { return target == ErrManaged }

// RetireConfirmError carries the numbers behind ErrRetireConfirmRequired. It matches it.
type RetireConfirmError struct {
	Retiring, Linked int
}

func (e *RetireConfirmError) Error() string {
	return fmt.Sprintf("%s (%d of %d products)", ErrRetireConfirmRequired.Error(), e.Retiring, e.Linked)
}

func (e *RetireConfirmError) Is(target error) bool { return target == ErrRetireConfirmRequired }

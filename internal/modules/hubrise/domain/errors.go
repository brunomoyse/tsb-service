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

	// ErrDirectionConflict means syncCatalog was asked while the catalog direction is PUBLISH (or
	// the other way round): the two directions are mutually exclusive.
	ErrDirectionConflict = errors.New("hubrise: the catalog direction is PUBLISH, switch it before turning catalog sync on")
	// ErrPublishMode means a FULL catalog import was asked while the direction is PUBLISH.
	ErrPublishMode = errors.New("hubrise: the dashboard owns the menu (PUBLISH), a full import is not available")
	// ErrNotPublishMode means a publish operation was asked while the direction is not PUBLISH.
	ErrNotPublishMode = errors.New("hubrise: the catalog direction is not PUBLISH")
	// ErrReconnectRequired means the stored scope lacks catalog.write: reconnect first.
	ErrReconnectRequired = errors.New("hubrise: the connection has no catalog.write scope, reconnect HubRise first")
	// ErrAccountCatalog means the catalog belongs to the HubRise account and the connection holds a
	// location token, which cannot modify it.
	ErrAccountCatalog = errors.New("hubrise: the catalog belongs to the HubRise account, connect with an account token or use a location catalog")
	// ErrPreviewStale means the plan the admin confirmed is no longer the plan: preview again.
	ErrPreviewStale = errors.New("hubrise: the menu or the HubRise catalog changed since the preview, preview again")
	// ErrPublishConfirmRequired means the first publish, or a publish that removes a large part of the
	// HubRise catalog, was asked without the confirmation.
	ErrPublishConfirmRequired = errors.New("hubrise: this publish needs an explicit confirmation of the preview")
	// ErrPublishBlocked means the menu cannot be published as it is (the blockers of the preview).
	ErrPublishBlocked = errors.New("hubrise: the menu cannot be published, fix the blockers of the preview")
	// ErrPublishPendingChanges means leaving PUBLISH would abandon unpublished edits.
	ErrPublishPendingChanges = errors.New("hubrise: unpublished menu edits exist, publish them or acknowledge that HubRise will overwrite them")
	// ErrEmptyMenu means the local menu has no product: publishing it would wipe the EPOS menu.
	ErrEmptyMenu = errors.New("hubrise: the local menu has no product, nothing is published")
	// ErrPublishBusy means another publish holds the lease and the request could not wait for it.
	ErrPublishBusy = errors.New("hubrise: a catalog publish is already running")
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

// PublishBlockedError carries the blockers behind ErrPublishBlocked. It matches it.
type PublishBlockedError struct {
	Blockers []Warning
}

func (e *PublishBlockedError) Error() string {
	var parts []string
	for i, b := range e.Blockers {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(e.Blockers)-3))
			break
		}
		parts = append(parts, string(b.Code)+": "+b.Message)
	}
	return ErrPublishBlocked.Error() + " (" + strings.Join(parts, "; ") + ")"
}

func (e *PublishBlockedError) Is(target error) bool { return target == ErrPublishBlocked }

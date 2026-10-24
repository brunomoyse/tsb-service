package domain

import (
	"context"
	"time"
)

// Timing of the catalog publisher (amendment A15.5).
const (
	// PublishDebounce is how long the publisher waits after the last menu change.
	PublishDebounce = 10 * time.Second
	// PublishMinInterval is the minimum time between two runs, across replicas and restarts.
	PublishMinInterval = 30 * time.Second
	// PublishMaxDebounce caps the wait: continuous edits never starve a publish for longer.
	PublishMaxDebounce = 120 * time.Second
	// PublishLeaseTTL is how long a claimed run owns the publisher row. A crash leaves a lease that
	// expires after it.
	PublishLeaseTTL = 90 * time.Second
	// PublishScanInterval is the period of the safety net that compares the local menu hash with the
	// published one, for writers that bypass the change hook.
	PublishScanInterval = 5 * time.Minute
	// PublishBackoffMin and PublishBackoffMax bound the retry delay after a HubRise failure.
	PublishBackoffMin = 30 * time.Second
	PublishBackoffMax = 15 * time.Minute
)

// PublishErrorCode is the stable name of why the last publish did not happen. The values are the
// GraphQL enum HubrisePublishErrorCode.
type PublishErrorCode string

const (
	// PublishWriteScopeMissing: catalog.write was not granted, the admin must reconnect.
	PublishWriteScopeMissing PublishErrorCode = "WRITE_SCOPE_MISSING"
	// PublishAccountCatalog: the catalog is account level and the token is a location token.
	PublishAccountCatalog PublishErrorCode = "ACCOUNT_CATALOG"
	// PublishCatalogNotFound: the catalog of the connection does not exist at HubRise any more.
	PublishCatalogNotFound PublishErrorCode = "CATALOG_NOT_FOUND"
	// PublishRejected: HubRise answered 422; waits for a new edit.
	PublishRejected PublishErrorCode = "REJECTED"
	// PublishInvalidMenu: the pre-flight validation found the menu unpublishable.
	PublishInvalidMenu PublishErrorCode = "INVALID_MENU"
	// PublishUnavailable: HubRise is down, slow or rate limited; retried with backoff.
	PublishUnavailable PublishErrorCode = "UNAVAILABLE"
	// PublishAuth: the token itself is refused; the whole integration is paused.
	PublishAuth PublishErrorCode = "AUTH"
	// PublishRemovalsNeedConfirm: the removals trip the mass-removal threshold and wait for the
	// dashboard confirmation.
	PublishRemovalsNeedConfirm PublishErrorCode = "REMOVALS_NEED_CONFIRM"
)

// PublishState is the single hubrise_catalog_publish row: the state machine of the publisher. It is
// not a queue: "one publish pending" is the row itself.
type PublishState struct {
	// ChangeSeq is bumped by every menu change.
	ChangeSeq int64 `db:"change_seq"`
	// DirtySince is set while edits wait to be published.
	DirtySince    *time.Time `db:"dirty_since"`
	LastChangeAt  *time.Time `db:"last_change_at"`
	NextAttemptAt *time.Time `db:"next_attempt_at"`
	Attempts      int        `db:"attempts"`

	LeaseHolder   *string    `db:"lease_holder"`
	LeasedUntil   *time.Time `db:"leased_until"`
	LastStartedAt *time.Time `db:"last_started_at"`

	LastPublishedAt         *time.Time `db:"last_published_at"`
	LastPublishedSeq        int64      `db:"last_published_seq"`
	LastPublishedLocalHash  *string    `db:"last_published_local_hash"`
	LastPublishedRemoteHash *string    `db:"last_published_remote_hash"`

	FirstPublishConfirmedAt *time.Time `db:"first_publish_confirmed_at"`
	ExternalChangeAt        *time.Time `db:"external_change_at"`

	LastError     *string    `db:"last_error"`
	LastErrorCode *string    `db:"last_error_code"`
	LastErrorAt   *time.Time `db:"last_error_at"`
	BlockedAt     *time.Time `db:"blocked_at"`

	UpdatedAt time.Time `db:"updated_at"`
}

// Dirty reports whether edits wait to be published.
func (s *PublishState) Dirty() bool { return s != nil && s.DirtySince != nil }

// Leased reports whether a run owns the row at now.
func (s *PublishState) Leased(now time.Time) bool {
	return s != nil && s.LeaseHolder != nil && s.LeasedUntil != nil && s.LeasedUntil.After(now)
}

// ErrorCode returns the code of the last error, empty when there is none.
func (s *PublishState) ErrorCode() PublishErrorCode {
	if s == nil || s.LastErrorCode == nil {
		return ""
	}
	return PublishErrorCode(*s.LastErrorCode)
}

// NextDue returns the earliest time a debounced edit may be published, given that the last change
// happened at lastChange, the first of the current burst at dirtySince and the previous run started at
// lastStart (zero when none): ten seconds after the last change, never later than two minutes after
// the first, and never sooner than thirty seconds after the previous run. The same arithmetic runs in
// SQL (MarkDirty); this copy documents it and lets tests compare them.
func NextDue(lastChange, dirtySince, lastStart time.Time) time.Time {
	due := lastChange.Add(PublishDebounce)
	if cap := dirtySince.Add(PublishMaxDebounce); cap.Before(due) {
		due = cap
	}
	if !lastStart.IsZero() {
		if floor := lastStart.Add(PublishMinInterval); floor.After(due) {
			due = floor
		}
	}
	return due
}

// PublishResult is what a successful publish leaves in the row.
type PublishResult struct {
	At         time.Time
	LocalHash  string
	RemoteHash string
}

// PublishFailure describes a run that did not publish.
type PublishFailure struct {
	Code    PublishErrorCode
	Message string
	// RetryAt, when set, is the earliest time of the next attempt (backoff or Retry-After).
	RetryAt *time.Time
	// CountAttempt adds one to the attempts counter (a real failure, not a rate-limit wait).
	CountAttempt bool
}

// PublishedItem is what the last successful PUT contained for one item the shop owns.
type PublishedItem struct {
	EntityType  EntityType `db:"entity_type"`
	HubriseKey  string     `db:"hubrise_key"`
	HubriseRef  string     `db:"hubrise_ref"`
	ContentHash string     `db:"content_hash"`
	PublishedAt time.Time  `db:"published_at"`
}

// LinkHubriseID refreshes the cached HubRise id of a link after a PUT (ids change on every PUT).
type LinkHubriseID struct {
	EntityType EntityType
	Key        string
	HubriseID  string
}

// PublishStateRepository stores the publisher row.
type PublishStateRepository interface {
	// Get returns the row; ErrNotFound only if the migration's row is missing.
	Get(ctx context.Context) (*PublishState, error)
	// MarkDirty records a menu change: change_seq + 1, the dirty flag, a cleared block, and the next
	// attempt (NextDue). It returns the new change_seq.
	MarkDirty(ctx context.Context, now time.Time) (int64, error)
	// Claim takes the lease for a background run when everything allows it (dirty, not blocked, due,
	// no live lease, first publish confirmed, the connection enabled and healthy and publishing),
	// stamping last_started_at. ok is false when it did not. seq is the change_seq the run covers.
	Claim(ctx context.Context, holder string, now time.Time) (seq int64, ok bool, err error)
	// ClaimNow takes the lease for an interactive run: it ignores the dirty flag, the schedule, the
	// block and the confirmation, but not a live lease held by somebody else.
	ClaimNow(ctx context.Context, holder string, now time.Time) (seq int64, ok bool, err error)
	// Extend renews the lease; false when it was lost (another replica took it over).
	Extend(ctx context.Context, holder string, now time.Time) (bool, error)
	// Release gives the lease back without recording a result (an aborted run).
	Release(ctx context.Context, holder string) error
	// Complete records a successful publish of the edits up to seq in one transaction with the
	// replacement of the published items and the refresh of the link ids: the dirty flag is cleared
	// only when no newer edit arrived (change_seq = seq), the error fields and the external change
	// warning are cleared, and the lease is released.
	Complete(ctx context.Context, holder string, seq int64, res PublishResult, items []PublishedItem, ids []LinkHubriseID) error
	// RecordError records a run that did not publish and releases the lease; the dirty flag stays. An
	// empty holder records the error without a lease (the callback path).
	RecordError(ctx context.Context, holder string, f PublishFailure, now time.Time) error
	// Block records a permanent refusal: like RecordError and blocked_at, so nothing is retried until
	// the next menu change or a manual publish.
	Block(ctx context.Context, holder string, code PublishErrorCode, message string, now time.Time) error
	// SetFirstPublishConfirmed stamps (or with nil clears) the confirmation of the first publish.
	SetFirstPublishConfirmed(ctx context.Context, at *time.Time) error
	// SetExternalChange stamps the warning "HubRise was edited elsewhere" (keeping the earliest stamp
	// of a burst), or with nil clears it.
	SetExternalChange(ctx context.Context, at *time.Time) error
	// ClearError clears the error fields and the block (a reconnect fixed the cause).
	ClearError(ctx context.Context) error
	// Reset returns the publisher to "awaiting first publish": unconfirmed, dirty, no error, no block,
	// no external change, no lease.
	Reset(ctx context.Context, now time.Time) error
}

// PublishedItemRepository stores what the last PUT contained.
type PublishedItemRepository interface {
	// Replace makes items the whole set, in one transaction.
	Replace(ctx context.Context, items []PublishedItem) error
	// List returns every published item.
	List(ctx context.Context) ([]PublishedItem, error)
	// ByKeys returns the published items of an entity type with one of the keys, by key.
	ByKeys(ctx context.Context, t EntityType, keys []string) (map[string]PublishedItem, error)
	// Count counts the published items of an entity type.
	Count(ctx context.Context, t EntityType) (int, error)
	// Clear forgets every published item (the connection points at another catalog now).
	Clear(ctx context.Context) error
}

// Issue codes of the catalog publisher: the warnings and blockers of a plan (domain.Warning carries
// them). Warnings never stop a publish, blockers do.
const (
	// WarnCategoryDescriptionKept: the HubRise category has a plain text description, which is never
	// replaced; the translations of its name are not written.
	WarnCategoryDescriptionKept WarningCode = "CATEGORY_DESCRIPTION_KEPT"
	// WarnNameSplitAmbiguous: the local name of a multi-SKU product does not start with the HubRise
	// product name, so the whole name became the SKU name.
	WarnNameSplitAmbiguous WarningCode = "NAME_SPLIT_AMBIGUOUS"
	// WarnRetainedByDeal: a SKU deleted in the shop is still used by a HubRise deal, so it was kept,
	// disabled.
	WarnRetainedByDeal WarningCode = "RETAINED_BY_DEAL"
	// WarnCategoryInUse: a category that is no longer in the shop is kept because a product still uses it.
	WarnCategoryInUse WarningCode = "CATEGORY_IN_USE"
	// WarnNameMissing: a local item has no name in any language; a placeholder name is published.
	WarnNameMissing WarningCode = "NAME_MISSING"
	// WarnCategoryDiverges: the locals of one HubRise product disagree on its category or VAT, which
	// are shared, so the HubRise value is kept.
	WarnCategoryDiverges WarningCode = "PRODUCT_FIELD_SHARED"
	// WarnListSplit: a shared option list diverged between SKUs and one SKU got its own list.
	WarnListSplit WarningCode = "OPTION_LIST_SPLIT"
	// WarnVatNotExpressible: the VAT category has no tax rate (out_of_scope); only the SKU custom
	// field carries it.
	WarnVatNotExpressible WarningCode = "VAT_NOT_EXPRESSIBLE"

	// BlockBaseMissingRef: a HubRise category or option list has no ref, so it cannot be linked.
	BlockBaseMissingRef WarningCode = "BASE_MISSING_REF"
	// BlockDuplicateRef: a ref is used twice among items of one kind.
	BlockDuplicateRef WarningCode = "DUPLICATE_REF"
	// BlockDanglingRef: an item names a category, option list or SKU that does not exist.
	BlockDanglingRef WarningCode = "DANGLING_REF"
	// BlockEmptyProduct: a product has no SKU.
	BlockEmptyProduct WarningCode = "EMPTY_PRODUCT"
	// BlockTaxRatePartial: a tax rate sets only some of delivery, collection and eat_in.
	BlockTaxRatePartial WarningCode = "TAX_RATE_PARTIAL"
	// BlockSkuNameDuplicate: two SKUs of one product have the same name (one unnamed SKU is allowed).
	BlockSkuNameDuplicate WarningCode = "SKU_NAME_DUPLICATE"
	// BlockEmptyOptionList: an option list has no option.
	BlockEmptyOptionList WarningCode = "EMPTY_OPTION_LIST"
	// BlockInvalidMoney: an amount is not a HubRise amount.
	BlockInvalidMoney WarningCode = "INVALID_MONEY"
	// BlockInvalidBarcode: a barcode is not 8, 12 or 13 digits.
	BlockInvalidBarcode WarningCode = "INVALID_BARCODE"
	// BlockOptionLimits: the minimum, maximum or defaults of an option list break the HubRise rules.
	BlockOptionLimits WarningCode = "OPTION_LIMITS"
	// BlockInvalidRef: a ref the shop would have to publish is empty or too long.
	BlockInvalidRef WarningCode = "INVALID_REF"
)

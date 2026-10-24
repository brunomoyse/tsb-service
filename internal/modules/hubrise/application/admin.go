package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
	"tsb-service/pkg/utils"
)

// Audit actions the connection management records itself (the GraphQL mutations are also recorded
// by the audit extension under graphql:<field>).
const (
	AuditConnect    = "hubrise.connect"
	AuditDisconnect = "hubrise.disconnect"
	AuditDirection  = "hubrise.catalog.direction"
)

// oauthStateTTL is how long the state nonce of an authorization lives: as long as the
// authorization code HubRise issues.
const oauthStateTTL = 10 * time.Minute

// maxSettingLength bounds the free text settings (refs and names).
const maxSettingLength = 100

// CallbackEvents returns the events the integration registers for. HubRise answers 422 to any
// other combination, so the registration test pins exactly these.
func CallbackEvents() map[string][]string {
	return map[string][]string{
		"order":     {"update"},
		"catalog":   {"create", "update", "delete"},
		"inventory": {"update", "patch"},
	}
}

// AuditFunc records a privileged action. details never carries a token. It may be nil.
type AuditFunc func(ctx context.Context, action string, success bool, details map[string]any)

// AdminConfig is the part of the deployment configuration the connection management needs.
type AdminConfig struct {
	// RedirectURL is where HubRise sends the browser back with the authorization code
	// (<API_BASE_URL>/hubrise/oauth/callback).
	RedirectURL string
	// CallbackURL is where HubRise posts its events (<API_BASE_URL>/hubrise/callback).
	CallbackURL string
	// Scope overrides the requested scope (hubrise.DefaultScope).
	Scope string
	// StateTTL overrides the lifetime of the state nonce (10 minutes).
	StateTTL time.Duration
}

// AdminDeps are the collaborators of an Admin.
type AdminDeps struct {
	Config      AdminConfig
	OAuth       *hubrise.OAuth
	Connections domain.ConnectionRepository
	OAuthStates domain.OAuthStateRepository
	Links       domain.CatalogLinkRepository
	Orders      domain.OrderLinkRepository
	Outbox      domain.OutboxRepository
	Codec       domain.TokenCodec
	// Clients opens the client of the stored connection.
	Clients ClientProvider
	// TokenClient opens a client for a token that is not stored yet (the one just issued).
	TokenClient func(token string) (*hubrise.Client, error)
	Catalog     *CatalogService
	// Inventory is optional: without it a change of the inventory toggle needs the next sync to
	// push the baseline, and syncing by hand is refused.
	Inventory *InventoryService
	// Publisher is the catalog publisher. Optional: without it the dashboard-owned menu mode is
	// unavailable.
	Publisher *CatalogPublisher
	Clock     domain.Clock
	Audit     AuditFunc
	// Wake, optional, asks the outbox worker to drain now (after a retry).
	Wake func()
}

// Admin is the connection management of the integration: the OAuth connection, the settings, the
// disconnection, and the operations of the dashboard's HubRise page. The GraphQL layer and the
// OAuth callback endpoint only call it.
type Admin struct {
	d AdminDeps
}

// NewAdmin returns the service.
func NewAdmin(d AdminDeps) *Admin {
	if d.Config.Scope == "" {
		d.Config.Scope = hubrise.DefaultScope
	}
	if d.Config.StateTTL <= 0 {
		d.Config.StateTTL = oauthStateTTL
	}
	return &Admin{d: d}
}

// Status is what the dashboard shows about the integration.
type Status struct {
	// Connection is the stored row; nil when the installation never connected. Its sealed token is
	// never exposed by a caller.
	Connection *domain.Connection
	// Backlog counts the pending and parked outbox rows.
	Backlog domain.OutboxCounts
	// UnlinkedProducts counts the visible products an order would send without a SKU ref.
	UnlinkedProducts int
	// Attention counts the orders flagged for staff.
	Attention int
	// Direction is who owns the menu.
	Direction domain.CatalogDirection
	// Publish is the state of the catalog publisher.
	Publish *PublishStatus
}

// Status reads the state of the integration.
func (a *Admin) Status(ctx context.Context) (*Status, error) {
	st := &Status{}
	conn, err := a.d.Connections.Get(ctx)
	switch {
	case errors.Is(err, domain.ErrNotFound):
	case err != nil:
		return nil, err
	default:
		st.Connection = conn
	}
	if st.Backlog, err = a.d.Outbox.Counts(ctx); err != nil {
		return nil, err
	}
	if st.Attention, err = a.d.Orders.CountNeedsAttention(ctx); err != nil {
		return nil, err
	}
	if st.Connection.Connected() {
		if st.UnlinkedProducts, err = a.d.Links.CountUnlinkedProducts(ctx); err != nil {
			return nil, err
		}
	}
	st.Direction = domain.DirectionNone
	if st.Connection != nil {
		st.Direction = st.Connection.CatalogDirection()
	}
	if a.d.Publisher != nil {
		if st.Publish, err = a.d.Publisher.Status(ctx, st.Connection); err != nil {
			return nil, err
		}
	} else {
		st.Publish = &PublishStatus{}
	}
	return st, nil
}

// connected loads the connection and requires a token.
func (a *Admin) connected(ctx context.Context) (*domain.Connection, error) {
	conn, err := a.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.ErrNotConnected
	}
	if err != nil {
		return nil, err
	}
	if !conn.Connected() {
		return nil, domain.ErrNotConnected
	}
	return conn, nil
}

func (a *Admin) audit(ctx context.Context, action string, success bool, details map[string]any) {
	if a.d.Audit != nil {
		a.d.Audit(ctx, action, success, details)
	}
}

// ---- connect ----

// StartConnection begins the authorization for the admin and returns the HubRise URL to send the
// browser to. The state nonce is bound to the admin, usable once, for ten minutes.
func (a *Admin) StartConnection(ctx context.Context, adminID uuid.UUID) (string, error) {
	now := a.d.Clock.Now()
	// Old states are only garbage; failing to purge them must not stop a connection.
	if _, err := a.d.OAuthStates.PurgeExpired(ctx, now); err != nil {
		logging.FromContext(ctx).Warn("hubrise: cannot purge expired oauth states", zap.Error(err))
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // never fails since Go 1.24: a broken source of randomness crashes the process
	state := base64.RawURLEncoding.EncodeToString(raw)
	if err := a.d.OAuthStates.Create(ctx, state, adminID, now.Add(a.d.Config.StateTTL)); err != nil {
		return "", err
	}
	return a.d.OAuth.AuthorizeURL(a.d.Config.RedirectURL, a.d.Config.Scope, state), nil
}

// ConnectResult is the outcome of a completed authorization.
type ConnectResult struct {
	// AdminID is the admin who started it.
	AdminID uuid.UUID
	// RelinkRequired is true when the connection names another HubRise location or catalog than the
	// existing links were made for: the integration stays off until the links are forgotten.
	RelinkRequired bool
}

// CompleteConnection finishes the authorization the browser comes back from. The state must be one
// StartConnection issued, unexpired and unused (domain.ErrInvalidState otherwise: nothing is
// exchanged). The code is traded for a token and the callback registered at HubRise before
// anything is stored, so those failures leave the previous connection as it was. Reconnecting to
// another location or catalog than the links were made for switches the integration off in the
// very statement that stores the new ids (until the owner forgets the links), so a failure after
// that point can no longer push with links of the old catalog. The previous location's token is
// only given up once everything else succeeded. The returned error never carries the code or the
// token.
func (a *Admin) CompleteConnection(ctx context.Context, state, code string) (*ConnectResult, error) {
	now := a.d.Clock.Now()
	adminID, ok, err := a.d.OAuthStates.Consume(ctx, state, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrInvalidState
	}
	actx := adminContext(ctx, adminID)
	fail := func(err error) (*ConnectResult, error) {
		a.audit(actx, AuditConnect, false, map[string]any{"error": truncate(err.Error(), 300)})
		return nil, err
	}

	tok, err := a.d.OAuth.ExchangeCode(ctx, code)
	if err != nil {
		return fail(fmt.Errorf("%w: %w", domain.ErrAuthorizationFailed, err))
	}
	client, err := a.d.TokenClient(tok.AccessToken)
	if err != nil {
		return fail(fmt.Errorf("%w: %w", domain.ErrAuthorizationFailed, err))
	}
	locationName := tok.LocationName
	if locationName == "" { // the token response did not name the location: ask HubRise
		if loc, lerr := client.GetLocation(ctx); lerr == nil {
			locationName = loc.Name
		}
	}
	if _, err := client.SetCallback(ctx, hubrise.Callback{URL: a.d.Config.CallbackURL, Events: CallbackEvents()}); err != nil {
		return fail(fmt.Errorf("%w: register the callback: %w", domain.ErrAuthorizationFailed, err))
	}
	enc, err := a.d.Codec.Encrypt(tok.AccessToken)
	if err != nil {
		return fail(fmt.Errorf("hubrise: seal the token: %w", err))
	}

	previous, err := a.d.Connections.Get(ctx)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		previous = nil
	case err != nil:
		return fail(err)
	}
	params := domain.ConnectParams{
		AccessTokenEnc: enc, AccountID: tok.AccountID, AccountName: tok.AccountName,
		LocationID: tok.LocationID, LocationName: locationName, CatalogID: tok.CatalogID, CatalogName: tok.CatalogName,
		CustomerListID: tok.CustomerListID, CustomerListName: tok.CustomerListName, Scope: a.d.Config.Scope,
	}
	if !previous.Connected() { // pushing becomes possible now: only orders from here on are pushed
		params.PushOrdersSince = now
	}
	// Decided before the row changes and stored with it: whatever fails later, links made for
	// another location or catalog are never used with the new ids.
	needsRelink, err := a.d.Catalog.RelinkNeeded(ctx, previous, tok.LocationID, tok.CatalogID)
	if err != nil {
		return fail(err)
	}
	if needsRelink {
		params.RelinkRequiredAt = &now
	}
	if _, err := a.d.Connections.Connect(ctx, params); err != nil {
		return fail(err)
	}
	if err := a.d.Connections.SetCallbackRegistered(ctx, &now); err != nil {
		return fail(err)
	}
	relink, err := a.d.Catalog.AfterConnect(ctx, previous)
	if err != nil {
		return fail(err)
	}
	a.releaseOldLocation(ctx, previous, tok.LocationID)
	a.audit(actx, AuditConnect, true, map[string]any{
		"location_id": tok.LocationID, "catalog_id": tok.CatalogID, "relink_required": relink,
	})
	return &ConnectResult{AdminID: adminID, RelinkRequired: relink}, nil
}

// releaseOldLocation gives up the previous token when the authorization moved to another location:
// its callback would keep posting events of a location this installation no longer serves. It is
// best effort, the new connection is already stored and settled.
func (a *Admin) releaseOldLocation(ctx context.Context, previous *domain.Connection, newLocation string) {
	if !previous.Connected() || previous.LocationID == nil || *previous.LocationID == newLocation {
		return
	}
	a.revokeToken(ctx, previous, true)
}

// revokeToken deletes the callback (when withCallback) and revokes the access token of conn at
// HubRise. Failures are logged and swallowed: a token HubRise already refuses is as good as gone.
func (a *Admin) revokeToken(ctx context.Context, conn *domain.Connection, withCallback bool) (revoked bool) {
	log := logging.FromContext(ctx)
	if withCallback {
		if client, err := a.d.Clients.Client(ctx, conn); err != nil {
			log.Warn("hubrise: cannot open the client to delete the callback", zap.Error(err))
		} else if err := client.DeleteCallback(ctx); err != nil {
			log.Warn("hubrise: cannot delete the callback", zap.Error(err))
		}
	}
	token, err := conn.AccessToken(a.d.Codec)
	if err != nil {
		log.Warn("hubrise: cannot open the token to revoke it", zap.Error(err))
		return false
	}
	if err := a.d.OAuth.Revoke(ctx, token.Reveal()); err != nil {
		log.Warn("hubrise: cannot revoke the token", zap.Error(err))
		return false
	}
	return true
}

// adminContext is a context that audit entries and logs attribute to the admin.
func adminContext(ctx context.Context, adminID uuid.UUID) context.Context {
	ctx = utils.SetUserID(ctx, adminID.String())
	return utils.SetIsAdmin(ctx, true)
}

// ---- disconnect ----

// Disconnect revokes the token at HubRise, deletes the callback, drops the stored token and
// switches the integration off. The row, its settings and the links stay, unless forgetLinks, in
// which case the links go (the products stay, as ordinary local products). It works on a
// connection HubRise already refuses; a disconnected row only has its links forgotten.
func (a *Admin) Disconnect(ctx context.Context, forgetLinks bool) error {
	conn, err := a.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotConnected
	}
	if err != nil {
		return err
	}
	details := map[string]any{"forget_links": forgetLinks}
	if conn.Connected() {
		details["revoked"] = a.revokeToken(ctx, conn, true)
		if err := a.d.Connections.Disconnect(ctx); err != nil {
			a.audit(ctx, AuditDisconnect, false, details)
			return err
		}
	}
	if forgetLinks {
		n, err := a.d.Catalog.ForgetLinks(ctx)
		if err != nil {
			a.audit(ctx, AuditDisconnect, false, details)
			return err
		}
		details["links_forgotten"] = n
	}
	a.audit(ctx, AuditDisconnect, true, details)
	return nil
}

// ---- settings ----

// validateSettings checks and normalizes a patch. The text options are trimmed; an empty string
// clears the option.
func validateSettings(p *domain.SettingsPatch) error {
	if p.CatalogLanguage != nil {
		lang := strings.ToLower(strings.TrimSpace(*p.CatalogLanguage))
		valid := false
		for _, l := range catalogLanguages {
			valid = valid || l == lang
		}
		if !valid {
			return &domain.InvalidSettingError{Field: "catalogLanguage", Message: "must be one of " + strings.Join(catalogLanguages, ", ")}
		}
		p.CatalogLanguage = &lang
	}
	for name, field := range map[string]**string{
		"variantRef": &p.VariantRef, "channelName": &p.ChannelName,
		"deliveryChargeRef": &p.DeliveryChargeRef, "paymentFeeChargeRef": &p.PaymentFeeChargeRef,
	} {
		if *field == nil {
			continue
		}
		v := strings.TrimSpace(**field)
		if len([]rune(v)) > maxSettingLength || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return &domain.InvalidSettingError{Field: name, Message: fmt.Sprintf("at most %d characters, no control characters", maxSettingLength)}
		}
		*field = &v
	}
	return nil
}

// UpdateSettings applies a settings change and returns the new state.
//
// Switching the integration on is refused while a relink is pending, and switching catalog sync on
// needs a dry run of the current catalog from the last day (domain.ErrPreviewRequired). Pushing
// orders becoming effective stamps push_orders_since, so a toggle never replays history. When
// inventory sync becomes effective the baseline is reset and pushed: the shop kept deciding what is
// sold out while HubRise was not told.
func (a *Admin) UpdateSettings(ctx context.Context, patch domain.SettingsPatch) (*domain.Connection, error) {
	before, err := a.connected(ctx)
	if err != nil {
		return nil, err
	}
	patch.PushOrdersSince = nil
	if err := validateSettings(&patch); err != nil {
		return nil, err
	}
	if patch.Enabled != nil && *patch.Enabled && !before.Enabled {
		if err := a.d.Catalog.CheckEnable(ctx); err != nil {
			return nil, err
		}
	}
	if patch.SyncCatalog != nil && *patch.SyncCatalog && before.PublishCatalog {
		return nil, domain.ErrDirectionConflict
	}
	if patch.PublishImages != nil && *patch.PublishImages && !before.HasWriteScope() {
		return nil, domain.ErrReconnectRequired
	}
	if patch.SyncCatalog != nil && *patch.SyncCatalog && !before.SyncCatalog {
		if err := a.d.Catalog.CheckEnableSyncCatalog(ctx); err != nil {
			return nil, err
		}
	}
	after := applySettings(before, patch)
	if pushEffective(after) && !pushEffective(before) {
		now := a.d.Clock.Now()
		patch.PushOrdersSince = &now
	}
	updated, err := a.d.Connections.UpdateSettings(ctx, patch)
	if err != nil {
		return nil, err
	}
	if inventoryEffective(updated) && !inventoryEffective(before) {
		a.restartInventoryBaseline(ctx)
	}
	return updated, nil
}

func pushEffective(c *domain.Connection) bool { return c.Connected() && c.Enabled && c.PushOrders }

func inventoryEffective(c *domain.Connection) bool {
	return c.Connected() && c.Enabled && c.SyncInventory
}

// applySettings is the connection as the patch would leave it, for the toggles the transitions
// depend on.
func applySettings(c *domain.Connection, p domain.SettingsPatch) *domain.Connection {
	out := *c
	set := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	set(&out.Enabled, p.Enabled)
	set(&out.PushOrders, p.PushOrders)
	set(&out.SyncInventory, p.SyncInventory)
	return &out
}

// restartInventoryBaseline makes the next inventory sync push the shop's sold-out products, and
// does it now when it can. A failure is logged: the 30 minute sync repeats it.
func (a *Admin) restartInventoryBaseline(ctx context.Context) {
	log := logging.FromContext(ctx)
	if err := a.d.Connections.ResetInventoryBaseline(ctx); err != nil {
		log.Warn("hubrise: cannot reset the inventory baseline", zap.Error(err))
		return
	}
	if a.d.Inventory == nil {
		return
	}
	if _, err := a.d.Inventory.Baseline(ctx); err != nil {
		log.Warn("hubrise: cannot push the inventory baseline now", zap.Error(err))
	}
}

// ---- catalog and inventory operations ----

// PreviewCatalog plans an import of the mode without writing anything (see CatalogService.Preview).
func (a *Admin) PreviewCatalog(ctx context.Context, mode domain.SyncMode) (*SyncResult, error) {
	if !mode.Valid() {
		return nil, &domain.InvalidSettingError{Field: "mode", Message: "unknown sync mode"}
	}
	return a.d.Catalog.Preview(ctx, mode)
}

// SyncCatalog imports the catalog in the mode. A mass retirement needs confirmRetire.
func (a *Admin) SyncCatalog(ctx context.Context, mode domain.SyncMode, confirmRetire bool) (*SyncResult, error) {
	if !mode.Valid() {
		return nil, &domain.InvalidSettingError{Field: "mode", Message: "unknown sync mode"}
	}
	return a.d.Catalog.Sync(ctx, SyncRequest{Mode: mode, ConfirmRetire: confirmRetire})
}

// ---- the dashboard-owned menu ----

// SetCatalogDirection switches who owns the menu, atomically (the import flags go off for PUBLISH).
//
//   - To PUBLISH the installation must be connected, not waiting for a relink, and hold catalog.write
//     (domain.ErrReconnectRequired). The publisher starts "awaiting first publish": nothing is
//     written until the first publish is confirmed.
//   - Leaving PUBLISH with edits that were not published needs acknowledgeLocalOverwrite
//     (domain.ErrPublishPendingChanges): HubRise will overwrite them.
//   - To IMPORT the existing rule applies, a dry run of the current catalog from the last day
//     (domain.ErrPreviewRequired).
func (a *Admin) SetCatalogDirection(ctx context.Context, to domain.CatalogDirection, acknowledgeLocalOverwrite bool) (*domain.Connection, error) {
	if !to.Valid() {
		return nil, &domain.InvalidSettingError{Field: "direction", Message: "unknown catalog direction"}
	}
	before, err := a.connected(ctx)
	if err != nil {
		return nil, err
	}
	if to == domain.DirectionImport && before.CatalogDirection() != domain.DirectionImport {
		// Outside the lock: it reads the HubRise catalog.
		if err := a.d.Catalog.CheckEnableSyncCatalog(ctx); err != nil {
			return nil, err
		}
	}
	guard := func(_ context.Context, conn *domain.Connection, state *domain.PublishState) error {
		from := conn.CatalogDirection()
		if from == to {
			return nil
		}
		if to == domain.DirectionPublish {
			if conn.RelinkRequiredAt != nil {
				return domain.ErrRelinkRequired
			}
			if !conn.HasWriteScope() {
				return domain.ErrReconnectRequired
			}
		}
		if from == domain.DirectionPublish && state.Dirty() && !acknowledgeLocalOverwrite {
			return domain.ErrPublishPendingChanges
		}
		return nil
	}
	updated, from, err := a.d.Connections.ChangeCatalogDirection(ctx, to, a.d.Clock.Now(), guard)
	if err != nil {
		return nil, err
	}
	if from != to {
		a.audit(ctx, AuditDirection, true, map[string]any{"from": string(from), "to": string(to), "acknowledged_overwrite": acknowledgeLocalOverwrite})
	}
	return updated, nil
}

// PreviewPublish plans a publish of the menu against the HubRise catalog and writes nothing.
func (a *Admin) PreviewPublish(ctx context.Context, removeForeign bool) (*PublishPreview, error) {
	if a.d.Publisher == nil {
		return nil, domain.ErrNotPublishMode
	}
	return a.d.Publisher.Preview(ctx, removeForeign)
}

// PublishCatalog publishes the menu now (see CatalogPublisher.PublishNow).
func (a *Admin) PublishCatalog(ctx context.Context, req PublishRequest) (*PublishOutcome, error) {
	if a.d.Publisher == nil {
		return nil, domain.ErrNotPublishMode
	}
	return a.d.Publisher.PublishNow(ctx, req)
}

// AcknowledgeExternalChange clears the "HubRise was edited elsewhere" warning.
func (a *Admin) AcknowledgeExternalChange(ctx context.Context) error {
	if a.d.Publisher == nil {
		return nil
	}
	return a.d.Publisher.AcknowledgeExternalChange(ctx)
}

// ProductPublishInfos is the batch behind Product.hubrisePublish: the publish info of the products,
// empty while the direction is not PUBLISH.
func (a *Admin) ProductPublishInfos(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ProductPublishInfo, error) {
	if a.d.Publisher == nil {
		return map[uuid.UUID]ProductPublishInfo{}, nil
	}
	return a.d.Publisher.ProductInfos(ctx, ids)
}

// ForgetLinks clears every catalog link; the products stay. It returns how many links went.
func (a *Admin) ForgetLinks(ctx context.Context) (int64, error) { return a.d.Catalog.ForgetLinks(ctx) }

// LinkProduct links a product to a SKU by hand, or unlinks it with a nil skuRef.
func (a *Admin) LinkProduct(ctx context.Context, productID uuid.UUID, skuRef *string) error {
	return a.d.Catalog.LinkProduct(ctx, productID, skuRef)
}

// SyncInventory reads the HubRise inventory now and applies it. It returns how many links changed
// or had their stock recorded. domain.ErrNotEnabled when the inventory sync is off or paused,
// ErrInventoryBusy when another process is already syncing.
func (a *Admin) SyncInventory(ctx context.Context) (int, error) {
	conn, err := a.connected(ctx)
	if err != nil {
		return 0, err
	}
	if a.d.Inventory == nil || !conn.Active() || !conn.SyncInventory {
		return 0, domain.ErrNotEnabled
	}
	res, err := a.d.Inventory.Sync(ctx, false)
	return res.SoldOut + res.Released + res.Recorded, err
}

// ---- outbox ----

// Outbox lists the newest outbox rows, optionally one state. The payload is deliberately not part
// of the listing: it holds customer data.
func (a *Admin) Outbox(ctx context.Context, state *domain.OutboxState, first int) ([]domain.OutboxItem, error) {
	const def, most = 50, 200
	if first <= 0 {
		first = def
	}
	first = min(first, most)
	items, err := a.d.Outbox.List(ctx, state, first)
	for i := range items {
		items[i].Payload = nil
	}
	return items, err
}

// RetryOutbox reactivates the parked rows: those of the chain of row id, or all of them without an
// id. It returns how many were reactivated.
func (a *Admin) RetryOutbox(ctx context.Context, id *int64) (int64, error) {
	now := a.d.Clock.Now()
	var n int64
	var err error
	if id == nil {
		n, err = a.d.Outbox.RetryAllParked(ctx, now)
	} else {
		n, err = a.d.Outbox.RetryParked(ctx, *id, now)
	}
	if err == nil && n > 0 && a.d.Wake != nil {
		a.d.Wake()
	}
	return n, err
}

// OrderInfo is what staff see about the HubRise side of an order.
type OrderInfo struct {
	HubriseOrderID   *string
	PushState        domain.PushState
	PushedAt         *time.Time
	LastRemoteStatus *string
	RemoteTotal      *string
	NeedsAttention   *string
}

// Order returns the HubRise side of an order, or nil when it has none.
func (a *Admin) Order(ctx context.Context, orderID uuid.UUID) (*OrderInfo, error) {
	l, err := a.d.Orders.Get(ctx, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil //nolint:nilnil // no HubRise order is not an error
	}
	if err != nil {
		return nil, err
	}
	info := &OrderInfo{
		HubriseOrderID: l.HubriseOrderID, PushState: l.PushState, PushedAt: l.PushedAt,
		LastRemoteStatus: l.LastRemoteStatus, NeedsAttention: l.NeedsAttention,
	}
	if l.RemoteTotal != nil {
		s := l.RemoteTotal.StringFixed(2)
		info.RemoteTotal = &s
	}
	return info, nil
}

// ManagedProducts returns, among ids, the products HubRise manages: linked, while the integration
// is enabled with catalog sync on. It is the batch behind Product.hubriseManaged.
func (a *Admin) ManagedProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	conn, err := a.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return map[uuid.UUID]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !conn.Connected() || !conn.Enabled || !conn.SyncCatalog {
		return map[uuid.UUID]bool{}, nil
	}
	return a.d.Links.LinkedProductIDs(ctx, ids)
}

// CallbackURLFor builds the callback and redirect URLs of a deployment from its public API base
// URL (API_BASE_URL).
func CallbackURLFor(apiBaseURL string) (callback, redirect string, err error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(apiBaseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", fmt.Errorf("hubrise: API_BASE_URL %q is not an absolute http(s) URL", apiBaseURL)
	}
	base := u.String()
	return base + "/hubrise/callback", base + "/hubrise/oauth/callback", nil
}

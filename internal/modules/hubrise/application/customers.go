package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// ErrCustomerBusy means another process is writing the same customer right now. The caller puts its
// outbox row back and tries again shortly.
var ErrCustomerBusy = errors.New("hubrise: the customer is being written elsewhere")

// CustomerConfig tunes the customer sync; zero values take the defaults.
type CustomerConfig struct {
	// CallTimeout bounds one HubRise call (rate limiter wait included), like WorkerConfig.CallTimeout (60 s).
	CallTimeout time.Duration
	// LeaseTTL bounds how long the per user lease of a write is held (5 minutes, and at least four
	// times CallTimeout: a write makes up to three calls).
	LeaseTTL time.Duration
	// TransientAttempts is how many times an order is delayed by a customer write that fails for a
	// reason that may pass (HubRise down, rate limited) before the order goes out with a guest
	// customer instead (3). An order must not wait for the customer list indefinitely.
	TransientAttempts int
}

func (c CustomerConfig) withDefaults() CustomerConfig {
	if c.CallTimeout <= 0 {
		c.CallTimeout = time.Minute
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = 5 * time.Minute
	}
	c.LeaseTTL = max(c.LeaseTTL, 4*c.CallTimeout)
	if c.TransientAttempts <= 0 {
		c.TransientAttempts = 3
	}
	return c
}

// CustomerDeps are the collaborators of a CustomerSync.
type CustomerDeps struct {
	Connections domain.ConnectionRepository
	Links       domain.CustomerLinkRepository
	Source      domain.CustomerSource
	Outbox      domain.OutboxRepository
	Leases      domain.LeaseRepository
	Clock       domain.Clock
	// Holder identifies this process in the lease table; empty takes a random one.
	Holder string
	Config CustomerConfig
}

// CustomerSync keeps the HubRise customer list in step with the shop's users (amendment A1 of the
// plan). It has two faces:
//
//   - the hooks the user service calls (ProfileChanged, AccountDeleted): quick outbox inserts that
//     never touch the network and do nothing unless the integration has a customer for the user;
//   - the sends the outbox worker makes: the upsert of the customer just before an order is pushed
//     (ForOrder), the PATCH of a changed profile, and the anonymisation of a deleted account.
//
// A customer is created the first time a registered user's order is pushed, not at registration:
// the customer list then only ever holds people who ordered. Writes to one customer are serialized
// across replicas by a lease named after the user.
type CustomerSync struct {
	d      CustomerDeps
	cfg    CustomerConfig
	holder string
	wake   func()
}

// NewCustomerSync returns the customer sync.
func NewCustomerSync(d CustomerDeps) *CustomerSync {
	holder := d.Holder
	if holder == "" {
		holder = uuid.NewString()
	}
	return &CustomerSync{d: d, cfg: d.Config.withDefaults(), holder: holder, wake: func() {}}
}

// SetWake installs the function called after a row was queued, to wake the outbox worker early.
func (c *CustomerSync) SetWake(wake func()) {
	if wake == nil {
		wake = func() {}
	}
	c.wake = wake
}

// ---- hooks ----

// connection returns the connection row, nil when the installation never connected. It is read on
// every call, like the order hooks do, so a disable is seen at once on every replica.
func (c *CustomerSync) connection(ctx context.Context) (*domain.Connection, error) {
	conn, err := c.d.Connections.Get(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return conn, err
}

// ProfileChanged queues the update of the HubRise customer of a user whose name, email or phone
// changed. A user without a HubRise customer needs nothing: the customer is created, with the
// current profile, at the first order. Updates of a burst collapse into one row, and the profile is
// read when the row is sent.
func (c *CustomerSync) ProfileChanged(ctx context.Context, userID uuid.UUID) error {
	conn, err := c.connection(ctx)
	if err != nil {
		return fmt.Errorf("hubrise: read connection: %w", err)
	}
	if conn == nil || !conn.Active() || !conn.SyncCustomers || conn.CustomerListID == nil {
		return nil
	}
	link, err := c.d.Links.Get(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if link.HubriseCustomerID == nil || link.AnonymisedAt != nil {
		return nil
	}
	payload := encode(domain.CustomerPayload{Version: domain.CustomerPayloadVersion, UserID: userID})
	item := domain.NewOutboxItem(domain.OutboxCustomer, nil, domain.CustomerDedupeKey(userID), payload, c.d.Clock.Now())
	item.CreatedAt = item.NextAttemptAt
	if _, inserted, err := c.d.Outbox.EnqueueIfNoPending(ctx, item); err != nil {
		return err
	} else if inserted {
		c.wake()
	}
	return nil
}

// AccountDeleted queues the anonymisation of the HubRise customer of a user whose account was
// deleted. The row carries the HubRise customer id itself, so it is sent whatever becomes of the
// user and of the link. The profile updates still queued for the user are dropped: they would only
// write personal data the account no longer has. It is queued even while the integration is paused
// or switched off (it is sent once the integration runs again), and only skipped when the
// installation never connected. The personal data frozen in the order rows already pushed (or
// parked) is dropped as well; the reconciler repeats that for the accounts this call missed.
func (c *CustomerSync) AccountDeleted(ctx context.Context, userID uuid.UUID) error {
	conn, err := c.connection(ctx)
	if err != nil {
		return fmt.Errorf("hubrise: read connection: %w", err)
	}
	if conn == nil {
		return nil
	}
	_, scrubErr := c.d.Outbox.ScrubCustomerData(ctx, userID)
	return errors.Join(scrubErr, c.queueAnonymise(ctx, conn, userID))
}

// EraseMissed queues the erasure of the accounts that were deleted while the hook failed (the
// database was down, the process died between the Zitadel call and the hook): the deletion is
// final, so nobody would ask again. The reconciler calls it on every run.
func (c *CustomerSync) EraseMissed(ctx context.Context, limit int) error {
	ids, err := c.d.Source.DeletedNotErased(ctx, limit)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := c.AccountDeleted(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("user %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// queueAnonymise queues the anonymisation row of AccountDeleted.
func (c *CustomerSync) queueAnonymise(ctx context.Context, conn *domain.Connection, userID uuid.UUID) error {
	if conn.CustomerListID == nil {
		return nil
	}
	link, err := c.d.Links.Get(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil // no customer was ever attempted: HubRise holds nothing
	}
	if err != nil {
		return err
	}
	if link.AnonymisedAt != nil {
		return nil
	}
	payload := encode(domain.CustomerAnonymisePayload{
		Version: domain.CustomerPayloadVersion, UserID: userID,
		HubriseCustomerID: deref(link.HubriseCustomerID), CustomerListID: *conn.CustomerListID,
	})
	key := domain.CustomerDedupeKey(userID)
	if _, err := c.d.Outbox.SkipOpenByDedupeKey(ctx, domain.OutboxCustomer, key, "the account was deleted"); err != nil {
		return err
	}
	item := domain.NewOutboxItem(domain.OutboxCustomerAnonymise, nil, key, payload, c.d.Clock.Now())
	item.CreatedAt = item.NextAttemptAt
	if _, _, err := c.d.Outbox.Enqueue(ctx, item); err != nil {
		return err
	}
	logging.FromContext(ctx).Info("hubrise: customer queued for anonymisation", zap.String("user_id", userID.String()))
	c.wake()
	return nil
}

// encode marshals one of the plain payload structs of this file, which cannot fail to encode.
func encode(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- sends ----

// callCustomer bounds one HubRise request, the rate limiter wait included.
func callCustomer[T any](ctx context.Context, c *CustomerSync, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
	defer cancel()
	return f(ctx)
}

// withLease runs f holding the lease of the user's customer, ErrCustomerBusy when somebody else has it.
func (c *CustomerSync) withLease(ctx context.Context, userID uuid.UUID, f func() error) error {
	name := "customer:" + userID.String()
	ok, err := c.d.Leases.Acquire(ctx, name, c.holder, c.d.Clock.Now(), c.cfg.LeaseTTL)
	if err != nil {
		return fmt.Errorf("hubrise: customer lease: %w", err)
	}
	if !ok {
		return ErrCustomerBusy
	}
	defer func() {
		if err := c.d.Leases.Release(context.WithoutCancel(ctx), name, c.holder); err != nil {
			logging.FromContext(ctx).Warn("hubrise: cannot release a customer lease", zap.Error(err))
		}
	}()
	return f()
}

// ForOrder returns the HubRise customer id to put on the order of the payload, creating or updating
// the customer first (the integration guide: PATCH the customer just before the order is pushed). An
// empty id means the order goes out with a guest customer object instead: the sync is off, there is
// no customer list, the account is gone, or the customer list refused the customer in a way that
// retrying will not fix. The order is never held back for that. A failure that may pass (HubRise
// down) is returned so the order is retried, up to TransientAttempts times (attempts is how many
// times the order row was claimed); a refusal of the whole connection (401, 403) is returned at once
// for the worker to pause on.
func (c *CustomerSync) ForOrder(ctx context.Context, client *hubrise.Client, conn *domain.Connection, p domain.CreatePayload, attempts int) (string, error) {
	if !conn.SyncCustomers || conn.CustomerListID == nil || p.UserID == uuid.Nil {
		return "", nil
	}
	var id string
	err := c.withLease(ctx, p.UserID, func() error {
		profile, err := c.d.Source.Profile(ctx, p.UserID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && profile.Deleted) {
			return nil // nobody to keep in the list
		}
		if err != nil {
			return err
		}
		id, err = c.upsert(ctx, client, *conn.CustomerListID, p.UserID, orderFields(profile, p.Customer), false)
		return err
	})
	if err == nil || errors.Is(err, ErrCustomerBusy) {
		return id, err
	}
	return "", c.degrade(ctx, err, p.UserID, attempts)
}

// degrade decides what a failed customer write means for an order: nil sends the order with a guest
// customer, an error holds it back.
func (c *CustomerSync) degrade(ctx context.Context, err error, userID uuid.UUID, attempts int) error {
	var he *hubrise.Error
	switch {
	case !errors.As(err, &he):
		return err // the database, a lease: nothing says HubRise refused
	case he.ConnectionLevel(), he.Kind == hubrise.KindCanceled:
		return err
	case he.Kind == hubrise.KindInvalid, he.Kind == hubrise.KindNotFound:
		logging.FromContext(ctx).Warn("hubrise: customer refused, the order goes out with a guest customer",
			zap.String("user_id", userID.String()), zap.Error(he))
		return nil
	case attempts >= c.cfg.TransientAttempts:
		logging.FromContext(ctx).Warn("hubrise: customer list unavailable, the order goes out with a guest customer",
			zap.String("user_id", userID.String()), zap.Int("attempts", attempts), zap.Error(he))
		return nil
	}
	return err
}

// SendProfile sends the current profile of the user to the HubRise customer (a customer row). skip
// names the reason when there is nothing to send. Errors are the HubRise ones for the worker to
// classify: unlike an order, a profile row that HubRise refuses is parked.
func (c *CustomerSync) SendProfile(ctx context.Context, client *hubrise.Client, conn *domain.Connection, userID uuid.UUID) (skip string, err error) {
	if !conn.SyncCustomers {
		return "customer sync is off", nil
	}
	if conn.CustomerListID == nil {
		return "the connection has no customer list", nil
	}
	err = c.withLease(ctx, userID, func() error {
		link, err := c.d.Links.Get(ctx, userID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			skip = "the user has no HubRise customer"
			return nil
		case err != nil:
			return err
		case link.AnonymisedAt != nil:
			skip = "the customer was anonymised"
			return nil
		}
		profile, err := c.d.Source.Profile(ctx, userID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && profile.Deleted) {
			skip = "the account was deleted"
			return nil
		}
		if err != nil {
			return err
		}
		_, err = c.upsert(ctx, client, *conn.CustomerListID, userID, orderFields(profile, nil), false)
		return err
	})
	return skip, err
}

// SendAnonymise anonymises the HubRise customer named by the payload (a customer_anonymise row). A
// customer HubRise no longer has is nothing left to erase.
func (c *CustomerSync) SendAnonymise(ctx context.Context, client *hubrise.Client, conn *domain.Connection, p domain.CustomerAnonymisePayload) (skip string, err error) {
	listID := p.CustomerListID
	if listID == "" && conn.CustomerListID != nil {
		listID = *conn.CustomerListID
	}
	if listID == "" {
		return "the connection has no customer list", nil
	}
	err = c.withLease(ctx, p.UserID, func() error {
		id := p.HubriseCustomerID
		if id == "" {
			// The creation was attempted and its answer lost: the customer may exist.
			found, err := callCustomer(ctx, c, func(cx context.Context) (*hubrise.Customer, error) {
				return client.FindCustomerByPrivateRef(cx, listID, p.UserID.String())
			})
			if err != nil {
				return err
			}
			if found == nil {
				skip = "HubRise has no customer for the user"
				return nil
			}
			id = found.ID
		}
		_, err := callCustomer(ctx, c, func(cx context.Context) (*hubrise.Customer, error) {
			return client.AnonymiseCustomer(cx, listID, id)
		})
		if hubrise.KindOf(err) == hubrise.KindNotFound {
			skip = "HubRise no longer has the customer"
			return c.d.Links.MarkAnonymised(ctx, p.UserID, c.d.Clock.Now())
		}
		if err != nil {
			return err
		}
		return c.d.Links.MarkAnonymised(ctx, p.UserID, c.d.Clock.Now())
	})
	return skip, err
}

// upsert makes the HubRise customer of the user match fields, and returns its id. The caller holds
// the user's lease. The link is written before a creation is attempted, so a creation whose answer
// was lost (a crash, a dropped response) is recognised afterwards and the customer is looked up by
// private_ref instead of created a second time. A customer HubRise lost is created again, once.
func (c *CustomerSync) upsert(ctx context.Context, client *hubrise.Client, listID string, userID uuid.UUID, f domain.CustomerFields, retried bool) (string, error) {
	link, existed, err := c.d.Links.Begin(ctx, userID)
	if err != nil {
		return "", err
	}
	if link.AnonymisedAt != nil {
		return "", nil // the account is gone: never written again
	}
	profileHash, addressHash := f.ProfileHash(), f.AddressHash()

	if link.HubriseCustomerID == nil {
		var found *hubrise.Customer
		if existed {
			found, err = callCustomer(ctx, c, func(cx context.Context) (*hubrise.Customer, error) {
				return client.FindCustomerByPrivateRef(cx, listID, userID.String())
			})
			if err != nil {
				return "", err
			}
		}
		if found == nil {
			created, err := callCustomer(ctx, c, func(cx context.Context) (*hubrise.Customer, error) {
				return client.CreateCustomer(cx, listID, customerBody(f, userID.String(), true, true))
			})
			if err != nil {
				return "", err
			}
			// A failure to record the id is repaired by the lookup of the next attempt: the link
			// already says a creation was tried.
			if err := c.d.Links.SetCustomer(ctx, userID, created.ID, domain.JoinCustomerHash(profileHash, addressHash), c.d.Clock.Now()); err != nil {
				return "", err
			}
			logging.FromContext(ctx).Info("hubrise: customer created",
				zap.String("user_id", userID.String()), zap.String("hubrise_customer_id", created.ID))
			return created.ID, nil
		}
		// Found again after a crash: adopt it, then bring it up to date.
		if err := c.d.Links.SetCustomer(ctx, userID, found.ID, "", c.d.Clock.Now()); err != nil {
			return "", err
		}
		link.HubriseCustomerID, link.ContentHash = &found.ID, ""
	}

	id := *link.HubriseCustomerID
	oldProfile, oldAddress := domain.SplitCustomerHash(link.ContentHash)
	sendProfile := oldProfile != profileHash
	sendAddress := addressHash != "" && oldAddress != addressHash
	if !sendProfile && !sendAddress {
		return id, nil
	}
	_, err = callCustomer(ctx, c, func(cx context.Context) (*hubrise.Customer, error) {
		return client.UpdateCustomer(cx, listID, id, customerBody(f, "", sendProfile, sendAddress))
	})
	if hubrise.KindOf(err) == hubrise.KindNotFound && !retried {
		// The customer is gone at HubRise (or the PATCH named a list it does not have: the lookup
		// of the next round tells the two apart).
		logging.FromContext(ctx).Warn("hubrise: customer missing at HubRise, looking it up again",
			zap.String("user_id", userID.String()), zap.String("hubrise_customer_id", id))
		if err := c.d.Links.Forget(ctx, userID); err != nil {
			return "", err
		}
		return c.upsert(ctx, client, listID, userID, f, true)
	}
	if err != nil {
		return "", err
	}
	if addressHash == "" {
		addressHash = oldAddress // a pickup order leaves the address HubRise has
	}
	if err := c.d.Links.SetHash(ctx, userID, domain.JoinCustomerHash(profileHash, addressHash), c.d.Clock.Now()); err != nil {
		return "", err
	}
	return id, nil
}

// orderFields are the customer fields an order of the user carries: the current profile, and the
// delivery address of the order when it has one (the order payload holds it for a delivery only).
func orderFields(p *domain.CustomerProfile, order *domain.PayloadCustomer) domain.CustomerFields {
	f := domain.CustomerFields{FirstName: p.FirstName, LastName: p.LastName, Email: p.Email, Phone: p.Phone}
	if order != nil {
		f.Address1, f.PostalCode, f.City = order.Address1, order.PostalCode, order.City
		f.Latitude, f.Longitude, f.DeliveryNotes = order.Latitude, order.Longitude, order.DeliveryNotes
	}
	return f
}

// customerBody is the customer sent to HubRise. privateRef is set on a creation only. Only the parts
// asked for are filled: an update sends what changed. The country of an address is Belgium, the only
// place the shop delivers to.
func customerBody(f domain.CustomerFields, privateRef string, profile, address bool) *hubrise.Customer {
	cu := &hubrise.Customer{PrivateRef: privateRef}
	if profile {
		cu.FirstName, cu.LastName, cu.Email, cu.Phone = f.FirstName, f.LastName, f.Email, f.Phone
	}
	if address && f.HasAddress() {
		cu.Address1, cu.PostalCode, cu.City, cu.Country = f.Address1, f.PostalCode, f.City, "BE"
		cu.Latitude, cu.Longitude, cu.DeliveryNotes = f.Latitude, f.Longitude, f.DeliveryNotes
	}
	return cu
}

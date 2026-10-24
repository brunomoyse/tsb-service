package infrastructure

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
)

// orderSource reads the shop's own order tables (orders, order_product, products, users...) for the
// integration. It is the one place where the hubrise module touches tables it does not own, so the
// module never imports the order module.
type orderSource struct{ store }

// NewOrderSource returns the reader of the shop's orders.
func NewOrderSource(db *sqlx.DB) domain.OrderSource { return &orderSource{newStore(db)} }

// nameOrder picks a translation the way the shop does (fr first, then en, nl, zh); the push is for
// the kitchen, which reads French.
const nameOrder = `array_position(ARRAY['fr', 'en', 'nl', 'zh'], %s::text)`

type snapshotRow struct {
	ID                 uuid.UUID        `db:"id"`
	UserID             uuid.UUID        `db:"user_id"`
	Status             string           `db:"order_status"`
	Type               string           `db:"order_type"`
	Online             bool             `db:"is_online_payment"`
	IsTest             bool             `db:"is_test"`
	Channel            string           `db:"channel"`
	CreatedAt          time.Time        `db:"created_at"`
	PreferredReadyTime *time.Time       `db:"preferred_ready_time"`
	Note               string           `db:"order_note"`
	Extras             []byte           `db:"order_extra"`
	CouponCode         string           `db:"coupon_code"`
	CashAmount         *decimal.Decimal `db:"cash_payment_amount"`
	DeliveryFee        decimal.Decimal  `db:"delivery_fee"`
	TransactionFee     decimal.Decimal  `db:"transaction_fee"`
	TakeawayDiscount   decimal.Decimal  `db:"takeaway_discount"`
	CouponDiscount     decimal.Decimal  `db:"coupon_discount"`
	TotalPrice         decimal.Decimal  `db:"total_price"`
	MolliePaymentID    string           `db:"mollie_payment_id"`
	StreetName         string           `db:"street_name"`
	HouseNumber        string           `db:"house_number"`
	BoxNumber          string           `db:"box_number"`
	Postcode           string           `db:"postcode"`
	Municipality       string           `db:"municipality_name"`
	AddressLat         *float64         `db:"address_lat"`
	AddressLng         *float64         `db:"address_lng"`
	AddressExtra       string           `db:"address_extra"`
	FirstName          string           `db:"first_name"`
	LastName           string           `db:"last_name"`
	Email              string           `db:"email"`
	Phone              string           `db:"phone_number"`
}

const snapshotSQL = `
	SELECT o.id, o.user_id, o.order_status, o.order_type, o.is_online_payment, o.is_test,
	       COALESCE(o.channel, '') AS channel, o.created_at, o.preferred_ready_time,
	       COALESCE(o.order_note, '') AS order_note, o.order_extra, COALESCE(o.coupon_code, '') AS coupon_code,
	       o.cash_payment_amount, COALESCE(o.delivery_fee, 0) AS delivery_fee, o.transaction_fee,
	       o.takeaway_discount, o.coupon_discount, o.total_price,
	       COALESCE((SELECT p.mollie_payment_id FROM mollie_payments p WHERE p.order_id = o.id
	                 ORDER BY (p.status = 'paid') DESC, p.created_at DESC LIMIT 1), '') AS mollie_payment_id,
	       COALESCE(o.street_name, '') AS street_name, COALESCE(o.house_number, '') AS house_number,
	       COALESCE(o.box_number, '') AS box_number, COALESCE(o.postcode, '') AS postcode,
	       COALESCE(o.municipality_name, '') AS municipality_name, o.address_lat, o.address_lng,
	       COALESCE(o.address_extra, '') AS address_extra,
	       COALESCE(u.first_name, '') AS first_name, COALESCE(u.last_name, '') AS last_name,
	       COALESCE(u.email, '') AS email, COALESCE(u.phone_number, '') AS phone_number
	FROM orders o
	LEFT JOIN users u ON u.id = o.user_id
	WHERE o.id = $1`

type lineRow struct {
	ID          uuid.UUID       `db:"id"`
	ProductID   uuid.UUID       `db:"product_id"`
	ProductName string          `db:"product_name"`
	BasePrice   decimal.Decimal `db:"base_price"`
	Quantity    int64           `db:"quantity"`
	TotalPrice  decimal.Decimal `db:"total_price"`
	VatRate     decimal.Decimal `db:"vat_rate_applied"`
}

// linesSQL orders the lines like the kitchen ticket does: by category, code, name.
var linesSQL = fmt.Sprintf(`
	SELECT op.id, op.product_id, op.quantity, op.total_price, op.vat_rate_applied, p.price AS base_price,
	       COALESCE(pt.name, p.code, '') AS product_name
	FROM order_product op
	JOIN products p ON p.id = op.product_id
	JOIN product_categories pc ON pc.id = p.category_id
	LEFT JOIN LATERAL (
		SELECT t.name FROM product_translations t WHERE t.product_id = p.id
		ORDER BY `+nameOrder+` NULLS LAST LIMIT 1
	) pt ON true
	WHERE op.order_id = $1
	ORDER BY pc."order", p.code, pt.name, op.id`, "t.language")

type selectionRow struct {
	LineID    uuid.UUID       `db:"order_product_id"`
	GroupID   uuid.UUID       `db:"group_id"`
	ChoiceID  uuid.UUID       `db:"choice_id"`
	Quantity  int             `db:"quantity"`
	Modifier  decimal.Decimal `db:"price_modifier"`
	Name      string          `db:"name"`
	GroupName string          `db:"group_name"`
}

var selectionsSQL = fmt.Sprintf(`
	SELECT opc.order_product_id, opc.product_choice_group_id AS group_id, opc.product_choice_id AS choice_id,
	       opc.quantity, pc.price_modifier,
	       COALESCE((SELECT t.name FROM product_choice_translations t WHERE t.product_choice_id = pc.id
	                 ORDER BY `+nameOrder+` NULLS LAST LIMIT 1), '') AS name,
	       COALESCE((SELECT g.name FROM product_choice_group_translations g WHERE g.product_choice_group_id = opc.product_choice_group_id
	                 ORDER BY `+nameOrder+` NULLS LAST LIMIT 1), '') AS group_name
	FROM order_product_choices opc
	JOIN product_choices pc ON pc.id = opc.product_choice_id
	WHERE opc.order_product_id = ANY($1)
	ORDER BY opc.order_product_id, pc.sort_order, opc.product_choice_id`, "t.locale", "g.locale")

func (s *orderSource) Snapshot(ctx context.Context, orderID uuid.UUID) (*domain.OrderSnapshot, error) {
	var row snapshotRow
	if err := s.get(ctx, "read order snapshot", &row, snapshotSQL, orderID); err != nil {
		return nil, err
	}
	snap := &domain.OrderSnapshot{
		OrderID: row.ID, UserID: row.UserID, Status: row.Status, Type: row.Type,
		IsOnlinePayment: row.Online, IsTest: row.IsTest, Channel: row.Channel, CreatedAt: row.CreatedAt,
		PreferredReadyTime: row.PreferredReadyTime, Note: row.Note, CouponCode: row.CouponCode,
		CashAmount: row.CashAmount, DeliveryFee: row.DeliveryFee, TransactionFee: row.TransactionFee,
		TakeawayDiscount: row.TakeawayDiscount, CouponDiscount: row.CouponDiscount, TotalPrice: row.TotalPrice,
		MolliePaymentID: row.MolliePaymentID,
		StreetName:      row.StreetName, HouseNumber: row.HouseNumber, BoxNumber: row.BoxNumber,
		Postcode: row.Postcode, Municipality: row.Municipality, AddressLat: row.AddressLat,
		AddressLng: row.AddressLng, AddressExtra: row.AddressExtra,
		FirstName: row.FirstName, LastName: row.LastName, Email: row.Email, Phone: row.Phone,
	}
	if len(row.Extras) > 0 {
		var extras []domain.OrderExtra
		// A malformed extras column must not stop the push: the order still goes out without them.
		if err := json.Unmarshal(row.Extras, &extras); err == nil {
			snap.Extras = extras
		}
	}

	var lines []lineRow
	if err := s.sel(ctx, "read order lines", &lines, linesSQL, orderID); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(lines))
	byLine := make(map[uuid.UUID]int, len(lines))
	for i, l := range lines {
		ids[i] = l.ID
		byLine[l.ID] = i
		snap.Lines = append(snap.Lines, domain.SnapshotLine{
			ProductID: l.ProductID, ProductName: l.ProductName, BasePrice: l.BasePrice,
			Quantity: l.Quantity, TotalPrice: l.TotalPrice, VatRate: l.VatRate,
		})
	}
	if len(ids) == 0 {
		return snap, nil
	}
	var sels []selectionRow
	if err := s.sel(ctx, "read order selections", &sels, selectionsSQL, pq.Array(ids)); err != nil {
		return nil, err
	}
	for _, sel := range sels {
		i := byLine[sel.LineID]
		snap.Lines[i].Selections = append(snap.Lines[i].Selections, domain.SnapshotSelection{
			GroupID: sel.GroupID, ChoiceID: sel.ChoiceID, GroupName: sel.GroupName, Name: sel.Name,
			Modifier: sel.Modifier, Quantity: sel.Quantity,
		})
	}
	return snap, nil
}

func (s *orderSource) State(ctx context.Context, orderID uuid.UUID) (*domain.OrderState, error) {
	var st struct {
		Status     string          `db:"order_status"`
		Type       string          `db:"order_type"`
		Total      decimal.Decimal `db:"total_price"`
		ReadyTime  *time.Time      `db:"estimated_ready_time"`
		OnlinePaid bool            `db:"online_paid"`
	}
	if err := s.get(ctx, "read order state", &st, `
		SELECT o.order_status, o.order_type, o.total_price, o.estimated_ready_time,
		       EXISTS (SELECT 1 FROM mollie_payments p WHERE p.order_id = o.id AND p.status = 'paid') AS online_paid
		FROM orders o WHERE o.id = $1`, orderID); err != nil {
		return nil, err
	}
	return &domain.OrderState{
		Status: st.Status, Type: st.Type, Total: st.Total, OnlinePaid: st.OnlinePaid, EstimatedReadyTime: st.ReadyTime,
	}, nil
}

func (s *orderSource) OpenUnpushed(ctx context.Context, createdAfter time.Time, limit int) ([]uuid.UUID, error) {
	ids := []uuid.UUID{}
	err := s.sel(ctx, "list unpushed orders", &ids, `
		SELECT o.id FROM orders o
		WHERE o.created_at >= $1 AND NOT o.is_test
		  AND o.order_status NOT IN ('FAILED', 'CANCELLED', 'DELIVERED', 'PICKED_UP')
		  AND NOT EXISTS (SELECT 1 FROM hubrise_orders h WHERE h.order_id = o.id)
		  AND (NOT o.is_online_payment
		       OR EXISTS (SELECT 1 FROM mollie_payments p WHERE p.order_id = o.id AND p.status = 'paid'))
		ORDER BY o.created_at, o.id
		LIMIT $2`, createdAfter, limit)
	return ids, err
}

type openLinkRow struct {
	OrderID          uuid.UUID       `db:"order_id"`
	HubriseOrderID   string          `db:"hubrise_order_id"`
	LocalStatus      string          `db:"local_status"`
	LocalTotal       decimal.Decimal `db:"local_total"`
	LastRemoteStatus string          `db:"last_remote_status"`
	NeedsAttention   string          `db:"needs_attention"`
	PushedAt         *time.Time      `db:"pushed_at"`
}

const openLinkColumns = `ho.order_id, ho.hubrise_order_id, o.order_status AS local_status, o.total_price AS local_total,
	COALESCE(ho.last_remote_status, '') AS last_remote_status, COALESCE(ho.needs_attention, '') AS needs_attention, ho.pushed_at`

func openLinks(rows []openLinkRow) []domain.OpenLink {
	out := make([]domain.OpenLink, len(rows))
	for i, r := range rows {
		out[i] = domain.OpenLink(r)
	}
	return out
}

func (s *orderSource) StatusMismatches(ctx context.Context, pairs []domain.StatusPair, limit int) ([]domain.OpenLink, error) {
	locals, remotes := make([]string, len(pairs)), make([]string, len(pairs))
	for i, p := range pairs {
		locals[i], remotes[i] = p.Local, p.Remote
	}
	var rows []openLinkRow
	err := s.sel(ctx, "list status mismatches", &rows, `
		SELECT `+openLinkColumns+`
		FROM hubrise_orders ho
		JOIN orders o ON o.id = ho.order_id
		JOIN unnest($1::text[], $2::text[]) AS m(local_status, remote_status) ON m.local_status = o.order_status
		WHERE ho.push_state = 'sent' AND ho.hubrise_order_id IS NOT NULL
		  AND ho.last_remote_status IS DISTINCT FROM m.remote_status
		  AND NOT (COALESCE(ho.last_remote_status, '') = 'awaiting_collection' AND o.order_type = 'DELIVERY')
		  AND (ho.last_remote_status IS NULL
		       OR ho.last_remote_status NOT IN ('completed', 'cancelled', 'rejected', 'delivery_failed'))
		  AND NOT EXISTS (SELECT 1 FROM hubrise_outbox x
		                  WHERE x.order_id = o.id AND x.kind IN ('order_status', 'order_create')
		                    AND x.state IN ('pending', 'sending', 'parked'))
		ORDER BY ho.updated_at, ho.order_id
		LIMIT $3`, pq.Array(locals), pq.Array(remotes), limit)
	return openLinks(rows), err
}

func (s *orderSource) RefreshCandidates(ctx context.Context, limit int) ([]domain.OpenLink, error) {
	var rows []openLinkRow
	err := s.sel(ctx, "list orders to refresh", &rows, `
		SELECT `+openLinkColumns+`
		FROM hubrise_orders ho
		JOIN orders o ON o.id = ho.order_id
		WHERE ho.push_state = 'sent' AND ho.hubrise_order_id IS NOT NULL
		  AND o.order_status NOT IN ('FAILED', 'CANCELLED', 'DELIVERED', 'PICKED_UP')
		ORDER BY ho.last_remote_read_at NULLS FIRST, ho.pushed_at, ho.order_id
		LIMIT $1`, limit)
	return openLinks(rows), err
}

func (s *orderSource) StuckNew(ctx context.Context, pushedBefore time.Time, limit int) ([]domain.OpenLink, error) {
	var rows []openLinkRow
	err := s.sel(ctx, "list orders stuck in new", &rows, `
		SELECT `+openLinkColumns+`
		FROM hubrise_orders ho
		JOIN orders o ON o.id = ho.order_id
		WHERE ho.push_state = 'sent' AND ho.hubrise_order_id IS NOT NULL
		  AND ho.last_remote_status = 'new' AND ho.pushed_at < $1
		  AND ho.stuck_alerted_at IS NULL
		  AND (ho.needs_attention IS NULL OR ho.needs_attention IN ('UNLINKED_ITEM', 'TOTAL_MISMATCH', 'NOT_ACCEPTED'))
		  AND o.order_status NOT IN ('FAILED', 'CANCELLED', 'DELIVERED', 'PICKED_UP')
		ORDER BY ho.pushed_at, ho.order_id
		LIMIT $2`, pushedBefore, limit)
	return openLinks(rows), err
}

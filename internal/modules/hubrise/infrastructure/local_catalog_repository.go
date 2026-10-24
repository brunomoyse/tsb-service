package infrastructure

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/gosimple/slug"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"

	"tsb-service/internal/modules/hubrise/domain"
)

// catalogImportLockName keys the advisory lock that serializes catalog imports across instances.
const catalogImportLockName = "hubrise:catalog-import"

type localCatalogRepository struct {
	db *sqlx.DB
}

// NewLocalCatalogRepository returns the importer's repository of the local menu on the admin pool.
func NewLocalCatalogRepository(db *sqlx.DB) domain.LocalCatalogRepository {
	return &localCatalogRepository{db: db}
}

type textRow struct {
	Owner       uuid.UUID `db:"owner"`
	Language    string    `db:"language"`
	Name        string    `db:"name"`
	Description string    `db:"description"`
	Source      string    `db:"source"`
}

// selecter is what a snapshot reads through: the pool or one transaction.
type selecter interface {
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
}

func (r *localCatalogRepository) Snapshot(ctx context.Context) (*domain.LocalSnapshot, error) {
	return snapshotOf(ctx, r.db)
}

// SnapshotWithLinks reads the menu and every catalog link in one repeatable-read transaction, so
// the two agree even while the dashboard keeps editing.
func (r *localCatalogRepository) SnapshotWithLinks(ctx context.Context) (*domain.LocalSnapshot, []domain.CatalogLink, error) {
	tx, err := r.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("hubrise: begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	snap, err := snapshotOf(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	links := NewCatalogLinkRepositoryTx(tx)
	var all []domain.CatalogLink
	for _, t := range []domain.EntityType{domain.EntityCategory, domain.EntityProduct, domain.EntityChoiceGroup, domain.EntityChoice} {
		ls, err := links.ListByType(ctx, t, true)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, ls...)
	}
	return snap, all, nil
}

func snapshotOf(ctx context.Context, r selecter) (*domain.LocalSnapshot, error) {
	snap := &domain.LocalSnapshot{}

	var cats []struct {
		ID    uuid.UUID `db:"id"`
		Slug  string    `db:"slug"`
		Order int       `db:"order"`
	}
	if err := r.SelectContext(ctx, &cats, `SELECT id, slug, COALESCE("order", 0) AS "order" FROM product_categories ORDER BY "order", slug`); err != nil {
		return nil, fmt.Errorf("hubrise: snapshot categories: %w", err)
	}
	catTexts, err := texts(ctx, r, `SELECT product_category_id AS owner, language, name, '' AS description, source FROM product_category_translations`)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		snap.Categories = append(snap.Categories, domain.LocalCategory{ID: c.ID, Slug: c.Slug, Order: c.Order, Names: catTexts[c.ID]})
	}

	var prods []struct {
		ID          uuid.UUID       `db:"id"`
		CategoryID  uuid.UUID       `db:"category_id"`
		Code        string          `db:"code"`
		Price       decimal.Decimal `db:"price"`
		VatCategory string          `db:"vat_category"`
		Visible     bool            `db:"is_visible"`
		Available   bool            `db:"is_available"`
	}
	if err := r.SelectContext(ctx, &prods, `
		SELECT id, category_id, COALESCE(code, '') AS code, COALESCE(price, 0) AS price, vat_category, is_visible, is_available
		FROM products ORDER BY id`); err != nil {
		return nil, fmt.Errorf("hubrise: snapshot products: %w", err)
	}
	prodTexts, err := texts(ctx, r, `SELECT product_id AS owner, language, name, COALESCE(description, '') AS description, source FROM product_translations`)
	if err != nil {
		return nil, err
	}
	for _, p := range prods {
		snap.Products = append(snap.Products, domain.LocalProduct{ID: p.ID, CategoryID: p.CategoryID, Code: p.Code, Price: p.Price,
			VatCategory: p.VatCategory, Visible: p.Visible, Available: p.Available, Texts: prodTexts[p.ID]})
	}

	var groups []struct {
		ID        uuid.UUID `db:"id"`
		ProductID uuid.UUID `db:"product_id"`
		Min       int       `db:"min_selections"`
		Max       int       `db:"max_selections"`
		Sort      int       `db:"sort_order"`
	}
	if err := r.SelectContext(ctx, &groups, `SELECT id, product_id, min_selections, max_selections, sort_order FROM product_choice_groups ORDER BY sort_order, id`); err != nil {
		return nil, fmt.Errorf("hubrise: snapshot choice groups: %w", err)
	}
	groupTexts, err := texts(ctx, r, `SELECT product_choice_group_id AS owner, locale AS language, name, '' AS description, source FROM product_choice_group_translations`)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		snap.Groups = append(snap.Groups, domain.LocalGroup{ID: g.ID, ProductID: g.ProductID, Min: g.Min, Max: g.Max, Sort: g.Sort, Names: groupTexts[g.ID]})
	}

	var choices []struct {
		ID            uuid.UUID       `db:"id"`
		ProductID     uuid.UUID       `db:"product_id"`
		GroupID       uuid.UUID       `db:"choice_group_id"`
		PriceModifier decimal.Decimal `db:"price_modifier"`
		Sort          int             `db:"sort_order"`
		Available     bool            `db:"is_available"`
	}
	if err := r.SelectContext(ctx, &choices, `SELECT id, product_id, choice_group_id, price_modifier, sort_order, is_available FROM product_choices ORDER BY sort_order, id`); err != nil {
		return nil, fmt.Errorf("hubrise: snapshot choices: %w", err)
	}
	choiceTexts, err := texts(ctx, r, `SELECT product_choice_id AS owner, locale AS language, name, '' AS description, source FROM product_choice_translations`)
	if err != nil {
		return nil, err
	}
	for _, c := range choices {
		snap.Choices = append(snap.Choices, domain.LocalChoice{ID: c.ID, ProductID: c.ProductID, GroupID: c.GroupID, PriceModifier: c.PriceModifier,
			Sort: c.Sort, Unavailable: !c.Available, Names: choiceTexts[c.ID]})
	}
	return snap, nil
}

// texts reads a translation table into per owner, per language rows.
func texts(ctx context.Context, r selecter, query string) (map[uuid.UUID]map[string]domain.LocalText, error) {
	var rows []textRow
	if err := r.SelectContext(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("hubrise: snapshot translations: %w", err)
	}
	out := map[uuid.UUID]map[string]domain.LocalText{}
	for _, row := range rows {
		if out[row.Owner] == nil {
			out[row.Owner] = map[string]domain.LocalText{}
		}
		out[row.Owner][row.Language] = domain.LocalText{Name: row.Name, Description: row.Description, Source: row.Source}
	}
	return out, nil
}

func (r *localCatalogRepository) InBatch(ctx context.Context, fn func(ctx context.Context, tx domain.CatalogTx) error) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hubrise: begin catalog batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, catalogImportLockName); err != nil {
		return fmt.Errorf("hubrise: lock catalog import: %w", err)
	}
	if err := fn(ctx, &catalogTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hubrise: commit catalog batch: %w", err)
	}
	return nil
}

// catalogTx is the write side of one import batch.
type catalogTx struct {
	tx *sqlx.Tx
}

func (t *catalogTx) Links() domain.CatalogLinkRepository { return NewCatalogLinkRepositoryTx(t.tx) }

// uniqueSlug turns base into a slug that no row of table has yet: base, base-2, base-3...
func (t *catalogTx) uniqueSlug(ctx context.Context, table, base string) (string, error) {
	root := slug.MakeLang(base, "fr")
	if root == "" {
		root = "item"
	}
	for n := 1; ; n++ {
		candidate := root
		if n > 1 {
			candidate = root + "-" + strconv.Itoa(n)
		}
		var taken bool
		if err := t.tx.GetContext(ctx, &taken, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE slug = $1)`, candidate); err != nil {
			return "", fmt.Errorf("hubrise: check slug: %w", err)
		}
		if !taken {
			return candidate, nil
		}
	}
}

func (t *catalogTx) CreateCategory(ctx context.Context, c domain.NewCategory) (uuid.UUID, error) {
	s, err := t.uniqueSlug(ctx, "product_categories", c.SlugBase)
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO product_categories (id, "order", slug) VALUES ($1, $2, $3)`, id, c.Order, s); err != nil {
		return uuid.Nil, fmt.Errorf("hubrise: create category: %w", err)
	}
	return id, nil
}

func (t *catalogTx) CreateProduct(ctx context.Context, p domain.NewProduct) (uuid.UUID, error) {
	// The slug follows the shop's rule: the category's name plus the product's name, French first.
	var categoryName string
	err := t.tx.GetContext(ctx, &categoryName, `
		SELECT name FROM product_category_translations WHERE product_category_id = $1 AND name <> ''
		ORDER BY (language = 'fr') DESC, language LIMIT 1`, p.CategoryID)
	if err != nil && !isNoRows(err) {
		return uuid.Nil, fmt.Errorf("hubrise: read category name: %w", err)
	}
	s, err := t.uniqueSlug(ctx, "products", categoryName+" "+p.SlugName)
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	var code *string
	if p.Code != "" {
		code = &p.Code
	}
	_, err = t.tx.ExecContext(ctx, `
		INSERT INTO products (id, category_id, price, code, slug, vat_category, is_visible, is_available,
			is_halal, is_vegetarian, is_spicy, is_discountable)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, p.CategoryID, p.Price, code, s, p.VatCategory, p.Visible, p.Available, p.Halal, p.Vegetarian, p.Spicy, p.Discountable)
	if err != nil {
		return uuid.Nil, fmt.Errorf("hubrise: create product: %w", err)
	}
	return id, nil
}

func (t *catalogTx) PatchProduct(ctx context.Context, id uuid.UUID, p domain.ProductPatch) error {
	return t.mustChange(ctx, "patch product", `
		UPDATE products SET
			category_id = COALESCE($2::uuid, category_id),
			price = COALESCE($3::numeric, price),
			code = COALESCE($4::text, code),
			vat_category = COALESCE($5::text, vat_category),
			is_visible = COALESCE($6::boolean, is_visible),
			is_available = COALESCE($7::boolean, is_available),
			updated_at = now()
		WHERE id = $1`, id, p.CategoryID, p.Price, p.Code, p.VatCategory, p.Visible, p.Available)
}

func (t *catalogTx) CreateGroup(ctx context.Context, g domain.NewGroup) (uuid.UUID, error) {
	id := uuid.New()
	if _, err := t.tx.ExecContext(ctx,
		`INSERT INTO product_choice_groups (id, product_id, min_selections, max_selections, sort_order) VALUES ($1, $2, $3, $4, $5)`,
		id, g.ProductID, g.Min, g.Max, g.Sort); err != nil {
		return uuid.Nil, fmt.Errorf("hubrise: create choice group: %w", err)
	}
	return id, nil
}

func (t *catalogTx) PatchGroup(ctx context.Context, id uuid.UUID, p domain.GroupPatch) error {
	return t.mustChange(ctx, "patch choice group", `
		UPDATE product_choice_groups SET
			min_selections = COALESCE($2::int, min_selections),
			max_selections = COALESCE($3::int, max_selections),
			sort_order = COALESCE($4::int, sort_order)
		WHERE id = $1`, id, p.Min, p.Max, p.Sort)
}

func (t *catalogTx) CreateChoice(ctx context.Context, c domain.NewChoice) (uuid.UUID, error) {
	id := uuid.New()
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO product_choices (id, product_id, choice_group_id, price_modifier, sort_order, is_available)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, c.ProductID, c.GroupID, c.PriceModifier, c.Sort, !c.Unavailable); err != nil {
		return uuid.Nil, fmt.Errorf("hubrise: create choice: %w", err)
	}
	return id, nil
}

func (t *catalogTx) PatchChoice(ctx context.Context, id uuid.UUID, p domain.ChoicePatch) error {
	var available *bool
	if p.Unavailable != nil {
		v := !*p.Unavailable
		available = &v
	}
	return t.mustChange(ctx, "patch choice", `
		UPDATE product_choices SET
			price_modifier = COALESCE($2::numeric, price_modifier),
			sort_order = COALESCE($3::int, sort_order),
			is_available = COALESCE($4::boolean, is_available)
		WHERE id = $1`, id, p.PriceModifier, p.Sort, available)
}

// textTables names, per target, the table, its owner column and its language column, and whether
// the table has a description and an updated_at.
var textTables = map[domain.TextTarget]struct {
	table, owner, lang    string
	description, updateAt bool
}{
	domain.TextProduct:  {"product_translations", "product_id", "language", true, true},
	domain.TextCategory: {"product_category_translations", "product_category_id", "language", false, true},
	domain.TextGroup:    {"product_choice_group_translations", "product_choice_group_id", "locale", false, false},
	domain.TextChoice:   {"product_choice_translations", "product_choice_id", "locale", false, false},
}

func (t *catalogTx) UpsertTexts(ctx context.Context, target domain.TextTarget, ownerID uuid.UUID, texts []domain.TextWrite) error {
	spec, ok := textTables[target]
	if !ok {
		return fmt.Errorf("hubrise: unknown translation target %q", target)
	}
	columns, values, set := spec.owner+", "+spec.lang+", name, source", "$1, $2, $3, 'hubrise'", "name = EXCLUDED.name, source = 'hubrise'"
	if spec.description {
		columns += ", description"
		values += ", NULLIF($4, '')"
		set += ", description = EXCLUDED.description"
	}
	if spec.updateAt {
		set += ", updated_at = now()"
	}
	query := `INSERT INTO ` + spec.table + ` (` + columns + `) VALUES (` + values + `)
		ON CONFLICT (` + spec.owner + `, ` + spec.lang + `) DO UPDATE SET ` + set + `
		WHERE ` + spec.table + `.source = 'hubrise'`
	for _, text := range texts {
		if text.Name == "" {
			continue
		}
		args := []any{ownerID, text.Language, text.Name}
		if spec.description {
			args = append(args, text.Description)
		}
		if _, err := t.tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("hubrise: write %s translation %s: %w", target, text.Language, err)
		}
	}
	return nil
}

// mustChange runs an UPDATE that must hit exactly one row.
func (t *catalogTx) mustChange(ctx context.Context, what, query string, args ...any) error {
	res, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("hubrise: %s: %w", what, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("hubrise: %s: %w", what, domain.ErrNotFound)
	}
	return nil
}

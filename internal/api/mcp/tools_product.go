package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	productDomain "tsb-service/internal/modules/product/domain"
)

// --- list_products ----------------------------------------------------------

type listProductsIn struct {
	CategoryID *string `json:"categoryId,omitempty" jsonschema:"filter by category UUID; omit to list all"`
	Search     *string `json:"search,omitempty" jsonschema:"case-insensitive substring match against any translation's name"`
	OnlyVisible *bool  `json:"onlyVisible,omitempty" jsonschema:"when true, only products with isVisible=true are returned"`
}

type listProductsOut struct {
	Products []productOut `json:"products"`
	Total    int          `json:"total"`
}

// --- get_product ------------------------------------------------------------

type getProductIn struct {
	ID string `json:"id" jsonschema:"product UUID"`
}

type getProductOut = productOut

// --- create_product ---------------------------------------------------------

type translationIn struct {
	Language    string  `json:"language" jsonschema:"locale code: fr, en, zh, nl"`
	Name        string  `json:"name" jsonschema:"display name in this locale"`
	Description *string `json:"description,omitempty" jsonschema:"optional long description"`
}

type createProductIn struct {
	CategoryID     string          `json:"categoryId" jsonschema:"category UUID"`
	Code           *string         `json:"code,omitempty" jsonschema:"optional SCE 2.0 fiscal code"`
	Price          string          `json:"price" jsonschema:"price in EUR as decimal string e.g. '12.50'"`
	PieceCount     *int            `json:"pieceCount,omitempty" jsonschema:"number of pieces for sushi sets"`
	IsVisible      bool            `json:"isVisible" jsonschema:"shown on the customer storefront"`
	IsAvailable    bool            `json:"isAvailable" jsonschema:"can be ordered (in-stock toggle)"`
	IsHalal        bool            `json:"isHalal"`
	IsVegetarian   bool            `json:"isVegetarian"`
	IsSpicy        bool            `json:"isSpicy"`
	IsLunchOnly    bool            `json:"isLunchOnly" jsonschema:"only available during lunch service"`
	IsDiscountable bool            `json:"isDiscountable" jsonschema:"eligible for coupon/takeaway discounts"`
	VatCategory    string          `json:"vatCategory" jsonschema:"one of: food, beverage, zero_rated, out_of_scope"`
	Translations   []translationIn `json:"translations" jsonschema:"at least 3 translations required for visible products"`
}

// --- update_product ---------------------------------------------------------

type updateProductIn struct {
	ID             string          `json:"id" jsonschema:"product UUID"`
	CategoryID     *string         `json:"categoryId,omitempty"`
	Code           *string         `json:"code,omitempty"`
	Price          *string         `json:"price,omitempty" jsonschema:"new price as decimal string"`
	PieceCount     *int            `json:"pieceCount,omitempty"`
	IsVisible      *bool           `json:"isVisible,omitempty"`
	IsAvailable    *bool           `json:"isAvailable,omitempty"`
	IsHalal        *bool           `json:"isHalal,omitempty"`
	IsVegetarian   *bool           `json:"isVegetarian,omitempty"`
	IsSpicy        *bool           `json:"isSpicy,omitempty"`
	IsLunchOnly    *bool           `json:"isLunchOnly,omitempty"`
	IsDiscountable *bool           `json:"isDiscountable,omitempty"`
	VatCategory    *string         `json:"vatCategory,omitempty"`
	Translations   []translationIn `json:"translations,omitempty" jsonschema:"replace the full translation set; omit to keep existing"`
}

// --- toggle_product_availability -------------------------------------------

type toggleAvailabilityIn struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
}

// --- list_categories --------------------------------------------------------

type listCategoriesOut struct {
	Categories []categoryOut `json:"categories"`
}

func registerProductTools(s *mcpsdk.Server, deps Deps) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_products",
			Description: "List all products with optional filtering by category or name. Returns every translation per product so the chatbot can render in the right language.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args listProductsIn) (*mcpsdk.CallToolResult, listProductsOut, error) {
			products, err := deps.Product.GetProducts(ctx)
			if err != nil {
				return errorResult(fmt.Sprintf("fetch products: %v", err)), listProductsOut{}, nil
			}

			var catFilter *uuid.UUID
			if args.CategoryID != nil && *args.CategoryID != "" {
				id, err := uuid.Parse(*args.CategoryID)
				if err != nil {
					return errorResult(fmt.Sprintf("invalid categoryId: %v", err)), listProductsOut{}, nil
				}
				catFilter = &id
			}
			needle := ""
			if args.Search != nil {
				needle = strings.ToLower(strings.TrimSpace(*args.Search))
			}

			out := listProductsOut{Products: make([]productOut, 0, len(products))}
			for _, p := range products {
				if catFilter != nil && p.CategoryID != *catFilter {
					continue
				}
				if args.OnlyVisible != nil && *args.OnlyVisible && !p.IsVisible {
					continue
				}
				if needle != "" && !matchesTranslation(p.Translations, needle) {
					continue
				}
				out.Products = append(out.Products, toProductOut(p))
			}
			out.Total = len(out.Products)
			return nil, out, nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "get_product",
			Description: "Fetch a single product by UUID, with all translations.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args getProductIn) (*mcpsdk.CallToolResult, getProductOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), productOut{}, nil
			}
			p, err := deps.Product.GetProduct(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("product not found: %v", err)), productOut{}, nil
			}
			return nil, toProductOut(p), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "create_product",
			Description: "Create a new product. Visible products require at least 3 translations. Image upload is out of scope for the chatbot — upload via the dashboard first.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args createProductIn) (*mcpsdk.CallToolResult, productOut, error) {
			catID, err := uuid.Parse(args.CategoryID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid categoryId: %v", err)), productOut{}, nil
			}
			price, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(args.Price), ",", "."))
			if err != nil {
				return errorResult(fmt.Sprintf("invalid price: %v", err)), productOut{}, nil
			}
			vat := productDomain.VatCategory(args.VatCategory)
			if !vat.IsValid() {
				return errorResult(fmt.Sprintf("invalid vatCategory: %q (must be food, beverage, zero_rated, out_of_scope)", args.VatCategory)), productOut{}, nil
			}

			prod, err := deps.Product.CreateProduct(
				ctx,
				catID,
				price,
				args.Code,
				args.PieceCount,
				args.IsVisible,
				args.IsAvailable,
				args.IsHalal,
				args.IsVegetarian,
				args.IsSpicy,
				args.IsLunchOnly,
				args.IsDiscountable,
				vat,
				toDomainTranslations(args.Translations),
			)
			if err != nil {
				return errorResult(fmt.Sprintf("create product: %v", err)), productOut{}, nil
			}
			return nil, toProductOut(prod), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "update_product",
			Description: "Partial-update a product. Omit any field to leave it unchanged. Translations replace the full set when supplied.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args updateProductIn) (*mcpsdk.CallToolResult, productOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), productOut{}, nil
			}
			prod, err := deps.Product.GetProduct(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("product not found: %v", err)), productOut{}, nil
			}

			if args.CategoryID != nil {
				cid, err := uuid.Parse(*args.CategoryID)
				if err != nil {
					return errorResult(fmt.Sprintf("invalid categoryId: %v", err)), productOut{}, nil
				}
				prod.CategoryID = cid
			}
			if args.Price != nil {
				p, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(*args.Price), ",", "."))
				if err != nil {
					return errorResult(fmt.Sprintf("invalid price: %v", err)), productOut{}, nil
				}
				prod.Price = p
			}
			if args.Code != nil {
				prod.Code = args.Code
			}
			if args.PieceCount != nil {
				prod.PieceCount = args.PieceCount
			}
			if args.IsVisible != nil {
				prod.IsVisible = *args.IsVisible
			}
			if args.IsAvailable != nil {
				prod.IsAvailable = *args.IsAvailable
			}
			if args.IsHalal != nil {
				prod.IsHalal = *args.IsHalal
			}
			if args.IsVegetarian != nil {
				prod.IsVegetarian = *args.IsVegetarian
			}
			if args.IsSpicy != nil {
				prod.IsSpicy = *args.IsSpicy
			}
			if args.IsLunchOnly != nil {
				prod.IsLunchOnly = *args.IsLunchOnly
			}
			if args.IsDiscountable != nil {
				prod.IsDiscountable = *args.IsDiscountable
			}
			if args.VatCategory != nil {
				vat := productDomain.VatCategory(*args.VatCategory)
				if !vat.IsValid() {
					return errorResult(fmt.Sprintf("invalid vatCategory: %q", *args.VatCategory)), productOut{}, nil
				}
				prod.VatCategory = vat
			}
			if args.Translations != nil {
				prod.Translations = toDomainTranslations(args.Translations)
			}

			if err := deps.Product.UpdateProduct(ctx, prod); err != nil {
				return errorResult(fmt.Sprintf("update product: %v", err)), productOut{}, nil
			}
			fresh, err := deps.Product.GetProduct(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("refetch after update: %v", err)), productOut{}, nil
			}
			return nil, toProductOut(fresh), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "toggle_product_availability",
			Description: "Shortcut for marking a product available or out-of-stock without changing other fields.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args toggleAvailabilityIn) (*mcpsdk.CallToolResult, productOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), productOut{}, nil
			}
			prod, err := deps.Product.GetProduct(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("product not found: %v", err)), productOut{}, nil
			}
			prod.IsAvailable = args.Available
			if err := deps.Product.UpdateProduct(ctx, prod); err != nil {
				return errorResult(fmt.Sprintf("update product: %v", err)), productOut{}, nil
			}
			fresh, err := deps.Product.GetProduct(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("refetch: %v", err)), productOut{}, nil
			}
			return nil, toProductOut(fresh), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_categories",
			Description: "List all product categories with their translations and display order.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, listCategoriesOut, error) {
			cats, err := deps.Product.GetCategories(ctx)
			if err != nil {
				return errorResult(fmt.Sprintf("fetch categories: %v", err)), listCategoriesOut{}, nil
			}
			out := listCategoriesOut{Categories: make([]categoryOut, len(cats))}
			for i, c := range cats {
				out.Categories[i] = toCategoryOut(c)
			}
			return nil, out, nil
		},
	)
}

func toDomainTranslations(in []translationIn) []productDomain.Translation {
	out := make([]productDomain.Translation, len(in))
	for i, t := range in {
		out[i] = productDomain.Translation{
			Language:    t.Language,
			Name:        t.Name,
			Description: t.Description,
		}
	}
	return out
}

func matchesTranslation(translations []productDomain.Translation, needle string) bool {
	for _, t := range translations {
		if strings.Contains(strings.ToLower(t.Name), needle) {
			return true
		}
		if t.Description != nil && strings.Contains(strings.ToLower(*t.Description), needle) {
			return true
		}
	}
	return false
}


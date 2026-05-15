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

type listChoiceGroupsIn struct {
	ProductID string `json:"productId" jsonschema:"product UUID"`
}

type listChoiceGroupsOut struct {
	Groups []productChoiceGroupOut `json:"groups"`
}

type choiceTranslationIn struct {
	Locale string `json:"locale" jsonschema:"locale code: fr, en, zh, nl"`
	Name   string `json:"name"`
}

type createChoiceGroupIn struct {
	ProductID     string                `json:"productId"`
	MinSelections int                   `json:"minSelections" jsonschema:"minimum number of choices the customer must pick"`
	MaxSelections int                   `json:"maxSelections" jsonschema:"maximum number of choices allowed"`
	SortOrder     int                   `json:"sortOrder"`
	Translations  []choiceTranslationIn `json:"translations"`
}

type updateChoiceGroupIn struct {
	ID            string                `json:"id"`
	MinSelections *int                  `json:"minSelections,omitempty"`
	MaxSelections *int                  `json:"maxSelections,omitempty"`
	SortOrder     *int                  `json:"sortOrder,omitempty"`
	Translations  []choiceTranslationIn `json:"translations,omitempty"`
}

type deleteIDIn struct {
	ID        string `json:"id"`
	Confirmed bool   `json:"confirmed" jsonschema:"must be true; protects against accidental destructive calls by the chatbot"`
}

type createChoiceIn struct {
	ChoiceGroupID *string               `json:"choiceGroupId,omitempty" jsonschema:"target choice group; if omitted, productId is used and a default group is created on demand"`
	ProductID     *string               `json:"productId,omitempty"`
	PriceModifier string                `json:"priceModifier" jsonschema:"extra cost in EUR as decimal string; '0.00' for no surcharge"`
	SortOrder     int                   `json:"sortOrder"`
	Translations  []choiceTranslationIn `json:"translations"`
}

type updateChoiceIn struct {
	ID            string                `json:"id"`
	PriceModifier *string               `json:"priceModifier,omitempty"`
	SortOrder     *int                  `json:"sortOrder,omitempty"`
	Translations  []choiceTranslationIn `json:"translations,omitempty"`
}

func registerChoiceTools(s *mcpsdk.Server, deps Deps) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "list_product_choice_groups",
			Description: "List all choice groups (e.g. 'sauce', 'cooking') configured on a product, with their nested choices.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args listChoiceGroupsIn) (*mcpsdk.CallToolResult, listChoiceGroupsOut, error) {
			pid, err := uuid.Parse(args.ProductID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid productId: %v", err)), listChoiceGroupsOut{}, nil
			}
			groups, err := deps.Product.GetChoiceGroupsByProductID(ctx, pid)
			if err != nil {
				return errorResult(fmt.Sprintf("fetch groups: %v", err)), listChoiceGroupsOut{}, nil
			}
			out := listChoiceGroupsOut{Groups: make([]productChoiceGroupOut, len(groups))}
			for i, g := range groups {
				out.Groups[i] = toChoiceGroupOut(g)
			}
			return nil, out, nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "create_product_choice_group",
			Description: "Create a new choice group on a product. minSelections must be <= maxSelections.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args createChoiceGroupIn) (*mcpsdk.CallToolResult, productChoiceGroupOut, error) {
			pid, err := uuid.Parse(args.ProductID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid productId: %v", err)), productChoiceGroupOut{}, nil
			}
			if args.MinSelections < 0 || args.MaxSelections < 1 || args.MinSelections > args.MaxSelections {
				return errorResult("invalid selection bounds: require 0 <= min <= max and max >= 1"), productChoiceGroupOut{}, nil
			}
			translations := make([]productDomain.ChoiceTranslation, len(args.Translations))
			for i, t := range args.Translations {
				translations[i] = productDomain.ChoiceTranslation{Locale: t.Locale, Name: t.Name}
			}
			group := &productDomain.ProductChoiceGroup{
				ID:            uuid.New(),
				ProductID:     pid,
				MinSelections: args.MinSelections,
				MaxSelections: args.MaxSelections,
				SortOrder:     args.SortOrder,
				Translations:  translations,
			}
			if err := deps.Product.CreateChoiceGroup(ctx, group); err != nil {
				return errorResult(fmt.Sprintf("create group: %v", err)), productChoiceGroupOut{}, nil
			}
			return nil, toChoiceGroupOut(group), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "update_product_choice_group",
			Description: "Partial-update a choice group. Translations replace the full set when supplied.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args updateChoiceGroupIn) (*mcpsdk.CallToolResult, productChoiceGroupOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), productChoiceGroupOut{}, nil
			}
			group, err := deps.Product.GetChoiceGroupByID(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("group not found: %v", err)), productChoiceGroupOut{}, nil
			}
			if args.MinSelections != nil {
				group.MinSelections = *args.MinSelections
			}
			if args.MaxSelections != nil {
				group.MaxSelections = *args.MaxSelections
			}
			if group.MinSelections < 0 || group.MaxSelections < 1 || group.MinSelections > group.MaxSelections {
				return errorResult("invalid selection bounds after update"), productChoiceGroupOut{}, nil
			}
			if args.SortOrder != nil {
				group.SortOrder = *args.SortOrder
			}
			if args.Translations != nil {
				ts := make([]productDomain.ChoiceTranslation, len(args.Translations))
				for i, t := range args.Translations {
					ts[i] = productDomain.ChoiceTranslation{Locale: t.Locale, Name: t.Name}
				}
				group.Translations = ts
			}
			if err := deps.Product.UpdateChoiceGroup(ctx, group); err != nil {
				return errorResult(fmt.Sprintf("update group: %v", err)), productChoiceGroupOut{}, nil
			}
			return nil, toChoiceGroupOut(group), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "delete_product_choice_group",
			Description: "Delete a choice group and all its choices. Requires confirmed=true to prevent accidental loss.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args deleteIDIn) (*mcpsdk.CallToolResult, struct{ Deleted bool `json:"deleted"` }, error) {
			if !args.Confirmed {
				return errorResult("confirmation required: pass confirmed=true to delete this choice group"), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			if err := deps.Product.DeleteChoiceGroup(ctx, id); err != nil {
				return errorResult(fmt.Sprintf("delete group: %v", err)), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			return nil, struct{ Deleted bool `json:"deleted"` }{Deleted: true}, nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "create_product_choice",
			Description: "Add a choice (option) inside a choice group. priceModifier is a non-negative decimal string in EUR.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args createChoiceIn) (*mcpsdk.CallToolResult, productChoiceOut, error) {
			price, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(args.PriceModifier), ",", "."))
			if err != nil {
				return errorResult(fmt.Sprintf("invalid priceModifier: %v", err)), productChoiceOut{}, nil
			}
			if price.Sign() < 0 {
				return errorResult("priceModifier must be zero or positive"), productChoiceOut{}, nil
			}

			var group *productDomain.ProductChoiceGroup
			switch {
			case args.ChoiceGroupID != nil && *args.ChoiceGroupID != "":
				gid, err := uuid.Parse(*args.ChoiceGroupID)
				if err != nil {
					return errorResult(fmt.Sprintf("invalid choiceGroupId: %v", err)), productChoiceOut{}, nil
				}
				g, err := deps.Product.GetChoiceGroupByID(ctx, gid)
				if err != nil {
					return errorResult(fmt.Sprintf("group not found: %v", err)), productChoiceOut{}, nil
				}
				group = g
			case args.ProductID != nil && *args.ProductID != "":
				pid, err := uuid.Parse(*args.ProductID)
				if err != nil {
					return errorResult(fmt.Sprintf("invalid productId: %v", err)), productChoiceOut{}, nil
				}
				groups, err := deps.Product.GetChoiceGroupsByProductID(ctx, pid)
				if err != nil {
					return errorResult(fmt.Sprintf("fetch groups: %v", err)), productChoiceOut{}, nil
				}
				if len(groups) == 0 {
					return errorResult("product has no choice groups; create one first with create_product_choice_group"), productChoiceOut{}, nil
				}
				group = groups[0]
			default:
				return errorResult("either choiceGroupId or productId is required"), productChoiceOut{}, nil
			}

			translations := make([]productDomain.ChoiceTranslation, len(args.Translations))
			for i, t := range args.Translations {
				translations[i] = productDomain.ChoiceTranslation{Locale: t.Locale, Name: t.Name}
			}
			choice := &productDomain.ProductChoice{
				ID:            uuid.New(),
				ProductID:     group.ProductID,
				ChoiceGroupID: group.ID,
				PriceModifier: price,
				SortOrder:     args.SortOrder,
				Translations:  translations,
			}
			if err := deps.Product.CreateChoice(ctx, choice); err != nil {
				return errorResult(fmt.Sprintf("create choice: %v", err)), productChoiceOut{}, nil
			}
			return nil, toChoiceOut(choice), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "update_product_choice",
			Description: "Partial-update a single choice option (price modifier, sort order, translations).",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args updateChoiceIn) (*mcpsdk.CallToolResult, productChoiceOut, error) {
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), productChoiceOut{}, nil
			}
			choice, err := deps.Product.GetChoiceByID(ctx, id)
			if err != nil {
				return errorResult(fmt.Sprintf("choice not found: %v", err)), productChoiceOut{}, nil
			}
			if args.PriceModifier != nil {
				price, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(*args.PriceModifier), ",", "."))
				if err != nil {
					return errorResult(fmt.Sprintf("invalid priceModifier: %v", err)), productChoiceOut{}, nil
				}
				if price.Sign() < 0 {
					return errorResult("priceModifier must be zero or positive"), productChoiceOut{}, nil
				}
				choice.PriceModifier = price
			}
			if args.SortOrder != nil {
				choice.SortOrder = *args.SortOrder
			}
			if args.Translations != nil {
				ts := make([]productDomain.ChoiceTranslation, len(args.Translations))
				for i, t := range args.Translations {
					ts[i] = productDomain.ChoiceTranslation{
						ProductChoiceID: id,
						Locale:          t.Locale,
						Name:            t.Name,
					}
				}
				choice.Translations = ts
			}
			if err := deps.Product.UpdateChoice(ctx, choice); err != nil {
				return errorResult(fmt.Sprintf("update choice: %v", err)), productChoiceOut{}, nil
			}
			return nil, toChoiceOut(choice), nil
		},
	)

	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "delete_product_choice",
			Description: "Delete a choice option. Requires confirmed=true to prevent accidental loss.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args deleteIDIn) (*mcpsdk.CallToolResult, struct{ Deleted bool `json:"deleted"` }, error) {
			if !args.Confirmed {
				return errorResult("confirmation required: pass confirmed=true to delete this choice"), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			id, err := uuid.Parse(args.ID)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid id: %v", err)), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			if err := deps.Product.DeleteChoice(ctx, id); err != nil {
				return errorResult(fmt.Sprintf("delete choice: %v", err)), struct{ Deleted bool `json:"deleted"` }{}, nil
			}
			return nil, struct{ Deleted bool `json:"deleted"` }{Deleted: true}, nil
		},
	)
}

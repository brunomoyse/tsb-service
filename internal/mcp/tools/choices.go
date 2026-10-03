package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
)

func registerChoices(s *mcp.Server, d *Deps) {
	type GroupIn struct {
		GroupID       string            `json:"group_id,omitempty" jsonschema:"existing group to change (from get_product); omit to create a new group"`
		ProductID     string            `json:"product_id,omitempty" jsonschema:"product to add a new group to (only when group_id is omitted)"`
		Names         map[string]string `json:"names,omitempty" jsonschema:"group name by language code; fr required for a new group"`
		MinSelections *int              `json:"min_selections,omitempty" jsonschema:"minimum choices the customer must pick (0 = optional)"`
		MaxSelections *int              `json:"max_selections,omitempty" jsonschema:"maximum choices the customer can pick"`
		SortOrder     *int              `json:"sort_order,omitempty" jsonschema:"position among the product's groups"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_choice_group_change", Annotations: proposeOnly,
		Description: describe(`Propose creating a choice group on a product (give product_id) or changing an existing one (give group_id): names, min/max selections, position. Creates a pending change only.`,
			`propose_choice_group_change({"group_id": "g-sauce", "max_selections": 2, "request_context": "酱可以选两个"})`),
	}, func(ctx context.Context, in GroupIn) (actions.Proposal, error) {
		if (in.GroupID == "") == (in.ProductID == "") {
			return actions.Proposal{}, actions.Userf("Give either group_id (to change a group) or product_id (to create one).")
		}
		return d.propose(ctx, "propose_choice_group_change", actions.KindChoiceGroupUpsert, actions.ChoiceGroupParams{
			GroupID: in.GroupID, ProductID: in.ProductID, Names: in.Names, MinSelections: in.MinSelections, MaxSelections: in.MaxSelections, SortOrder: in.SortOrder,
		}, nil, in.RequestContext)
	})

	type ChoiceIn struct {
		ChoiceID           string            `json:"choice_id,omitempty" jsonschema:"existing choice to change (from get_product); omit to create a new choice"`
		GroupID            string            `json:"group_id,omitempty" jsonschema:"group to add a new choice to (only when choice_id is omitted)"`
		Names              map[string]string `json:"names,omitempty" jsonschema:"choice name by language code; fr required for a new choice"`
		PriceModifierCents *int64            `json:"price_modifier_cents,omitempty" jsonschema:"surcharge in cents added to the product price, 0 for none"`
		SortOrder          *int              `json:"sort_order,omitempty"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_choice_change", Annotations: proposeOnly,
		Description: describe(`Propose creating a choice in a group (give group_id) or changing one (give choice_id): names, surcharge, position. Surcharge changes are limited like prices. Creates a pending change only.`,
			`propose_choice_change({"choice_id": "c-spicy", "price_modifier_cents": 70, "request_context": "辣酱加70分"})`),
	}, func(ctx context.Context, in ChoiceIn) (actions.Proposal, error) {
		if (in.ChoiceID == "") == (in.GroupID == "") {
			return actions.Proposal{}, actions.Userf("Give either choice_id (to change a choice) or group_id (to create one).")
		}
		return d.propose(ctx, "propose_choice_change", actions.KindChoiceUpsert, actions.ChoiceParams{
			ChoiceID: in.ChoiceID, GroupID: in.GroupID, Names: in.Names, PriceModifierCents: in.PriceModifierCents, SortOrder: in.SortOrder,
		}, nil, in.RequestContext)
	})

	type GroupDeleteIn struct {
		GroupID string `json:"group_id"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_choice_group_deletion", Annotations: proposeOnly,
		Description: describe(`Propose deleting a choice group and all its choices. Cannot be undone. Creates a pending change only.`,
			`propose_choice_group_deletion({"group_id": "g-sauce", "request_context": "不要酱汁选项了"})`),
	}, func(ctx context.Context, in GroupDeleteIn) (actions.Proposal, error) {
		return d.propose(ctx, "propose_choice_group_deletion", actions.KindChoiceGroupDelete, actions.ChoiceGroupRef{GroupID: in.GroupID}, nil, in.RequestContext)
	})

	type ChoiceDeleteIn struct {
		ChoiceID string `json:"choice_id"`
		WriteContext
	}
	add(s, d, &mcp.Tool{
		Name: "propose_choice_deletion", Annotations: proposeOnly,
		Description: describe(`Propose deleting one choice from a group. Cannot be undone. Creates a pending change only.`,
			`propose_choice_deletion({"choice_id": "c-spicy", "request_context": "去掉辣酱"})`),
	}, func(ctx context.Context, in ChoiceDeleteIn) (actions.Proposal, error) {
		return d.propose(ctx, "propose_choice_deletion", actions.KindChoiceDelete, actions.ChoiceRef{ChoiceID: in.ChoiceID}, nil, in.RequestContext)
	})
}

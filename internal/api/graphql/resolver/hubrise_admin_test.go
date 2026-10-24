package resolver

import (
	"testing"

	"tsb-service/internal/api/graphql/model"
	hubriseDomain "tsb-service/internal/modules/hubrise/domain"
)

func TestHubriseDiffReachesTheDashboardWithItsWarningsAndConflicts(t *testing.T) {
	got := toHubriseDiff(&hubriseDomain.Diff{
		CatalogHash: "h",
		Changes: []hubriseDomain.Change{
			{Entity: hubriseDomain.EntityProduct, Action: hubriseDomain.ActionCreate, Key: "P1", Name: "Maki"},
			{Entity: hubriseDomain.EntityChoiceGroup, Action: hubriseDomain.ActionUpdate, Key: "S|L", Name: "Sauce", Fields: []string{"name"}},
		},
		Warnings: []hubriseDomain.Warning{
			{Code: hubriseDomain.WarnVATUnmapped, Key: "P2", Message: "no VAT category"},
			{Code: hubriseDomain.WarnVATUnmapped, Message: "general"},
		},
		Conflicts: []hubriseDomain.Conflict{
			{Entity: hubriseDomain.EntityProduct, Key: "P3", Name: "Nigiri", Reason: "two products match", Candidates: []string{"A", "B"}},
			{Entity: hubriseDomain.EntityCategory, Name: "Boissons", Reason: "no match"},
		},
		Unchanged: 4, LinkedProducts: 9, RetiringProducts: 2, RetireNeedsConfirm: true,
	})

	if got.CatalogHash != "h" || got.Unchanged != 4 || got.LinkedProducts != 9 || got.RetiringProducts != 2 || !got.RetireNeedsConfirm {
		t.Fatalf("counters: %+v", got)
	}
	if len(got.Changes) != 2 || got.Changes[0].Entity != model.HubriseCatalogEntityProduct || got.Changes[0].Action != model.HubriseCatalogActionCreate {
		t.Fatalf("changes: %+v", got.Changes)
	}
	if got.Changes[0].Fields == nil || len(got.Changes[0].Fields) != 0 {
		t.Fatalf("a change without fields is an empty list, not null: %#v", got.Changes[0].Fields)
	}
	if len(got.Changes[1].Fields) != 1 || got.Changes[1].Fields[0] != "name" {
		t.Fatalf("fields: %+v", got.Changes[1].Fields)
	}
	if len(got.Warnings) != 2 || got.Warnings[0].Key == nil || *got.Warnings[0].Key != "P2" || got.Warnings[1].Key != nil {
		t.Fatalf("warnings: %+v", got.Warnings)
	}
	if len(got.Conflicts) != 2 || got.Conflicts[0].Message != "Nigiri: two products match (A, B)" || got.Conflicts[1].Message != "Boissons: no match" || got.Conflicts[1].Key != nil {
		t.Fatalf("conflicts: %+v", got.Conflicts)
	}
}

func TestOptionalOmitsAnEmptyText(t *testing.T) {
	if optional("") != nil {
		t.Fatal("an empty text is null")
	}
	if v := optional("x"); v == nil || *v != "x" {
		t.Fatal("a text is kept")
	}
}

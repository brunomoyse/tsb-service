package resolver

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/api/graphql/model"
	hubriseApplication "tsb-service/internal/modules/hubrise/application"
	hubriseDomain "tsb-service/internal/modules/hubrise/domain"
)

func TestThePublishPreviewListsRemovalsFirstAndCapsTheChanges(t *testing.T) {
	var changes []hubriseApplication.PublishChange
	for i := 0; i < 400; i++ {
		changes = append(changes, hubriseApplication.PublishChange{Entity: hubriseDomain.EntityChoice, Action: hubriseApplication.ActionPublishUpdate, Key: fmt.Sprintf("u%d", i), Name: "u"})
	}
	for i := 0; i < 100; i++ {
		changes = append(changes, hubriseApplication.PublishChange{Entity: hubriseDomain.EntityProduct, Action: hubriseApplication.ActionPublishAdd, Key: fmt.Sprintf("a%d", i), Name: "a"})
	}
	for i := 0; i < 5; i++ {
		changes = append(changes, hubriseApplication.PublishChange{Entity: hubriseDomain.EntityCategory, Action: hubriseApplication.ActionPublishRemove, Key: fmt.Sprintf("r%d", i), Name: "r", Fields: []string{"x"}})
	}
	changes = append(changes, hubriseApplication.PublishChange{Entity: hubriseDomain.EntityChoiceGroup, Action: hubriseApplication.ActionPublishAdd, Key: "g", Name: "g"})
	now := time.Now()
	got := toHubrisePublishPreview(&hubriseApplication.PublishPreview{
		FirstPublish: true, GeneratedAt: now,
		Plan: &hubriseApplication.PublishPlan{
			PlanHash: "h", Changes: changes, Unchanged: 7, RemovalsNeedConfirm: true,
			Foreign:   []hubriseApplication.ForeignItem{{Entity: hubriseDomain.EntityProduct, Ref: "F", Name: "Foreign"}},
			Warnings:  []hubriseDomain.Warning{{Code: hubriseDomain.WarnNameMissing, Key: "k", Message: "m"}},
			Blockers:  []hubriseDomain.Warning{{Code: hubriseDomain.BlockDuplicateRef, Message: "dup"}},
			Preserved: nil,
		},
	})

	assert.Equal(t, "h", got.PlanHash)
	assert.True(t, got.FirstPublish)
	assert.True(t, got.RemovalsNeedConfirm)
	assert.Equal(t, 101, got.Added)
	assert.Equal(t, 400, got.Updated)
	assert.Equal(t, 5, got.Removed)
	assert.Equal(t, 7, got.Unchanged)
	assert.Equal(t, 1, got.Foreign)
	assert.Equal(t, now, got.GeneratedAt)
	assert.True(t, got.Truncated)
	require.Len(t, got.Changes, 500)
	for i := 0; i < 5; i++ {
		assert.Equal(t, model.HubrisePublishActionRemove, got.Changes[i].Action, "removals come first")
	}
	assert.Equal(t, model.HubrisePublishActionAdd, got.Changes[5].Action)
	assert.Equal(t, []string{"x"}, got.Changes[0].Fields)
	assert.Equal(t, []string{}, got.Changes[5].Fields, "no fields is an empty list")
	assert.Equal(t, []string{}, got.Preserved)

	// Counts per entity, in the order categories, products, groups, choices.
	require.Len(t, got.Counts, 4)
	assert.Equal(t, model.HubriseCatalogEntityCategory, got.Counts[0].Entity)
	assert.Equal(t, 5, got.Counts[0].Removed)
	assert.Equal(t, model.HubriseCatalogEntityProduct, got.Counts[1].Entity)
	assert.Equal(t, 100, got.Counts[1].Added)
	assert.Equal(t, model.HubriseCatalogEntityChoiceGroup, got.Counts[2].Entity)
	assert.Equal(t, model.HubriseCatalogEntityChoice, got.Counts[3].Entity)
	assert.Equal(t, 400, got.Counts[3].Updated)

	require.Len(t, got.ForeignItems, 1)
	assert.Equal(t, "F", got.ForeignItems[0].Ref)
	require.Len(t, got.Warnings, 1)
	assert.Equal(t, "k", *got.Warnings[0].Key)
	require.Len(t, got.Blockers, 1)
	assert.Nil(t, got.Blockers[0].Key)
}

func TestThePublishStatusMapsEveryField(t *testing.T) {
	assert.Equal(t, &model.HubrisePublishStatus{}, toHubrisePublishStatus(nil), "no status is a status of nothing")
	at := time.Now()
	msg := "boom"
	got := toHubrisePublishStatus(&hubriseApplication.PublishStatus{
		Enabled: true, Active: true, Pending: true, PendingSince: &at, NextPublishAt: &at, Publishing: true,
		LastPublishedAt: &at, FirstPublishConfirmedAt: &at, AwaitingFirstPublish: true, LastError: &msg,
		LastErrorCode: hubriseDomain.PublishRejected, LastErrorAt: &at, ExternalChangeAt: &at,
		NeedsReconnectForWrite: true, AccountCatalogDetected: true, PublishImages: true, PublishedProducts: 3, PendingProducts: 2,
	})
	code := model.HubrisePublishErrorCodeRejected
	assert.Equal(t, &model.HubrisePublishStatus{
		Enabled: true, Active: true, Pending: true, PendingSince: &at, NextPublishAt: &at, Publishing: true,
		LastPublishedAt: &at, FirstPublishConfirmedAt: &at, AwaitingFirstPublish: true, LastError: &msg,
		LastErrorCode: &code, LastErrorAt: &at, ExternalChangeAt: &at,
		NeedsReconnectForWrite: true, AccountCatalogDetected: true, PublishImages: true, PublishedProducts: 3, PendingProducts: 2,
	}, got)
	assert.Equal(t, model.HubriseCatalogDirectionNone, toHubriseDirection(""))
	assert.Equal(t, model.HubriseCatalogDirectionPublish, toHubriseDirection(hubriseDomain.DirectionPublish))
}

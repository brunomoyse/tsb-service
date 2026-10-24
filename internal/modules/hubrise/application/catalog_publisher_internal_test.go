package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/domain"
)

var errLinkBoom = errors.New("link boom")

// recordingLinks records the link writes of the publisher and fails the one it is told to.
type recordingLinks struct {
	domain.CatalogLinkRepository
	calls []string
	fail  string
}

func (r *recordingLinks) note(call string) error {
	r.calls = append(r.calls, call)
	if r.fail == call {
		return errLinkBoom
	}
	return nil
}

func (r *recordingLinks) SetRetired(_ context.Context, _ int64, at *time.Time) error {
	if at != nil {
		return errors.New("a revival must clear the retirement")
	}
	return r.note("unretire")
}

func (r *recordingLinks) Rekey(context.Context, int64, string, string) error { return r.note("rekey") }

func (r *recordingLinks) Upsert(_ context.Context, l *domain.CatalogLink) error {
	if l.Origin != domain.OriginPublished {
		return errors.New("a generated link is a published link")
	}
	return r.note("create")
}

type recordingTx struct {
	domain.CatalogTx
	links *recordingLinks
}

func (t recordingTx) Links() domain.CatalogLinkRepository { return t.links }

type recordingLocal struct {
	domain.LocalCatalogRepository
	links *recordingLinks
	batch int
}

func (l *recordingLocal) InBatch(ctx context.Context, fn func(context.Context, domain.CatalogTx) error) error {
	l.batch++
	return fn(ctx, recordingTx{links: l.links})
}

// The links of a plan are written revivals first, then rekeys, then new links, in one batch, and the
// first failure stops the batch (the transaction rolls back) before anything reaches HubRise.
func TestTheLinksOfAPlanAreWrittenInOrderAndTheFirstFailureStops(t *testing.T) {
	plan := &PublishPlan{
		Unretire: []int64{1},
		Rekeys:   []PlannedRekey{{LinkID: 2, NewKey: "k", NewRef: "r"}},
		NewLinks: []PlannedLink{{Entity: domain.EntityProduct, Key: "P", Ref: "P"}},
	}
	for _, tc := range []struct {
		fail string
		want []string
	}{
		{"", []string{"unretire", "rekey", "create"}},
		{"unretire", []string{"unretire"}},
		{"rekey", []string{"unretire", "rekey"}},
		{"create", []string{"unretire", "rekey", "create"}},
	} {
		links := &recordingLinks{fail: tc.fail}
		local := &recordingLocal{links: links}
		p := &CatalogPublisher{Local: local}
		err := p.writeLinks(t.Context(), plan)
		if tc.fail == "" {
			require.NoError(t, err)
		} else {
			assert.ErrorIs(t, err, errLinkBoom, tc.fail)
		}
		assert.Equal(t, tc.want, links.calls, "fail on %q", tc.fail)
		assert.Equal(t, 1, local.batch)
	}

	t.Run("nothing to write is no transaction", func(t *testing.T) {
		local := &recordingLocal{links: &recordingLinks{}}
		p := &CatalogPublisher{Local: local}
		require.NoError(t, p.writeLinks(t.Context(), &PublishPlan{}))
		assert.Zero(t, local.batch)
	})
}

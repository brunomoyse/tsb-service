package scaleway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// recordingStore is a SuppressionStore recording Suppress calls.
type recordingStore struct {
	mu          sync.Mutex
	suppressed  map[string]string // email -> reason
	failFor     map[string]bool
	lookupErr   error
	lookupCalls []string
}

func newRecordingStore() *recordingStore {
	return &recordingStore{suppressed: map[string]string{}, failFor: map[string]bool{}}
}

func (s *recordingStore) IsSuppressed(_ context.Context, email string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookupCalls = append(s.lookupCalls, email)
	if s.lookupErr != nil {
		return false, s.lookupErr
	}
	_, ok := s.suppressed[email]
	return ok, nil
}

func (s *recordingStore) Suppress(_ context.Context, email, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failFor[email] {
		return errors.New("db down")
	}
	s.suppressed[email] = reason
	return nil
}

type temEmail struct {
	Rcpt  string
	Flags []temv1alpha1.EmailFlag
}

func listResponse(w http.ResponseWriter, emails []temEmail) {
	type item struct {
		MailRcpt string   `json:"mail_rcpt"`
		Flags    []string `json:"flags"`
	}
	out := struct {
		TotalCount int    `json:"total_count"`
		Emails     []item `json:"emails"`
	}{TotalCount: len(emails)}
	for _, e := range emails {
		it := item{MailRcpt: e.Rcpt}
		for _, f := range e.Flags {
			it.Flags = append(it.Flags, string(f))
		}
		out.Emails = append(out.Emails, it)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func TestPollHardBouncesIsNoOpWithoutBackendOrStore(t *testing.T) {
	t.Run("SMTP backend (no TEM client)", func(t *testing.T) {
		isolateGlobals(t)
		baseReq = testBaseReq()
		suppressionStore = newRecordingStore()
		require.NoError(t, PollHardBounces(t.Context(), time.Now()))
	})

	t.Run("no suppression store", func(t *testing.T) {
		f := startFakeTEM(t, temOK)
		suppressionStore = nil
		require.NoError(t, PollHardBounces(t.Context(), time.Now()))
		require.Empty(t, f.requests(), "must not even call Scaleway")
	})
}

func TestPollHardBouncesSuppressesOnlyPermanentFailures(t *testing.T) {
	f := startFakeTEM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		listResponse(w, []temEmail{
			{"Bad@Example.com ", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagSoftBounce, temv1alpha1.EmailFlagMailboxNotFound}},
			{"hard@example.com", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagHardBounce}},
			{"blocked@example.com", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagBlocklisted}},
			{"soft@example.com", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagSoftBounce}},
			{"full@example.com", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagMailboxFull}},
			{"", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagHardBounce}},
			{"noflags@example.com", nil},
		})
	})
	store := newRecordingStore()
	suppressionStore = store

	core, logs := observer.New(zap.InfoLevel)
	defer zap.ReplaceGlobals(zap.New(core))()

	since := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, PollHardBounces(t.Context(), since))

	require.Equal(t, map[string]string{
		"bad@example.com":     "mailbox_not_found", // normalized; first hard flag wins over soft_bounce
		"hard@example.com":    "hard_bounce",
		"blocked@example.com": "blocklisted",
	}, store.suppressed)

	reqs := f.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	require.Equal(t, http.MethodGet, r.Method)
	require.Equal(t, "/transactional-email/v1alpha1/regions/fr-par/emails", r.Path)
	require.Equal(t, []string{"11111111-2222-4333-8444-555555555555"}, r.Query["project_id"])
	require.Equal(t, []string{"failed"}, r.Query["statuses"])
	require.Equal(t, []string{"1"}, r.Query["page"])
	require.Equal(t, []string{"100"}, r.Query["page_size"])
	require.Equal(t, []string{since.Format(time.RFC3339)}, r.Query["since"])
	require.NotEmpty(t, r.Token)

	summary := logs.FilterMessage("recorded email suppressions from hard bounces").All()
	require.Len(t, summary, 1)
	require.Equal(t, int64(3), summary[0].ContextMap()["count"])
}

func TestPollHardBouncesPaginates(t *testing.T) {
	// Page 1 is full (100 rows), so the poller must ask for page 2.
	f := startFakeTEM(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		var emails []temEmail
		switch r.URL.Query().Get("page") {
		case "1":
			for i := range 100 {
				emails = append(emails, temEmail{fmt.Sprintf("p1-%d@example.com", i), []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagHardBounce}})
			}
		case "2":
			emails = []temEmail{{"p2@example.com", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagHardBounce}}}
		}
		listResponse(w, emails)
	})
	store := newRecordingStore()
	suppressionStore = store

	require.NoError(t, PollHardBounces(t.Context(), time.Now()))

	reqs := f.requests()
	require.Len(t, reqs, 2)
	require.Equal(t, []string{"1"}, reqs[0].Query["page"])
	require.Equal(t, []string{"2"}, reqs[1].Query["page"])
	require.Len(t, store.suppressed, 101)
	require.Contains(t, store.suppressed, "p2@example.com")
}

func TestPollHardBouncesKeepsGoingWhenOneSuppressFails(t *testing.T) {
	startFakeTEM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		listResponse(w, []temEmail{
			{"fail@example.com", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagHardBounce}},
			{"ok@example.com", []temv1alpha1.EmailFlag{temv1alpha1.EmailFlagHardBounce}},
		})
	})
	store := newRecordingStore()
	store.failFor["fail@example.com"] = true
	suppressionStore = store

	core, logs := observer.New(zap.WarnLevel)
	defer zap.ReplaceGlobals(zap.New(core))()

	require.NoError(t, PollHardBounces(t.Context(), time.Now()))
	require.Equal(t, map[string]string{"ok@example.com": "hard_bounce"}, store.suppressed)
	require.Len(t, logs.FilterMessage("failed to record email suppression").All(), 1)
}

func TestPollHardBouncesListError(t *testing.T) {
	startFakeTEM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"denied_authentication","message":"insufficient permissions"}`))
	})
	store := newRecordingStore()
	suppressionStore = store

	err := PollHardBounces(t.Context(), time.Now())
	require.ErrorContains(t, err, "list bounced emails")
	require.Empty(t, store.suppressed)
}

func TestPollHardBouncesHonoursContext(t *testing.T) {
	startFakeTEM(t, temOK)
	suppressionStore = newRecordingStore()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorContains(t, PollHardBounces(ctx, time.Now()), "list bounced emails")
}

func TestPollHardBouncesSkipsNilEmailRows(t *testing.T) {
	startFakeTEM(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"emails":[null,{"mail_rcpt":"x@example.com","flags":["hard_bounce"]}]}`))
	})
	store := newRecordingStore()
	suppressionStore = store
	require.NoError(t, PollHardBounces(t.Context(), time.Now()))
	require.Equal(t, map[string]string{"x@example.com": "hard_bounce"}, store.suppressed)
}

func TestHardBounceReasonDefaultsToHardBounce(t *testing.T) {
	require.Equal(t, "hard_bounce", hardBounceReason(nil))
	require.Equal(t, "hard_bounce", hardBounceReason([]temv1alpha1.EmailFlag{temv1alpha1.EmailFlagSpam}))
}

package application

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/user/domain"
)

// recordingObserver logs what the user service tells it, in order, into a log it may share with the
// fakes of the service so the relative order of events can be asserted.
type recordingObserver struct {
	log       *[]string
	changed   []uuid.UUID
	deleted   []uuid.UUID
	changeErr error
	deleteErr error
	panics    bool
}

func (o *recordingObserver) ProfileChanged(_ context.Context, id uuid.UUID) error {
	*o.log = append(*o.log, "changed")
	o.changed = append(o.changed, id)
	if o.panics {
		panic("observer bug")
	}
	return o.changeErr
}

func (o *recordingObserver) AccountDeleted(_ context.Context, id uuid.UUID) error {
	*o.log = append(*o.log, "deleted")
	o.deleted = append(o.deleted, id)
	if o.panics {
		panic("observer bug")
	}
	return o.deleteErr
}

// orderedRepo logs the anonymisation into the shared log.
type orderedRepo struct {
	*jitRepo
	log *[]string
}

func (r orderedRepo) AnonymizeForDeletion(ctx context.Context, id string) error {
	*r.log = append(*r.log, "anonymised")
	return r.jitRepo.AnonymizeForDeletion(ctx, id)
}

type orderedFetcher struct {
	*fakeFetcher
	log *[]string
}

func (f orderedFetcher) DeleteUser(ctx context.Context, id string) error {
	*f.log = append(*f.log, "identity removed")
	return f.fakeFetcher.DeleteUser(ctx, id)
}

func observedUser() *domain.User {
	return &domain.User{
		ID: uuid.New(), FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com",
		PhoneNumber: strp("+32470000000"), ZitadelUserID: strp("sub-1"),
	}
}

func TestUpdateMeTellsTheObserverOnlyAboutProfileChanges(t *testing.T) {
	same := func(s string) *string { return &s }
	cases := []struct {
		name    string
		first   *string
		last    *string
		email   *string
		phone   *string
		place   *string
		marketN *bool
		want    int
	}{
		{name: "nothing sent", want: 0},
		{name: "the same values", first: same("Ada"), last: same("Lovelace"), email: same(" ADA@example.com "), phone: same("0470 00 00 00"), want: 0},
		{name: "a new first name", first: same("Grace"), want: 1},
		{name: "a new last name", last: same("Hopper"), want: 1},
		{name: "a new email", email: same("grace@example.com"), want: 1},
		{name: "a new phone", phone: same("0470 12 34 56"), want: 1},
		{name: "a cleared phone", phone: same(""), want: 1},
		{name: "only the default address", place: same("place-1"), want: 0},
		{name: "only the marketing preference", marketN: func() *bool { b := true; return &b }(), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := observedUser()
			svc, _ := newUpdateEnv(u)
			var log []string
			obs := &recordingObserver{log: &log}
			svc.SetProfileObserver(obs)

			_, err := svc.UpdateMe(t.Context(), u.ID.String(), tc.first, tc.last, tc.email, tc.phone, tc.place, tc.marketN, nil)
			require.NoError(t, err)
			require.Len(t, obs.changed, tc.want)
			for _, id := range obs.changed {
				assert.Equal(t, u.ID, id)
			}
			assert.Empty(t, obs.deleted)
		})
	}
}

func TestAnObserverNeverFailsOrBlocksAChange(t *testing.T) {
	t.Run("an error is logged and the change is kept", func(t *testing.T) {
		u := observedUser()
		svc, repo := newUpdateEnv(u)
		var log []string
		svc.SetProfileObserver(&recordingObserver{log: &log, changeErr: errors.New("outbox down")})
		first := "Grace"
		got, err := svc.UpdateMe(t.Context(), u.ID.String(), &first, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "Grace", got.FirstName)
		assert.Equal(t, 1, repo.updated)
	})

	t.Run("a panic is recovered", func(t *testing.T) {
		u := observedUser()
		svc, _ := newUpdateEnv(u)
		var log []string
		svc.SetProfileObserver(&recordingObserver{log: &log, panics: true})
		first := "Grace"
		got, err := svc.UpdateMe(t.Context(), u.ID.String(), &first, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "Grace", got.FirstName)
	})

	t.Run("without an observer, and after removing it, nothing is called", func(t *testing.T) {
		u := observedUser()
		svc, _ := newUpdateEnv(u)
		first := "Grace"
		_, err := svc.UpdateMe(t.Context(), u.ID.String(), &first, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)

		var log []string
		obs := &recordingObserver{log: &log}
		svc.SetProfileObserver(obs)
		svc.SetProfileObserver(nil)
		last := "Hopper"
		_, err = svc.UpdateMe(t.Context(), u.ID.String(), nil, &last, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		assert.Empty(t, log)
	})

	t.Run("a refused update tells nobody", func(t *testing.T) {
		u := observedUser()
		svc, _ := newUpdateEnv(u)
		var log []string
		svc.SetProfileObserver(&recordingObserver{log: &log})
		bad := "12"
		first := "Grace"
		_, err := svc.UpdateMe(t.Context(), u.ID.String(), &first, nil, nil, &bad, nil, nil, nil)
		assert.ErrorIs(t, err, ErrInvalidPhoneNumber)
		assert.Empty(t, log)
	})
}

func TestDeleteMeTellsTheObserverAfterTheIdentityAndBeforeTheErasure(t *testing.T) {
	u := observedUser()
	var log []string
	repo := &jitRepo{users: []*domain.User{u}}
	svc := NewUserService(orderedRepo{repo, &log}, orderedFetcher{&fakeFetcher{}, &log})
	obs := &recordingObserver{log: &log}
	svc.SetProfileObserver(obs)

	require.NoError(t, svc.DeleteMe(t.Context(), u.ID.String()))
	assert.Equal(t, []string{"identity removed", "deleted", "anonymised"}, log)
	assert.Equal(t, []uuid.UUID{u.ID}, obs.deleted)
}

func TestDeleteMeIsNotBlockedByTheObserver(t *testing.T) {
	for name, obs := range map[string]*recordingObserver{
		"an error": {deleteErr: errors.New("outbox down")},
		"a panic":  {panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			u := observedUser()
			var log []string
			obs.log = &log
			repo := &jitRepo{users: []*domain.User{u}}
			svc := NewUserService(orderedRepo{repo, &log}, &fakeFetcher{})
			svc.SetProfileObserver(obs)
			require.NoError(t, svc.DeleteMe(t.Context(), u.ID.String()))
			assert.Equal(t, []string{u.ID.String()}, repo.anonIDs, "the account is erased all the same")
		})
	}
}

func TestDeleteMeTellsNobodyWhenTheIdentityCannotBeRemoved(t *testing.T) {
	u := observedUser()
	var log []string
	repo := &jitRepo{users: []*domain.User{u}}
	svc := NewUserService(repo, &fakeFetcher{deleteErr: errors.New("zitadel down")})
	svc.SetProfileObserver(&recordingObserver{log: &log})
	assert.Error(t, svc.DeleteMe(t.Context(), u.ID.String()))
	assert.Empty(t, log)
}

func TestTheBackfillOfAProfileIsReportedToTheObserver(t *testing.T) {
	t.Run("a blank profile filled from Zitadel", func(t *testing.T) {
		repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), ZitadelUserID: strp("sub-1")}}}
		svc := NewUserService(repo, &fakeFetcher{email: "ann@example.com", given: "Ann", family: "Lee"})
		var log []string
		obs := &recordingObserver{log: &log}
		svc.SetProfileObserver(obs)
		_, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "", "", "")
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{repo.users[0].ID}, obs.changed)
	})

	t.Run("a migrated row adopted and given its names", func(t *testing.T) {
		repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), Email: "old@example.com"}}}
		svc := NewUserService(repo, nil)
		var log []string
		obs := &recordingObserver{log: &log}
		svc.SetProfileObserver(obs)
		_, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "OLD@example.com", "Ann", "Lee")
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{repo.users[0].ID}, obs.changed)
	})

	t.Run("linking a complete row changes no profile field", func(t *testing.T) {
		repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), Email: "old@example.com", FirstName: "A", LastName: "B"}}}
		svc := NewUserService(repo, nil)
		var log []string
		obs := &recordingObserver{log: &log}
		svc.SetProfileObserver(obs)
		_, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "old@example.com", "Ann", "Lee")
		require.NoError(t, err)
		assert.Empty(t, obs.changed)
	})
}

type failingUpdateRepo struct {
	*jitRepo
}

func (r failingUpdateRepo) UpdateUser(context.Context, *domain.User) (*domain.User, error) {
	return nil, errors.New("database down")
}

func TestAFailedUpdateTellsNobody(t *testing.T) {
	u := observedUser()
	var log []string
	svc := NewUserService(failingUpdateRepo{&jitRepo{users: []*domain.User{u}}}, nil)
	svc.SetProfileObserver(&recordingObserver{log: &log})
	first := "Grace"
	_, err := svc.UpdateMe(t.Context(), u.ID.String(), &first, nil, nil, nil, nil, nil, nil)
	require.Error(t, err)
	assert.Empty(t, log)
}

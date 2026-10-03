package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"

	"tsb-service/internal/modules/user/domain"
)

// jitRepo is an in-memory UserRepository for the JIT provisioning path with
// injectable failures.
type jitRepo struct {
	domain.UserRepository
	users     []*domain.User
	saved     []*domain.User
	updated   int
	saveErr   error
	saveRace  func(r *jitRepo) // runs just before a failing save, to simulate the winner of a race
	anonymize error
	anonIDs   []string
}

func (r *jitRepo) FindByZitadelID(_ context.Context, id string) (*domain.User, error) {
	for _, u := range r.users {
		if u.ZitadelUserID != nil && *u.ZitadelUserID == id {
			cp := *u
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (r *jitRepo) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, u := range r.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (r *jitRepo) FindByID(_ context.Context, id string) (*domain.User, error) {
	for _, u := range r.users {
		if u.ID.String() == id {
			cp := *u
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (r *jitRepo) Save(_ context.Context, u *domain.User) (uuid.UUID, error) {
	if r.saveErr != nil {
		if r.saveRace != nil {
			r.saveRace(r)
		}
		return uuid.Nil, r.saveErr
	}
	u.ID = uuid.New()
	cp := *u
	r.users = append(r.users, &cp)
	r.saved = append(r.saved, &cp)
	return u.ID, nil
}

func (r *jitRepo) UpdateUser(_ context.Context, u *domain.User) (*domain.User, error) {
	r.updated++
	for i, existing := range r.users {
		if existing.ID == u.ID {
			cp := *u
			r.users[i] = &cp
		}
	}
	return u, nil
}

func (r *jitRepo) AnonymizeForDeletion(_ context.Context, id string) error {
	r.anonIDs = append(r.anonIDs, id)
	return r.anonymize
}

type fakeFetcher struct {
	email, given, family string
	err                  error
	calls                int
	deleted              []string
	deleteErr            error
}

func (f *fakeFetcher) FetchUserInfo(context.Context, string) (string, string, string, error) {
	f.calls++
	return f.email, f.given, f.family, f.err
}

func (f *fakeFetcher) DeleteUser(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func strp(s string) *string { return &s }

func TestJIT_NewCustomerFirstLogin(t *testing.T) {
	repo := &jitRepo{}
	svc := NewUserService(repo, &fakeFetcher{})

	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "  Ann@Example.COM ", "Ann", "Lee")
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d users, want 1", len(repo.saved))
	}
	got := repo.saved[0]
	if got.Email != "ann@example.com" || got.FirstName != "Ann" || got.LastName != "Lee" {
		t.Fatalf("saved %+v", got)
	}
	if got.ZitadelUserID == nil || *got.ZitadelUserID != "sub-1" {
		t.Fatalf("zitadel id not stored: %v", got.ZitadelUserID)
	}
	if u.ID == uuid.Nil {
		t.Fatal("returned user has no ID")
	}
}

func TestJIT_ReturningCustomerIsNotDuplicated(t *testing.T) {
	repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), Email: "a@example.com", FirstName: "A", LastName: "B", ZitadelUserID: strp("sub-1")}}}
	f := &fakeFetcher{}
	svc := NewUserService(repo, f)

	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != repo.users[0].ID || len(repo.saved) != 0 || repo.updated != 0 {
		t.Fatalf("returning customer must be returned untouched (saved=%d updated=%d)", len(repo.saved), repo.updated)
	}
	if f.calls != 0 {
		t.Fatal("complete profile must not trigger a Zitadel fetch")
	}
}

func TestJIT_BackfillsBlankProfileFromZitadel(t *testing.T) {
	repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), ZitadelUserID: strp("sub-1")}}}
	svc := NewUserService(repo, &fakeFetcher{email: "Apple@Example.com", given: "Ann", family: "Lee"})

	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "apple@example.com" || u.FirstName != "Ann" || u.LastName != "Lee" {
		t.Fatalf("not backfilled: %+v", u)
	}
	if repo.updated != 1 {
		t.Fatalf("updated %d times, want 1", repo.updated)
	}
}

func TestJIT_BackfillKeepsExistingFieldsAndSkipsNoOpUpdate(t *testing.T) {
	repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), Email: "keep@example.com", FirstName: "Keep", ZitadelUserID: strp("sub-1")}}}
	// Zitadel only knows the same email, nothing new for the blank last name.
	svc := NewUserService(repo, &fakeFetcher{email: "other@example.com"})

	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "keep@example.com" || u.FirstName != "Keep" {
		t.Fatalf("existing fields overwritten: %+v", u)
	}
	if repo.updated != 0 {
		t.Fatal("nothing to backfill, must not write")
	}
}

func TestJIT_EnrichesNewUserFromZitadel(t *testing.T) {
	repo := &jitRepo{}
	svc := NewUserService(repo, &fakeFetcher{email: "ann@example.com", given: "Ann", family: "Lee"})

	if _, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "", "", ""); err != nil {
		t.Fatal(err)
	}
	got := repo.saved[0]
	if got.Email != "ann@example.com" || got.FirstName != "Ann" || got.LastName != "Lee" {
		t.Fatalf("saved %+v", got)
	}
}

func TestJIT_TokenClaimsWinOverZitadelProfile(t *testing.T) {
	repo := &jitRepo{}
	svc := NewUserService(repo, &fakeFetcher{email: "z@example.com", given: "Zed", family: "Zed"})

	if _, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "claim@example.com", "Claim", ""); err != nil {
		t.Fatal(err)
	}
	got := repo.saved[0]
	if got.Email != "claim@example.com" || got.FirstName != "Claim" || got.LastName != "Zed" {
		t.Fatalf("saved %+v", got)
	}
}

func TestJIT_ZitadelFetchFailureStillCreatesUser(t *testing.T) {
	repo := &jitRepo{}
	svc := NewUserService(repo, &fakeFetcher{err: errors.New("zitadel down")})

	if _, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "ann@example.com", "", ""); err != nil {
		t.Fatalf("login must not fail on a profile fetch error: %v", err)
	}
	if len(repo.saved) != 1 || repo.saved[0].Email != "ann@example.com" {
		t.Fatalf("saved %+v", repo.saved)
	}
}

func TestJIT_NilFetcherCreatesFromClaims(t *testing.T) {
	repo := &jitRepo{}
	svc := NewUserService(repo, nil)

	if _, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "ann@example.com", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(repo.saved) != 1 {
		t.Fatal("user not created")
	}
}

func TestJIT_MigratedRowFillsBlankNames(t *testing.T) {
	repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), Email: "old@example.com"}}}
	svc := NewUserService(repo, nil)

	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "OLD@example.com", "Ann", "Lee")
	if err != nil {
		t.Fatal(err)
	}
	if u.FirstName != "Ann" || u.LastName != "Lee" || u.ZitadelUserID == nil || *u.ZitadelUserID != "sub-1" {
		t.Fatalf("not adopted: %+v", u)
	}
	if len(repo.saved) != 0 {
		t.Fatal("must adopt the row, not create a second account")
	}
}

func TestJIT_SameSubLinkedRowByEmailIsReused(t *testing.T) {
	// Row already bound to this very sub (found by email because the sub lookup
	// raced): reuse, no conflict.
	repo := &jitRepo{users: []*domain.User{{ID: uuid.New(), Email: "a@example.com", FirstName: "A", LastName: "B", ZitadelUserID: strp("sub-1")}}}
	// Hide it from the sub lookup once, so it is found through the email.
	svc := NewUserService(&missOnceRepo{jitRepo: repo}, nil)

	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "a@example.com", "A", "B")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != repo.users[0].ID {
		t.Fatal("wrong row")
	}
}

type missOnceRepo struct {
	*jitRepo
	missed bool
}

func (r *missOnceRepo) FindByZitadelID(ctx context.Context, id string) (*domain.User, error) {
	if !r.missed {
		r.missed = true
		return nil, sql.ErrNoRows
	}
	return r.jitRepo.FindByZitadelID(ctx, id)
}

func TestJIT_ConcurrentFirstLoginRecoversByZitadelID(t *testing.T) {
	repo := &jitRepo{saveErr: domain.ErrDuplicateUser}
	winner := &domain.User{ID: uuid.New(), Email: "ann@example.com", FirstName: "Ann", LastName: "Lee", ZitadelUserID: strp("sub-1")}
	repo.saveRace = func(r *jitRepo) { r.users = append(r.users, winner) }
	svc := NewUserService(repo, nil)

	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "ann@example.com", "Ann", "Lee")
	if err != nil {
		t.Fatalf("a lost race must not fail the login: %v", err)
	}
	if u.ID != winner.ID {
		t.Fatal("did not return the row the winner created")
	}
}

func TestJIT_ConcurrentFirstLoginRecoversByEmail(t *testing.T) {
	repo := &jitRepo{saveErr: domain.ErrDuplicateUser}
	winner := &domain.User{ID: uuid.New(), Email: "ann@example.com"}
	repo.saveRace = func(r *jitRepo) { r.users = append(r.users, winner) }
	svc := NewUserService(repo, nil)

	// The first FindByEmail (before save) happens before the winner exists.
	u, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "ann@example.com", "Ann", "Lee")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != winner.ID {
		t.Fatal("did not return the existing row")
	}
}

func TestJIT_DuplicateEmailOwnedByAnotherIdentityIsAConflict(t *testing.T) {
	repo := &jitRepo{saveErr: domain.ErrDuplicateUser}
	repo.saveRace = func(r *jitRepo) {
		r.users = append(r.users, &domain.User{ID: uuid.New(), Email: "ann@example.com", ZitadelUserID: strp("someone-else")})
	}
	svc := NewUserService(repo, nil)

	_, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "ann@example.com", "Ann", "Lee")
	if !errors.Is(err, domain.ErrIdentityConflict) {
		t.Fatalf("err = %v, want ErrIdentityConflict", err)
	}
}

func TestJIT_DuplicateWithNothingToRecoverFails(t *testing.T) {
	repo := &jitRepo{saveErr: domain.ErrDuplicateUser}
	svc := NewUserService(repo, nil)

	if _, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "", "", ""); err == nil {
		t.Fatal("want an error when the duplicate row cannot be found")
	}
}

func TestJIT_SaveErrorIsReturned(t *testing.T) {
	boom := errors.New("db down")
	repo := &jitRepo{saveErr: boom}
	svc := NewUserService(repo, nil)

	_, err := svc.FindOrCreateByZitadelID(t.Context(), "sub-1", "ann@example.com", "A", "B")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
}

func TestResolveZitadelID(t *testing.T) {
	repo := &jitRepo{}
	svc := NewUserService(repo, nil)

	id, err := svc.ResolveZitadelID(t.Context(), "sub-1", "ann@example.com", "A", "B")
	if err != nil {
		t.Fatal(err)
	}
	if id != repo.saved[0].ID.String() {
		t.Fatalf("id = %s, want %s", id, repo.saved[0].ID)
	}

	repo.users = []*domain.User{{ID: uuid.New(), Email: "v@example.com", ZitadelUserID: strp("victim")}}
	if _, err := svc.ResolveZitadelID(t.Context(), "attacker", "v@example.com", "", ""); !errors.Is(err, domain.ErrIdentityConflict) {
		t.Fatalf("err = %v, want ErrIdentityConflict", err)
	}
}

func TestDeleteMe(t *testing.T) {
	id := uuid.New()
	newRepo := func() *jitRepo {
		return &jitRepo{users: []*domain.User{{ID: id, Email: "a@example.com", ZitadelUserID: strp("sub-1")}}}
	}

	t.Run("removes identity then anonymises", func(t *testing.T) {
		repo, f := newRepo(), &fakeFetcher{}
		if err := NewUserService(repo, f).DeleteMe(t.Context(), id.String()); err != nil {
			t.Fatal(err)
		}
		if len(f.deleted) != 1 || f.deleted[0] != "sub-1" || len(repo.anonIDs) != 1 {
			t.Fatalf("deleted=%v anon=%v", f.deleted, repo.anonIDs)
		}
	})
	t.Run("zitadel failure leaves the row intact", func(t *testing.T) {
		repo, f := newRepo(), &fakeFetcher{deleteErr: errors.New("down")}
		if err := NewUserService(repo, f).DeleteMe(t.Context(), id.String()); err == nil {
			t.Fatal("want error")
		}
		if len(repo.anonIDs) != 0 {
			t.Fatal("PII must not be wiped while the identity can still log in")
		}
	})
	t.Run("anonymise failure is reported", func(t *testing.T) {
		repo := newRepo()
		repo.anonymize = errors.New("db")
		if err := NewUserService(repo, &fakeFetcher{}).DeleteMe(t.Context(), id.String()); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("unknown user", func(t *testing.T) {
		if err := NewUserService(&jitRepo{}, &fakeFetcher{}).DeleteMe(t.Context(), id.String()); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("user without identity skips zitadel", func(t *testing.T) {
		repo := &jitRepo{users: []*domain.User{{ID: id}}}
		f := &fakeFetcher{}
		if err := NewUserService(repo, f).DeleteMe(t.Context(), id.String()); err != nil {
			t.Fatal(err)
		}
		if len(f.deleted) != 0 {
			t.Fatal("no identity to delete")
		}
	})
}

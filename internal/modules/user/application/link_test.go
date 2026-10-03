package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"

	"tsb-service/internal/modules/user/domain"
)

// linkRepo is a minimal in-memory UserRepository for the JIT linking path.
type linkRepo struct {
	domain.UserRepository
	users []*domain.User
}

func (r *linkRepo) FindByZitadelID(_ context.Context, id string) (*domain.User, error) {
	for _, u := range r.users {
		if u.ZitadelUserID != nil && *u.ZitadelUserID == id {
			cp := *u
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (r *linkRepo) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, u := range r.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (r *linkRepo) UpdateUser(_ context.Context, u *domain.User) (*domain.User, error) {
	for i, existing := range r.users {
		if existing.ID == u.ID {
			cp := *u
			r.users[i] = &cp
		}
	}
	return u, nil
}

func TestFindOrCreateByZitadelID_EmailLinking(t *testing.T) {
	sub := func(s string) *string { return &s }

	t.Run("unlinked migrated row is adopted", func(t *testing.T) {
		repo := &linkRepo{users: []*domain.User{{ID: uuid.New(), Email: "a@example.com", FirstName: "A", LastName: "B"}}}
		svc := NewUserService(repo, nil)
		u, err := svc.FindOrCreateByZitadelID(context.Background(), "sub-new", "a@example.com", "A", "B")
		if err != nil {
			t.Fatalf("link: %v", err)
		}
		if u.ZitadelUserID == nil || *u.ZitadelUserID != "sub-new" {
			t.Fatalf("row not linked: %v", u.ZitadelUserID)
		}
	})

	t.Run("row linked to another sub is never taken over", func(t *testing.T) {
		victim := &domain.User{ID: uuid.New(), Email: "victim@example.com", FirstName: "V", LastName: "V", ZitadelUserID: sub("sub-victim")}
		repo := &linkRepo{users: []*domain.User{victim}}
		svc := NewUserService(repo, nil)
		_, err := svc.FindOrCreateByZitadelID(context.Background(), "sub-attacker", "victim@example.com", "X", "Y")
		if !errors.Is(err, domain.ErrIdentityConflict) {
			t.Fatalf("err = %v, want ErrIdentityConflict", err)
		}
		if *repo.users[0].ZitadelUserID != "sub-victim" {
			t.Fatalf("victim row relinked to %s", *repo.users[0].ZitadelUserID)
		}
	})
}

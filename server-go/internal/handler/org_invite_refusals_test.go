package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type memberOrgStore struct {
	storage.OrgStore
	existing map[string]bool
	lookup   error // a failure other than "not a member"
	added    []string
	invites  int
}

func (s *memberOrgStore) GetOrgMember(_ context.Context, _, userID string) (*domain.OrgMember, error) {
	if s.lookup != nil {
		return nil, s.lookup
	}
	if s.existing[userID] {
		return &domain.OrgMember{UserID: userID, Role: domain.OrgRoleDeveloper}, nil
	}
	// As the Postgres store answers for someone who is not a member.
	return nil, fmt.Errorf("org member not found: %w", sql.ErrNoRows)
}

func (s *memberOrgStore) AddOrgMember(_ context.Context, member *domain.OrgMember) error {
	s.added = append(s.added, member.UserID)
	return nil
}

func (s *memberOrgStore) CreatePendingInvite(context.Context, *domain.PendingInvite) error {
	s.invites++
	return nil
}

type emailUserStore struct {
	storage.UserStore
	byEmail map[string]*domain.User
}

func (s *emailUserStore) FindUserByEmail(_ context.Context, email string) (*domain.User, error) {
	return s.byEmail[email], nil
}

// EXC-555: inviting someone already in the org answered 500 "failed to add
// member", and a malformed address got an invite link nobody could use.
func TestInviteRefusalsSayWhy(t *testing.T) {
	users := &emailUserStore{byEmail: map[string]*domain.User{"ann@x.test": {ID: "u-ann", Role: "user"}}}

	t.Run("already a member", func(t *testing.T) {
		orgs := &memberOrgStore{existing: map[string]bool{"u-ann": true}}
		w := httptest.NewRecorder()
		NewOrgHandler(orgs, users).resolveAndAddMember(w, inviteRequest(), "org1", "", "ann@x.test", "viewer")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already a member") {
			t.Fatalf("got %d %s, want 409 naming the membership", w.Code, w.Body.String())
		}
		if len(orgs.added) != 0 {
			t.Errorf("an existing member must not be re-added")
		}
	})

	t.Run("someone not yet a member is added", func(t *testing.T) {
		orgs := &memberOrgStore{}
		w := httptest.NewRecorder()
		NewOrgHandler(orgs, users).resolveAndAddMember(w, inviteRequest(), "org1", "", "ann@x.test", "viewer")
		if w.Code != http.StatusCreated || len(orgs.added) != 1 {
			t.Fatalf("got %d %s (added %v), want 201 and one new member", w.Code, w.Body.String(), orgs.added)
		}
	})

	t.Run("a membership lookup that fails is not taken for no membership", func(t *testing.T) {
		orgs := &memberOrgStore{lookup: fmt.Errorf("org member not found: %w", sql.ErrConnDone)}
		w := httptest.NewRecorder()
		NewOrgHandler(orgs, users).resolveAndAddMember(w, inviteRequest(), "org1", "", "ann@x.test", "viewer")
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "connection") {
			t.Fatalf("got %d %s, want a plain 500", w.Code, w.Body.String())
		}
		if len(orgs.added) != 0 {
			t.Errorf("nobody may be added when membership is unknown")
		}
	})

	t.Run("not an e-mail address", func(t *testing.T) {
		orgs := &memberOrgStore{}
		w := httptest.NewRecorder()
		NewOrgHandler(orgs, users).resolveAndAddMember(w, inviteRequest(), "org1", "", "not-an-address", "viewer")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "e-mail address") {
			t.Fatalf("got %d %s, want 400 naming the address", w.Code, w.Body.String())
		}
		if orgs.invites != 0 {
			t.Errorf("no invite may be created for a malformed address")
		}
	})
}

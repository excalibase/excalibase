package handler

import (
	"context"
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
	added    []string
	invites  int
}

func (s *memberOrgStore) GetOrgMember(_ context.Context, _, userID string) (*domain.OrgMember, error) {
	if s.existing[userID] {
		return &domain.OrgMember{UserID: userID, Role: domain.OrgRoleDeveloper}, nil
	}
	return nil, nil
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

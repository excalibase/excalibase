package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// memberOrgs answers only the two listings the test asks about; a call to
// anything else is a nil-interface panic, which is the point.
type memberOrgs struct {
	storage.OrgStore
	member []*domain.Org
	all    []*domain.Org
}

func (m memberOrgs) FindOrgsByUser(context.Context, string) ([]*domain.Org, error) {
	return m.member, nil
}
func (m memberOrgs) FindAllOrgs(context.Context) ([]*domain.Org, error) { return m.all, nil }

// EXC-757: the platform admin's org switcher lists the orgs they belong to;
// customer orgs are reached through the admin console.
func TestListMyOrgs_APlatformAdminSeesOnlyTheirOwnOrgs(t *testing.T) {
	mine := &domain.Org{ID: "o-mine", Slug: "mine"}
	customer := &domain.Org{ID: "o-customer", Slug: "customer"}
	h := NewOrgHandler(memberOrgs{member: []*domain.Org{mine}, all: []*domain.Org{mine, customer}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/orgs/", nil)
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "boss", Role: "platform_admin", Active: true}))
	w := httptest.NewRecorder()
	h.ListMyOrgs(w, req)

	var orgs []domain.Org
	if err := json.NewDecoder(w.Body).Decode(&orgs); err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 || orgs[0].ID != "o-mine" {
		t.Fatalf("orgs = %+v, want only the admin's own", orgs)
	}
}

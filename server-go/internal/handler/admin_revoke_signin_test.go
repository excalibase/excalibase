//go:build integration

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
)

// EXC-760: revoking an org, whatever plan it is on, must not hand its owner a
// fresh free org on the next sign-in.
func TestRevokeOrgThenSignInCreatesNoOrg(t *testing.T) {
	store := pgstore.New(t)
	ctx := t.Context()
	owner := &domain.User{
		ID: "revoked-owner", Username: testutil.FixtureToken("abuser"), Email: "abuser@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}
	if err := store.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := auth.EnsurePersonalOrg(ctx, store, owner); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	orgs, _ := store.FindOrgsByUser(ctx, owner.ID)
	if len(orgs) != 1 {
		t.Fatalf("want one personal org, got %d", len(orgs))
	}
	orgs[0].Tier = domain.Standard
	if err := store.UpdateOrg(ctx, orgs[0]); err != nil {
		t.Fatalf("upgrade org: %v", err)
	}

	h := NewAdminHandler(nil, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}, store, &captureAudit{}, nil, "", nil)
	w := httptest.NewRecorder()
	adminRouterAs("platform_admin", h).ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/admin/orgs/"+orgs[0].ID+"?cascade=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}

	if err := auth.EnsurePersonalOrg(ctx, store, owner); err != nil {
		t.Fatalf("sign-in after revoke: %v", err)
	}
	if after, _ := store.FindOrgsByUser(ctx, owner.ID); len(after) != 0 {
		t.Fatalf("sign-in after revoke created %d org(s)", len(after))
	}
}

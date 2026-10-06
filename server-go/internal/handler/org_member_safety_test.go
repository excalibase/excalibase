//go:build integration

package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// orgWithBob creates alice's org with bob as a developer member.
func orgWithBob(t *testing.T) (chi.Router, domain.Org, *inMemoryInstanceStore) {
	t.Helper()
	r, _, instances := setupOrgRouterWithInstances(t, true)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Safe","slug":"safe-org"}`, testAliceID)
	if w.Code != http.StatusCreated {
		t.Fatalf("create org: %d %s", w.Code, w.Body.String())
	}
	var org domain.Org
	_ = json.NewDecoder(w.Body).Decode(&org)
	if w := orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID); w.Code != http.StatusCreated {
		t.Fatalf("add bob: %d %s", w.Code, w.Body.String())
	}
	instances.Create(&domain.DatabaseInstance{ProjectID: "my-proj", OrgID: org.ID})
	return r, org, instances
}

// EXC-555: adding a project member answers what is wrong with the request
// rather than 500.
func TestAddProjectMember_UnknownUserIs404(t *testing.T) {
	r, org, _ := orgWithBob(t)
	w := orgRequest(r, "POST", testOrgsSlash+org.ID+testMyProjMembers, `{"userId":"no-such-user","role":"viewer"}`, testAliceID)
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d %s, want 404", w.Code, w.Body.String())
	}
}

func TestAddProjectMember_NonOrgMemberIs404(t *testing.T) {
	r, _, instances := setupOrgRouterWithInstances(t, true)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Solo","slug":"solo-org"}`, testAliceID)
	var org domain.Org
	_ = json.NewDecoder(w.Body).Decode(&org)
	instances.Create(&domain.DatabaseInstance{ProjectID: "my-proj", OrgID: org.ID})

	w = orgRequest(r, "POST", testOrgsSlash+org.ID+testMyProjMembers, `{"userId":"`+testBobID+`","role":"viewer"}`, testAliceID)
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d %s, want 404 for a user outside the org", w.Code, w.Body.String())
	}
}

func TestAddProjectMember_DuplicateIs409(t *testing.T) {
	r, org, _ := orgWithBob(t)
	body := `{"userId":"` + testBobID + `","role":"viewer"}`
	if w := orgRequest(r, "POST", testOrgsSlash+org.ID+testMyProjMembers, body, testAliceID); w.Code != http.StatusCreated {
		t.Fatalf("first add: %d %s", w.Code, w.Body.String())
	}
	if w := orgRequest(r, "POST", testOrgsSlash+org.ID+testMyProjMembers, body, testAliceID); w.Code != http.StatusConflict {
		t.Errorf("duplicate add: got %d %s, want 409", w.Code, w.Body.String())
	}
}

func TestOrgMember_LastOwnerCannotBeDemotedOrRemoved(t *testing.T) {
	r, org, _ := orgWithBob(t)
	path := testOrgsSlash + org.ID + testMembersSlash + testAliceID
	if w := orgRequest(r, "PATCH", path, `{"role":"admin"}`, testAliceID); w.Code != http.StatusConflict {
		t.Errorf("demote last owner: got %d %s, want 409", w.Code, w.Body.String())
	}
	if w := orgRequest(r, "DELETE", path, "", testAliceID); w.Code != http.StatusConflict {
		t.Errorf("remove last owner: got %d %s, want 409", w.Code, w.Body.String())
	}
}

func TestOrgMember_OneOfTwoOwnersCanBeDemoted(t *testing.T) {
	r, store, _ := setupOrgRouterWithInstances(t, true)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Two","slug":"two-owners"}`, testAliceID)
	var org domain.Org
	_ = json.NewDecoder(w.Body).Decode(&org)
	if err := store.AddOrgMember(t.Context(), &domain.OrgMember{OrgID: org.ID, UserID: testBobID, Role: domain.OrgRoleOwner}); err != nil {
		t.Fatalf("seed second owner: %v", err)
	}
	if w := orgRequest(r, "PATCH", testOrgsSlash+org.ID+testMembersSlash+testBobID, `{"role":"admin"}`, testAliceID); w.Code != http.StatusOK {
		t.Errorf("demote one of two owners: got %d %s, want 200", w.Code, w.Body.String())
	}
}

func TestOrgMember_UnknownMemberIs404(t *testing.T) {
	r, org, _ := orgWithBob(t)
	path := testOrgsSlash + org.ID + testMembersSlash + "no-such-user"
	if w := orgRequest(r, "PATCH", path, `{"role":"admin"}`, testAliceID); w.Code != http.StatusNotFound {
		t.Errorf("patch unknown member: got %d %s, want 404", w.Code, w.Body.String())
	}
	if w := orgRequest(r, "DELETE", path, "", testAliceID); w.Code != http.StatusNotFound {
		t.Errorf("remove unknown member: got %d %s, want 404", w.Code, w.Body.String())
	}
}

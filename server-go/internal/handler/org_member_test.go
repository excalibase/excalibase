//go:build integration

package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)



// TestUpdateOrgMemberRole_HappyPath: alice (owner) promotes bob from
// developer to admin. Owner has manage-members perm, so the patch lands.
func TestUpdateOrgMemberRole_HappyPath(t *testing.T) {
	r, _ := setupOrgRouter(t)

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"T","slug":"t-update"}`, testAliceID)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/orgs: got %d body=%s", w.Code, w.Body.String())
	}
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	w = orgRequest(r, "PATCH", testOrgsSlash+org.ID+testMembersSlash+testBobID,
		`{"role":"admin"}`, testAliceID)
	if w.Code != http.StatusOK {
		t.Errorf("PATCH role: got %d body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateOrgMemberRole_InvalidRole(t *testing.T) {
	r, _ := setupOrgRouter(t)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Team","slug":"team-x"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	w = orgRequest(r, "PATCH", testOrgsSlash+org.ID+testMembersSlash+testBobID,
		`{"role":"emperor"}`, testAliceID)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid role: got %d, want 400", w.Code)
	}
}

func TestUpdateOrgMemberRole_NonAdmin_Forbidden(t *testing.T) {
	r, _ := setupOrgRouter(t)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Team","slug":"team-x"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	// Bob is a developer — no manage-members permission. Should 403.
	w = orgRequest(r, "PATCH", testOrgsSlash+org.ID+testMembersSlash+testBobID,
		`{"role":"admin"}`, testBobID)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-admin patch: got %d, want 403", w.Code)
	}
}

func TestUpdateOrgMemberRole_BadJSON(t *testing.T) {
	r, _ := setupOrgRouter(t)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"T","slug":"t-bad-json"}`, testAliceID)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/orgs: got %d body=%s", w.Code, w.Body.String())
	}
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	w = orgRequest(r, "PATCH", testOrgsSlash+org.ID+testMembersSlash+testBobID,
		`not-json`, testAliceID)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json: got %d, want 400", w.Code)
	}
}

func TestRemoveOrgMember_HappyPath(t *testing.T) {
	r, _ := setupOrgRouter(t)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Team","slug":"team-x"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	w = orgRequest(r, "DELETE", testOrgsSlash+org.ID+testMembersSlash+testBobID, "", testAliceID)
	if w.Code != http.StatusOK {
		t.Errorf("DELETE member: got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRemoveOrgMember_NonAdmin_Forbidden(t *testing.T) {
	r, _ := setupOrgRouter(t)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Team","slug":"team-x"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)
	orgRequest(r, "POST", testOrgsSlash+org.ID+testMembersPath,
		`{"userId":"`+testBobID+`","role":"developer"}`, testAliceID)

	w = orgRequest(r, "DELETE", testOrgsSlash+org.ID+testMembersSlash+testBobID, "", testBobID)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-admin delete: got %d, want 403", w.Code)
	}
}

// ListPendingInvites: invite a non-existent user (by email), then list
// pending invites and expect ≥1 entry. Existing invite-by-userId path
// auto-adds the member; the email path queues a PendingInvite row.

func TestListPendingInvites_Empty(t *testing.T) {
	r, _ := setupOrgRouter(t)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Team","slug":"team-x"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	w = orgRequest(r, "GET", testOrgsSlash+org.ID+"/invites", "", testAliceID)
	if w.Code != http.StatusOK {
		t.Errorf("ListPendingInvites: got %d", w.Code)
	}
	var invites []domain.PendingInvite
	json.NewDecoder(w.Body).Decode(&invites)
	if invites == nil {
		t.Error("invites should be [] not null — handler normalises")
	}
}

func TestListPendingInvites_NonMember_404(t *testing.T) {
	r, _ := setupOrgRouter(t)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Team","slug":"team-x"}`, testAliceID)
	var org domain.Org
	json.NewDecoder(w.Body).Decode(&org)

	// Bob is not a member, not platform-admin → 404 (org existence hidden).
	w = orgRequest(r, "GET", testOrgsSlash+org.ID+"/invites", "", testBobID)
	if w.Code != http.StatusNotFound {
		t.Errorf("non-member: got %d, want 404", w.Code)
	}
}

//go:build integration

package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-553: one account owns at most one free organization.
func TestCreateOrg_RefusesASecondFreeOrg(t *testing.T) {
	r, store := setupOrgRouter(t)

	if w := orgRequest(r, "POST", testOrgsPath, `{"name":"First","slug":"first"}`, testAliceID); w.Code != http.StatusCreated {
		t.Fatalf("first org: %d %s", w.Code, w.Body.String())
	}
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Second","slug":"second"}`, testAliceID)
	if w.Code != http.StatusConflict {
		t.Fatalf("second free org: got %d, want 409. Body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "one free organization") {
		t.Errorf("the refusal must say why: %s", w.Body.String())
	}
	if org, _ := store.FindOrgBySlug(t.Context(), "second"); org != nil {
		t.Fatal("the refused org was written")
	}
	// Another account still has its own allowance.
	if w := orgRequest(r, "POST", testOrgsPath, `{"name":"Bob","slug":"bob-org"}`, testBobID); w.Code != http.StatusCreated {
		t.Fatalf("another user's first org: %d %s", w.Code, w.Body.String())
	}
}

// The creator is the org's owner the moment it exists.
func TestCreateOrg_CreatorIsOwner(t *testing.T) {
	r, store := setupOrgRouter(t)

	org := createOrgAs(t, r, "owned", testAliceID)
	member, err := store.GetOrgMember(t.Context(), org.ID, testAliceID)
	if err != nil || member.Role != domain.OrgRoleOwner {
		t.Fatalf("creator membership = %+v, %v", member, err)
	}
}

func TestCreateOrg_TakenSlugIsAConflict(t *testing.T) {
	r, _ := setupOrgRouter(t)

	createOrgAs(t, r, "shared", testAliceID)
	w := orgRequest(r, "POST", testOrgsPath, `{"name":"Shared","slug":"shared"}`, testBobID)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "slug") {
		t.Fatalf("taken slug: got %d %s", w.Code, w.Body.String())
	}
}

func TestCreateOrg_NameLength(t *testing.T) {
	r, store := setupOrgRouter(t)

	for name, body := range map[string]string{
		"blank":    `{"name":"   ","slug":"blank-name"}`,
		"too long": `{"name":"` + strings.Repeat("n", domain.MaxOrgNameLength+1) + `","slug":"long-name"}`,
	} {
		w := orgRequest(r, "POST", testOrgsPath, body, testAliceID)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "1-64 characters") {
			t.Errorf("%s: got %d %s", name, w.Code, w.Body.String())
		}
	}

	w := orgRequest(r, "POST", testOrgsPath, `{"name":"  Trimmed  ","slug":"trimmed"}`, testAliceID)
	if w.Code != http.StatusCreated {
		t.Fatalf("valid name: %d %s", w.Code, w.Body.String())
	}
	org, _ := store.FindOrgBySlug(t.Context(), "trimmed")
	if org == nil || org.Name != "Trimmed" {
		t.Fatalf("stored org = %+v", org)
	}
}

func TestUpdateOrg_NameLength(t *testing.T) {
	r, _ := setupOrgRouter(t)
	org := createOrgAs(t, r, "renamed", testAliceID)

	w := orgRequest(r, "PATCH", testOrgsSlash+org.ID, `{"name":"`+strings.Repeat("n", domain.MaxOrgNameLength+1)+`"}`, testAliceID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("too long rename: got %d %s", w.Code, w.Body.String())
	}
	w = orgRequest(r, "PATCH", testOrgsSlash+org.ID, `{"name":"  New name "}`, testAliceID)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"New name"`) {
		t.Fatalf("rename: got %d %s", w.Code, w.Body.String())
	}
}

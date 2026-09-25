//go:build integration

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

const (
	testSetupStatusPath = "/api/auth/setup-status"
)

// wireSetupStatus mounts /api/auth/setup-status on the given base router,
// sharing the store the register router was wired with so status reflects
// users created via /api/auth/register or directly.
func wireSetupStatus(t *testing.T, base chi.Router, store *pgstore.Store) chi.Router {
	t.Helper()
	h := NewAuthHandler(store, store)
	base.Get(testSetupStatusPath, h.GetSetupStatus)
	return base
}

// TestRegister_FirstUser_BecomesPlatformAdmin asserts that the very first
// registration on a fresh platform is auto-promoted to platform_admin so
// the setup wizard can complete without a separate "promote" step.
func TestRegister_FirstUser_BecomesPlatformAdmin(t *testing.T) {
	r, store, setupToken := setupRegisterRouter(t)

	body := fmt.Sprintf(`{"username":"founder","email":"founder@example.com","password":%q,"setupToken":%q}`,
		testutil.FixturePassword("founder"), setupToken)
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("register: got %d, body %s", w.Code, w.Body.String())
	}

	user, err := store.FindUserByEmail(t.Context(), "founder@example.com")
	if err != nil || user == nil {
		t.Fatalf("user not stored: %v", err)
	}
	if user.Role != "platform_admin" {
		t.Errorf("first-user role: got %q, want %q", user.Role, "platform_admin")
	}
}

// TestRegister_SecondUser_StaysAsRegularUser asserts the auto-promotion
// rule fires for the FIRST registration only — subsequent registrations
// must keep the default "user" role.
func TestRegister_SecondUser_StaysAsRegularUser(t *testing.T) {
	r, store, setupToken := setupRegisterRouter(t)

	// First registration — becomes platform_admin
	first := fmt.Sprintf(`{"username":"founder","email":"founder@example.com","password":%q,"setupToken":%q}`,
		testutil.FixturePassword("founder"), setupToken)
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(first))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first register: %d %s", w.Code, w.Body.String())
	}

	// Second registration — should stay as plain "user"
	second := fmt.Sprintf(`{"username":"newcomer","email":"newcomer@example.com","password":%q}`, testutil.FixturePassword("newcomer"))
	req2 := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(second))
	req2.Header.Set(sharedContentType, sharedMIMEJSON)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("second register: %d %s", w2.Code, w2.Body.String())
	}

	user, err := store.FindUserByEmail(t.Context(), "newcomer@example.com")
	if err != nil || user == nil {
		t.Fatalf("second user not stored: %v", err)
	}
	if user.Role != "user" {
		t.Errorf("second-user role: got %q, want %q", user.Role, "user")
	}
}

// TestRegister_FirstUser_CreatesDefaultOrg asserts that when the wizard
// auto-promotes the first registration to platform_admin, the default
// organization is also created and the admin is added as its owner —
// matching what the legacy auth.Bootstrap+BootstrapDefaultOrg pair did
// at server startup before the wizard owned first-run.
func TestRegister_FirstUser_CreatesDefaultOrg(t *testing.T) {
	r, store, setupToken := setupRegisterRouter(t)

	body := fmt.Sprintf(`{"username":"founder","email":"founder@example.com","password":%q,"setupToken":%q}`,
		testutil.FixturePassword("founder"), setupToken)
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(body))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}

	orgs, _ := store.FindAllOrgs(t.Context())
	if len(orgs) != 1 {
		t.Fatalf("expected 1 default org after first register, got %d", len(orgs))
	}
	if orgs[0].Slug != "default" {
		t.Errorf("default org slug: got %q, want %q", orgs[0].Slug, "default")
	}

	user, _ := store.FindUserByEmail(t.Context(), "founder@example.com")
	if user == nil {
		t.Fatal("user not stored")
	}
	if orgs[0].OwnerID != user.ID {
		t.Errorf("owner: got %q, want %q", orgs[0].OwnerID, user.ID)
	}

	// And admin should be a member with role=owner
	members, _ := store.ListOrgMembers(t.Context(), orgs[0].ID)
	if len(members) == 0 {
		t.Fatal("expected admin as org member")
	}
	if members[0].UserID != user.ID || string(members[0].Role) != "owner" {
		t.Errorf("admin membership: got user=%q role=%q, want user=%q role=owner",
			members[0].UserID, members[0].Role, user.ID)
	}
}

// TestRegister_SecondUser_DoesNotCreateAnotherOrg asserts the org bootstrap
// only fires for the very first registration; subsequent registrations
// must NOT spawn additional orgs.
func TestRegister_SecondUser_DoesNotCreateAnotherOrg(t *testing.T) {
	r, store, setupToken := setupRegisterRouter(t)

	first := fmt.Sprintf(`{"username":"founder","email":"founder@example.com","password":%q,"setupToken":%q}`,
		testutil.FixturePassword("founder"), setupToken)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(first))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	r.ServeHTTP(w, req)

	second := fmt.Sprintf(`{"username":"newcomer","email":"newcomer@example.com","password":%q}`, testutil.FixturePassword("newcomer"))
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", testRegisterPath, strings.NewReader(second))
	req2.Header.Set(sharedContentType, sharedMIMEJSON)
	r.ServeHTTP(w2, req2)

	orgs, _ := store.FindAllOrgs(t.Context())
	if len(orgs) != 1 {
		t.Errorf("expected exactly 1 org after second register, got %d", len(orgs))
	}
}

// TestSetupStatus_NoUsers_ReturnsHasAdminFalse asserts the wizard-status
// endpoint reports hasAdmin=false on an empty platform so the studio
// knows to render the create-admin step.
func TestSetupStatus_NoUsers_ReturnsHasAdminFalse(t *testing.T) {
	r, store, _ := setupRegisterRouter(t)
	r2 := wireSetupStatus(t, r, store)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", testSetupStatusPath, nil)
	r2.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["hasAdmin"] != false {
		t.Errorf("hasAdmin: got %v, want false", resp["hasAdmin"])
	}
}

// TestSetupStatus_AfterRegister_ReturnsHasAdminTrue asserts the endpoint
// flips to hasAdmin=true once a platform_admin exists.
func TestSetupStatus_AfterRegister_ReturnsHasAdminTrue(t *testing.T) {
	r, store, _ := setupRegisterRouter(t)
	r2 := wireSetupStatus(t, r, store)

	// Seed an admin row directly so the test isolates the status logic.
	store.CreateUser(t.Context(), &domain.User{
		ID:           "admin-1",
		Username:     "admin",
		Email:        "admin@example.com",
		PasswordHash: testutil.FixturePasswordHash(),
		Role:         "platform_admin",
		Active:       true,
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", testSetupStatusPath, nil)
	r2.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["hasAdmin"] != true {
		t.Errorf("hasAdmin: got %v, want true", resp["hasAdmin"])
	}
}

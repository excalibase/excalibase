package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// The bootstrap service principal exists from the provisioning pod's first
// boot, before any human has registered (EXC-485). It must not count as "the
// first user" or "an admin", or the operator could never claim the platform
// with the setup token.
func seedServicePrincipal(t *testing.T, h *AuthHandler) {
	t.Helper()
	if err := h.userStore.CreateUser(t.Context(), &domain.User{
		ID: "svc-bootstrap-id", Username: "svc-bootstrap", Email: "svc-bootstrap@svc.excalibase.internal",
		Role: "platform_admin", Active: true, Kind: domain.UserKindService,
	}); err != nil {
		t.Fatalf("seed service principal: %v", err)
	}
}

func TestRegister_FirstHumanClaimsAdminWhenOnlyServicePrincipalsExist(t *testing.T) {
	h, raw := firstAdminHandler(t)
	seedServicePrincipal(t, h)

	w := postRegisterWithToken(h, "founder", "founder@x.test", raw)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	for _, u := range h.userStore.(*mockUserStore).users {
		if u.Username == "founder" && u.Role != "platform_admin" {
			t.Fatalf("founder role = %q, want platform_admin", u.Role)
		}
	}
}

func TestSetupStatus_ServicePrincipalsAreNotAnAdmin(t *testing.T) {
	h, _ := firstAdminHandler(t)
	seedServicePrincipal(t, h)

	w := httptest.NewRecorder()
	h.GetSetupStatus(w, httptest.NewRequest(http.MethodGet, "/api/auth/setup-status", nil))
	var body map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["hasAdmin"] {
		t.Fatal("setup-status must report no admin while only service principals exist")
	}
}

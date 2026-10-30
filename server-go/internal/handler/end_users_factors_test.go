package handler

import (
	"net/http"
	"strings"
	"testing"
)

// EXC-662 step 7: an admin resets an end user's MFA; auth removes the
// factors and recovery codes and ends the user's sessions.

const endUserSevenFactors = endUserSeven + "/factors"

func TestEndUserResetFactorsRelaysAndAudits(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusNoContent, ``)
	w := f.studioRequest("DELETE", endUserSevenFactors, "")
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	call := f.auth.calls[0]
	if call.method != http.MethodDelete || call.path != "/auth/acme/proj-a/users/7/factors" {
		t.Fatalf("auth saw %+v", call)
	}
	assertUserAdminToken(t, f.verifiedClaims(t, call))
	assertNoStudioCredential(t, call)
	entry := f.onlyAuditEntry(t)
	if details := auditDetails(t, entry); entry.Action != "end_user.factors.reset" || entry.Resource != "end_user" ||
		entry.UserID != "admin-1" || details["outcome"] != "success" || details["userId"] != float64(7) {
		t.Fatalf("audit %+v %v", entry, details)
	}
}

func TestEndUserResetFactorsRefusalsAreRelayedAndAudited(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusNotFound, `{"error":"no such user","code":"user_not_found"}`)
	w := f.studioRequest("DELETE", endUserSevenFactors, "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"user_not_found"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if details := auditDetails(t, f.onlyAuditEntry(t)); details["outcome"] != "refused" || details["status"] != float64(http.StatusNotFound) {
		t.Fatalf("audit %v", details)
	}
	f = newEndUsersFixture(t, http.StatusBadGateway, `{"error":"db at 10.0.0.4"}`)
	if w := f.studioRequest("DELETE", endUserSevenFactors, ""); w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "10.0.0.4") {
		t.Fatalf("auth down: %d %s", w.Code, w.Body.String())
	}
}

func TestEndUserResetFactorsValidatesTheUserAndProject(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusNoContent, ``)
	for _, path := range []string{"/api/projects/proj-a/end-users/abc/factors", "/api/projects/proj-a/end-users/0/factors"} {
		if w := f.studioRequest("DELETE", path, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := f.studioRequest("DELETE", "/api/projects/proj-missing/end-users/7/factors", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", w.Code)
	}
	if len(f.auth.calls) != 0 {
		t.Fatal("an invalid request reached auth")
	}
	router, _ := endUsersRouter(nil, "admin-1")
	if w := doRequest(router, "DELETE", endUserSevenFactors, ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without auth: %d", w.Code)
	}
}

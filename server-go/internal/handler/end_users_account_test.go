package handler

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// EXC-662 step 4: an admin reads one end user, changes their metadata or
// name, and deletes them; the changes are audited without their values.

const endUserSeven = "/api/projects/proj-a/end-users/7"

func TestEndUserGetRelaysTheAccountWithAUserAdminToken(t *testing.T) {
	answer := `{"id":7,"email":"b@x.test","fullName":"Bob","userMetadata":{"theme":"dark"},"appMetadata":{"plan":"pro"}}`
	f := newEndUsersFixture(t, http.StatusOK, answer)
	w := f.studioRequest("GET", endUserSeven, "")
	if w.Code != http.StatusOK || w.Body.String() != answer || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("get: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	call := f.auth.calls[0]
	if call.method != http.MethodGet || call.path != "/auth/acme/proj-a/users/7" {
		t.Fatalf("auth saw %+v", call)
	}
	assertUserAdminToken(t, f.verifiedClaims(t, call))
	assertNoStudioCredential(t, call)
	if len(f.audit.entries) != 0 {
		t.Fatalf("a read was audited: %+v", f.audit.entries)
	}
}

func TestEndUserUpdateRelaysThePatchAndAuditsOnlyWhichFieldsChanged(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{"id":7,"fullName":"Robert Secret","appMetadata":{"plan":"pro-secret"}}`)
	w := f.studioRequest("PATCH", endUserSeven, `{"appMetadata":{"plan":"pro-secret","trial":null},"fullName":"Robert Secret"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"plan":"pro-secret"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	call := f.auth.calls[0]
	var sent map[string]json.RawMessage
	if err := json.Unmarshal([]byte(call.body), &sent); err != nil || call.method != http.MethodPatch || call.path != "/auth/acme/proj-a/users/7" ||
		string(sent["appMetadata"]) != `{"plan":"pro-secret","trial":null}` || string(sent["fullName"]) != `"Robert Secret"` || len(sent) != 2 {
		t.Fatalf("auth saw %+v", call)
	}
	assertUserAdminToken(t, f.verifiedClaims(t, call))
	assertNoStudioCredential(t, call)

	entry := f.onlyAuditEntry(t)
	details := auditDetails(t, entry)
	if entry.Action != "end_user.update" || entry.Resource != "end_user" || entry.ResourceID != "proj-a" || entry.UserID != "admin-1" ||
		details["outcome"] != "success" || details["userId"] != float64(7) ||
		!slices.Equal(stringSlice(details["fields"]), []string{"appMetadata", "fullName"}) {
		t.Fatalf("audit: %+v %v", entry, details)
	}
	if strings.Contains(entry.Details, "Secret") || strings.Contains(entry.Details, "trial") {
		t.Fatalf("the audit carries values: %s", entry.Details)
	}
}

func TestEndUserUpdateValidatesThePatchBeforeAuthSeesIt(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusOK, `{}`)
	bodies := []string{
		`not json`,
		`{}`,
		`[]`,
		`{"appMetadata":null}`,
		`{"appMetadata":[1]}`,
		`{"appMetadata":"plan"}`,
		`{"userMetadata":1}`,
		`{"fullName":5}`,
		`{"fullName":null}`,
		`{"role":"admin"}`,
		`{"appMetadata":{},"email":"x@y.test"}`,
		`{"appMetadata":{}} {"appMetadata":{}}`,
		`{"appMetadata":{"blob":"` + strings.Repeat("a", 41<<10) + `"}}`,
	}
	for _, body := range bodies {
		w := f.studioRequest("PATCH", endUserSeven, body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
			t.Errorf("%.60s: got %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/projects/proj-a/end-users/abc", "/api/projects/proj-a/end-users/0", "/api/projects/proj-a/end-users/-1"} {
		for _, method := range []string{"GET", "PATCH", "DELETE"} {
			if w := f.studioRequest(method, path, `{"fullName":"x"}`); w.Code != http.StatusBadRequest {
				t.Errorf("%s %s: got %d", method, path, w.Code)
			}
		}
	}
	if len(f.auth.calls) != 0 {
		t.Fatalf("an invalid request reached auth: %+v", f.auth.calls)
	}
	for _, entry := range f.audit.entries {
		if details := auditDetails(t, entry); details["outcome"] != "invalid_request" || strings.Contains(entry.Details, "aaaa") {
			t.Fatalf("audit %+v", entry)
		}
	}
}

func TestEndUserUpdatePassesAuthsRefusalThroughAndAuditsIt(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusBadRequest, `{"error":"metadata is larger than 16384 bytes","code":"metadata_too_large"}`)
	w := f.studioRequest("PATCH", endUserSeven, `{"userMetadata":{"bio":"x"}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"metadata_too_large"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	details := auditDetails(t, f.onlyAuditEntry(t))
	if details["outcome"] != "refused" || details["code"] != "metadata_too_large" || !slices.Equal(stringSlice(details["fields"]), []string{"userMetadata"}) {
		t.Fatalf("audit %v", details)
	}
}

func TestEndUserDeleteRelaysAndAudits(t *testing.T) {
	f := newEndUsersFixture(t, http.StatusNoContent, ``)
	w := f.studioRequest("DELETE", endUserSeven, "")
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	call := f.auth.calls[0]
	if call.method != http.MethodDelete || call.path != "/auth/acme/proj-a/users/7" {
		t.Fatalf("auth saw %+v", call)
	}
	assertUserAdminToken(t, f.verifiedClaims(t, call))
	assertNoStudioCredential(t, call)
	entry := f.onlyAuditEntry(t)
	if details := auditDetails(t, entry); entry.Action != "end_user.delete" || details["outcome"] != "success" || details["userId"] != float64(7) {
		t.Fatalf("audit %+v %v", entry, details)
	}
}

func TestEndUserDeleteRefusalsAreRelayedAndAudited(t *testing.T) {
	cases := map[int]string{
		http.StatusNotFound: `{"error":"no such user","code":"user_not_found"}`,
		http.StatusConflict: `{"error":"an app table still references the account","code":"user_referenced"}`,
	}
	for status, answer := range cases {
		f := newEndUsersFixture(t, status, answer)
		w := f.studioRequest("DELETE", endUserSeven, "")
		if w.Code != status || !strings.Contains(w.Body.String(), `"code":"user_`) {
			t.Errorf("auth %d: got %d %s", status, w.Code, w.Body.String())
		}
		if details := auditDetails(t, f.onlyAuditEntry(t)); details["outcome"] != "refused" || details["status"] != float64(status) {
			t.Errorf("auth %d: audit %v", status, details)
		}
	}
	f := newEndUsersFixture(t, http.StatusServiceUnavailable, `{"error":"db at 10.0.0.4"}`)
	if w := f.studioRequest("DELETE", endUserSeven, ""); w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "10.0.0.4") {
		t.Fatalf("auth down: %d %s", w.Code, w.Body.String())
	}
}

func TestEndUserAccountRoutesNeedAuthAndAProject(t *testing.T) {
	router, _ := endUsersRouter(nil, "admin-1")
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		if w := doRequest(router, method, endUserSeven, `{"fullName":"x"}`); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without auth: %d", method, w.Code)
		}
	}
	f := newEndUsersFixture(t, http.StatusOK, `{}`)
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		if w := f.studioRequest(method, "/api/projects/proj-missing/end-users/7", `{"fullName":"x"}`); w.Code != http.StatusNotFound {
			t.Errorf("%s in an unknown project: %d", method, w.Code)
		}
	}
	if len(f.auth.calls) != 0 {
		t.Fatal("auth was called for an unresolved project")
	}
}

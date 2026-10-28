package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type fakeReadAddress struct {
	view  service.DBReadAddressView
	err   error
	calls []bool
}

func (f *fakeReadAddress) ReadAddress(context.Context, string) (service.DBReadAddressView, error) {
	return f.view, f.err
}

func (f *fakeReadAddress) SetReadAddress(_ context.Context, _ string, enabled bool) (service.DBReadAddressView, error) {
	f.calls = append(f.calls, enabled)
	f.view.Enabled = enabled
	return f.view, f.err
}

func readAddressCall(t *testing.T, h *DBReadAddressHandler, method, body string, role string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/db-read-address", h.Routes)
	req := httptest.NewRequest(method, "/api/projects/proj-abc/db-read-address/", strings.NewReader(body))
	if role != "" {
		req = req.WithContext(custommw.WithProjectAccess(req.Context(), &custommw.ProjectAccess{Member: &domain.OrgMember{Role: role}}))
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeReadAddress(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

func TestGetReadAddressRendersReplicasAndBothAddresses(t *testing.T) {
	api := &fakeReadAddress{view: service.DBReadAddressView{
		ProjectID: "proj-abc", Replicas: 2, PublicOffered: true, PublicOpen: true, Enabled: true, Available: true,
		Host: "proj-abc.db.example.com", Port: 31001, ConnectionString: "postgresql://app@proj-abc.db.example.com:31001/app?sslmode=verify-full",
		Internal: service.DBReadAddressInternal{Host: "proj-abc-postgres-ro.ns.svc.cluster.local", Port: 5432, ConnectionString: "postgresql://app@x:5432/app"},
	}}
	w := readAddressCall(t, NewDBReadAddressHandler(api, nil), http.MethodGet, "", domain.OrgRoleDeveloper)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %d %s", w.Code, w.Body.String())
	}
	body := decodeReadAddress(t, w)
	internal, _ := body["internal"].(map[string]any)
	if body["replicas"] != float64(2) || body["port"] != float64(31001) || body["available"] != true ||
		body["publicOpen"] != true || body["canChange"] != false || internal["host"] != "proj-abc-postgres-ro.ns.svc.cluster.local" {
		t.Fatalf("body %v", body)
	}
	if strings.Contains(w.Body.String(), "password") {
		t.Fatal("the read address carries a password")
	}
}

func TestGetReadAddressOmitsTheInternalAddressWithoutReplicas(t *testing.T) {
	api := &fakeReadAddress{view: service.DBReadAddressView{ProjectID: "proj-abc", Reason: domain.ReadAddressNoReplica}}
	body := decodeReadAddress(t, readAddressCall(t, NewDBReadAddressHandler(api, nil), http.MethodGet, "", domain.OrgRoleAdmin))
	if _, ok := body["internal"]; ok || body["reason"] != domain.ReadAddressNoReplica || body["canChange"] != true {
		t.Fatalf("body %v", body)
	}
}

func TestPutReadAddressRecordsAnAudit(t *testing.T) {
	api := &fakeReadAddress{view: service.DBReadAddressView{ProjectID: "proj-abc", Replicas: 2}}
	audit := &recordingAudit{}
	w := readAddressCall(t, NewDBReadAddressHandler(api, audit), http.MethodPut, `{"enabled":true}`, domain.OrgRoleAdmin)
	if w.Code != http.StatusOK || len(api.calls) != 1 || !api.calls[0] {
		t.Fatalf("PUT %d calls %v", w.Code, api.calls)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != auditDBReadAddressUpdate ||
		!strings.Contains(audit.entries[0].Details, `"outcome":"saved"`) || !strings.Contains(audit.entries[0].Details, `"enabled":true`) {
		t.Fatalf("audit %+v", audit.entries)
	}
}

func TestPutReadAddressRejectsABadBody(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"enabled":"yes"}`, `{"enabled":true,"port":1}`} {
		api := &fakeReadAddress{}
		w := readAddressCall(t, NewDBReadAddressHandler(api, nil), http.MethodPut, body, domain.OrgRoleAdmin)
		if w.Code != http.StatusBadRequest || len(api.calls) != 0 {
			t.Errorf("body %q: %d calls %v", body, w.Code, api.calls)
		}
	}
}

func TestPutReadAddressMapsRefusals(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{service.ErrReadAddressNoReplica, http.StatusConflict},
		{service.ErrReadAddressNeedsPublicPort, http.StatusConflict},
		{service.ErrProjectNotActive, http.StatusConflict},
		{service.ErrProjectOperationRunning, http.StatusConflict},
		{service.ErrDBEndpointUnsupported, http.StatusConflict},
		{service.ErrDBEndpointNotConfigured, http.StatusServiceUnavailable},
		{service.ErrReadAddressCopiesUnreadable, http.StatusServiceUnavailable},
		{service.ErrDBEndpointNotObserved, http.StatusBadGateway},
		{errors.New("pq: secret internal detail"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		audit := &recordingAudit{}
		api := &fakeReadAddress{err: tc.err}
		w := readAddressCall(t, NewDBReadAddressHandler(api, audit), http.MethodPut, `{"enabled":true}`, domain.OrgRoleAdmin)
		if w.Code != tc.want {
			t.Errorf("%v: %d, want %d", tc.err, w.Code, tc.want)
		}
		if strings.Contains(w.Body.String(), "secret internal detail") {
			t.Errorf("internal detail leaked: %s", w.Body.String())
		}
		if len(audit.entries) != 1 || !strings.Contains(audit.entries[0].Details, `"outcome":"refused"`) {
			t.Errorf("%v: audit %+v", tc.err, audit.entries)
		}
	}
}

func TestReadAddressFailsClosedWithoutAService(t *testing.T) {
	w := readAddressCall(t, NewDBReadAddressHandler(nil, nil), http.MethodGet, "", domain.OrgRoleAdmin)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired: %d", w.Code)
	}
}

func TestReadAddressRefusesABadProjectID(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/db-read-address", NewDBReadAddressHandler(&fakeReadAddress{}, nil).Routes)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/projects/..%2f/db-read-address/", nil))
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", w.Code)
	}
}

func TestASingleHostReadAddressCannotBeChanged(t *testing.T) {
	api := &fakeReadAddress{view: service.DBReadAddressView{ProjectID: "proj-abc", SingleHost: true, Reason: domain.ReadAddressSingleHost}}
	body := decodeReadAddress(t, readAddressCall(t, NewDBReadAddressHandler(api, nil), http.MethodGet, "", domain.OrgRoleOwner))
	if body["canChange"] != false || body["singleHost"] != true {
		t.Fatalf("body %v", body)
	}
}

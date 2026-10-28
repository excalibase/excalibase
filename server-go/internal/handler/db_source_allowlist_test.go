package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type fakeAllowListAPI struct {
	getErr  error
	current domain.DBSourceAllowList
	public  bool
	single  bool
	setErr  error
	sets    []domain.DBSourceAllowList
}

func (f *fakeAllowListAPI) view() service.DBAllowListView {
	return service.DBAllowListView{ProjectID: "proj-a", Access: f.current.Access, Sources: f.current.Sources,
		Max: domain.MaxDBAllowedSources, PublicEnabled: f.public, SingleHost: f.single}
}

func (f *fakeAllowListAPI) AllowedSources(context.Context, string) (service.DBAllowListView, error) {
	if f.getErr != nil {
		return service.DBAllowListView{}, f.getErr
	}
	return f.view(), nil
}

func (f *fakeAllowListAPI) SetAllowedSources(_ context.Context, _ string, list domain.DBSourceAllowList) (service.DBAllowListView, error) {
	if f.setErr != nil {
		return service.DBAllowListView{}, f.setErr
	}
	previous := f.current
	f.sets = append(f.sets, list)
	f.current = list
	view := f.view()
	view.PreviousAccess, view.PreviousSources = previous.Access, previous.Sources
	return view, nil
}

func allowListRouter(api DBAllowListAPI, audit auditWriter) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/db-endpoint/allowed-sources", NewDBAllowListHandler(api, audit).Routes)
	return r
}

func callAllowList(router http.Handler, method, body, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/projects/proj-a/db-endpoint/allowed-sources", strings.NewReader(body))
	req.RemoteAddr = "198.51.100.23:51000"
	ctx := auth.SetUser(req.Context(), &domain.User{ID: "user-1", Active: true})
	ctx = custommw.WithProjectAccess(ctx, &custommw.ProjectAccess{Member: &domain.OrgMember{Role: role}})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

type allowListWire struct {
	ProjectID     string   `json:"projectId"`
	Access        string   `json:"access"`
	Sources       []string `json:"sources"`
	Max           int      `json:"max"`
	PublicEnabled bool     `json:"publicEnabled"`
	SingleHost    bool     `json:"singleHost"`
	CanChange     bool     `json:"canChange"`
	CallerAddress string   `json:"callerAddress"`
}

func decodeAllowList(t *testing.T, rec *httptest.ResponseRecorder) allowListWire {
	t.Helper()
	var wire allowListWire
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return wire
}

func TestAllowListGetReportsTheChoiceAndTheCallersAddress(t *testing.T) {
	api := &fakeAllowListAPI{current: domain.DBSourceAllowList{Access: domain.DBSourceAccessListed, Sources: []string{"203.0.113.0/24"}}, public: true}
	rec := callAllowList(allowListRouter(api, &recordedAudit{}), http.MethodGet, "", domain.OrgRoleDeveloper)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	wire := decodeAllowList(t, rec)
	if wire.Access != "listed" || !slices.Equal(wire.Sources, []string{"203.0.113.0/24"}) || wire.Max != domain.MaxDBAllowedSources ||
		!wire.PublicEnabled || wire.CanChange || wire.CallerAddress != "198.51.100.23" {
		t.Fatalf("wire %+v", wire)
	}
}

func TestAllowListUnsetIsSpelledOutAndSourcesAreNeverNull(t *testing.T) {
	rec := callAllowList(allowListRouter(&fakeAllowListAPI{}, &recordedAudit{}), http.MethodGet, "", domain.OrgRoleAdmin)
	if !strings.Contains(rec.Body.String(), `"access":"unset"`) || !strings.Contains(rec.Body.String(), `"sources":[]`) {
		t.Fatalf("body %s", rec.Body)
	}
	if wire := decodeAllowList(t, rec); !wire.CanChange {
		t.Fatal("an Admin may change it")
	}
}

func TestAllowListPutParsesSavesAndAudits(t *testing.T) {
	api := &fakeAllowListAPI{current: domain.DBSourceAllowList{Access: domain.DBSourceAccessAny}}
	audit := &recordedAudit{}
	rec := callAllowList(allowListRouter(api, audit), http.MethodPut, `{"sources":["198.51.100.7"," 2001:db8::/32"]}`, domain.OrgRoleAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if len(api.sets) != 1 || !slices.Equal(api.sets[0].Sources, []string{"198.51.100.7/32", "2001:db8::/32"}) {
		t.Fatalf("sets %+v", api.sets)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("audit %+v", audit.entries)
	}
	entry := audit.entries[0]
	if entry.Action != "db_endpoint.allowed_sources.update" || entry.ProjectID != "proj-a" || entry.UserID != "user-1" ||
		!strings.Contains(entry.Details, `"previousAccess":"any"`) || !strings.Contains(entry.Details, "198.51.100.7/32") {
		t.Fatalf("entry %+v", entry)
	}
}

func TestAllowListPutAllowAny(t *testing.T) {
	api := &fakeAllowListAPI{}
	rec := callAllowList(allowListRouter(api, &recordedAudit{}), http.MethodPut, `{"allowAny":true}`, domain.OrgRoleAdmin)
	if rec.Code != http.StatusOK || api.current.Access != domain.DBSourceAccessAny {
		t.Fatalf("status %d %s, %+v", rec.Code, rec.Body, api.current)
	}
}

func TestAllowListPutRefusals(t *testing.T) {
	cases := map[string]struct {
		body   string
		setErr error
		status int
	}{
		"bad json":             {body: `{`, status: http.StatusBadRequest},
		"unknown field":        {body: `{"cidrs":["198.51.100.7"]}`, status: http.StatusBadRequest},
		"every address":        {body: `{"sources":["0.0.0.0/0"]}`, status: http.StatusBadRequest},
		"empty":                {body: `{"sources":[]}`, status: http.StatusBadRequest},
		"single host":          {body: `{"sources":["198.51.100.7"]}`, setErr: service.ErrDBEndpointUnsupported, status: http.StatusConflict},
		"lease held":           {body: `{"allowAny":true}`, setErr: service.ErrProjectOperationRunning, status: http.StatusConflict},
		"not observed":         {body: `{"allowAny":true}`, setErr: service.ErrDBAllowListNotObserved, status: http.StatusBadGateway},
		"flag off":             {body: `{"allowAny":true}`, setErr: service.ErrDBAllowListOff, status: http.StatusNotFound},
		"something else broke": {body: `{"allowAny":true}`, setErr: errors.New("boom"), status: http.StatusInternalServerError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			audit := &recordedAudit{}
			rec := callAllowList(allowListRouter(&fakeAllowListAPI{setErr: tc.setErr}, audit), http.MethodPut, tc.body, domain.OrgRoleAdmin)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.setErr == nil && len(audit.entries) != 0 {
				t.Fatal("a request refused before the service is not audited")
			}
			if tc.setErr != nil && (len(audit.entries) != 1 || !strings.Contains(audit.entries[0].Details, `"outcome":"refused"`)) {
				t.Fatalf("a refused change is audited with its outcome: %+v", audit.entries)
			}
		})
	}
}

func TestDBEndpointPutReportsAnUnconfirmedListAsABadGateway(t *testing.T) {
	rec := httptest.NewRecorder()
	writeDBEndpointError(rec, service.ErrDBAllowListNotObserved)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDBEndpointPutSaysToChooseSourcesFirst(t *testing.T) {
	rec := httptest.NewRecorder()
	writeDBEndpointError(rec, service.ErrDBAllowListNotChosen)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "choose who may connect") {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
}

func TestAllowListAnswersWhatItCannotServe(t *testing.T) {
	unwired := NewDBAllowListHandler(nil, nil)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/db-endpoint/allowed-sources", unwired.Routes)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := callAllowList(r, method, `{"allowAny":true}`, domain.OrgRoleAdmin); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s unwired: %d", method, rec.Code)
		}
	}
	unwired.SetAPI(&fakeAllowListAPI{})
	if rec := callAllowList(r, http.MethodGet, "", domain.OrgRoleAdmin); rec.Code != http.StatusOK {
		t.Errorf("wired: %d", rec.Code)
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/projects/bad$id/db-endpoint/allowed-sources", nil)
	rec := httptest.NewRecorder()
	allowListRouter(&fakeAllowListAPI{}, nil).ServeHTTP(rec, bad)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}

	failing := &fakeAllowListAPI{getErr: errors.New("store down")}
	if rec := callAllowList(allowListRouter(failing, nil), http.MethodGet, "", domain.OrgRoleAdmin); rec.Code != http.StatusInternalServerError ||
		strings.Contains(rec.Body.String(), "store down") {
		t.Errorf("read failure: %d %s", rec.Code, rec.Body)
	}
	invalid := &fakeAllowListAPI{setErr: domain.ErrInvalidDBSourceAllowList}
	if rec := callAllowList(allowListRouter(invalid, nil), http.MethodPut, `{"allowAny":true}`, domain.OrgRoleAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid from the service: %d", rec.Code)
	}
	if rec := callAllowList(allowListRouter(&fakeAllowListAPI{}, failingAudit{}), http.MethodPut, `{"allowAny":true}`, domain.OrgRoleAdmin); rec.Code != http.StatusOK {
		t.Errorf("an audit failure does not fail the change: %d", rec.Code)
	}
}

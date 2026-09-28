package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type fakeAppNetwork struct {
	view   service.AppNetworkView
	err    error
	setTo  *bool
	called int
}

func (f *fakeAppNetwork) Describe(_ context.Context, projectID string) (service.AppNetworkView, error) {
	f.called++
	f.view.ProjectID = projectID
	return f.view, f.err
}

func (f *fakeAppNetwork) Set(_ context.Context, projectID string, enabled bool) (service.AppNetworkView, error) {
	f.called++
	f.setTo = &enabled
	if f.err != nil {
		return service.AppNetworkView{}, f.err
	}
	return service.AppNetworkView{ProjectID: projectID, PrivateNetwork: enabled, Applied: enabled}, nil
}

const appNetworkPath = "/api/projects/proj-abc/app-network"

func appNetworkRouter(api AppNetworkAPI, access *custommw.ProjectAccess) http.Handler {
	h := NewAppNetworkHandler(api)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(custommw.WithProjectAccess(req.Context(), access)))
		})
	})
	r.Route("/api/projects/{projectId}/app-network", h.Routes)
	return r
}

func callAppNetwork(router http.Handler, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, appNetworkPath, strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

var adminAccess = &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleAdmin}}

func TestAppNetworkGetReportsSettingAndObservation(t *testing.T) {
	api := &fakeAppNetwork{view: service.AppNetworkView{PrivateNetwork: true, Applied: false}}
	w := callAppNetwork(appNetworkRouter(api, adminAccess), http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["projectId"] != "proj-abc" || got["privateNetwork"] != true || got["applied"] != false || got["canChange"] != true {
		t.Fatalf("body = %v", got)
	}
}

func TestAppNetworkSaysWhetherTheCallerMayChangeIt(t *testing.T) {
	for role, want := range map[string]bool{
		domain.OrgRoleViewer: false, domain.OrgRoleDeveloper: false, domain.OrgRoleAdmin: true, domain.OrgRoleOwner: true,
	} {
		access := &custommw.ProjectAccess{Member: &domain.OrgMember{Role: role}}
		w := callAppNetwork(appNetworkRouter(&fakeAppNetwork{}, access), http.MethodGet, "")
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["canChange"] != want {
			t.Errorf("%s: canChange = %v, want %v", role, got["canChange"], want)
		}
	}
}

func TestAppNetworkPutAppliesTheChoice(t *testing.T) {
	api := &fakeAppNetwork{}
	w := callAppNetwork(appNetworkRouter(api, adminAccess), http.MethodPut, `{"privateNetwork":true}`)
	if w.Code != http.StatusOK || api.setTo == nil || !*api.setTo {
		t.Fatalf("status %d, set %v: %s", w.Code, api.setTo, w.Body)
	}
}

// Nothing is guessed: a body that does not say on or off changes nothing.
func TestAppNetworkPutRefusesABodyWithoutAChoice(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"privateNetwork":"yes"}`, `not json`} {
		api := &fakeAppNetwork{}
		w := callAppNetwork(appNetworkRouter(api, adminAccess), http.MethodPut, body)
		if w.Code != http.StatusBadRequest || api.called != 0 {
			t.Errorf("body %q: status %d, service called %d times", body, w.Code, api.called)
		}
	}
}

func TestAppNetworkErrorsMapToTheirMeaning(t *testing.T) {
	cases := map[error]int{
		service.ErrAppNetworkProjectNotFound:  http.StatusNotFound,
		service.ErrAppNetworkUnsupported:      http.StatusConflict,
		service.ErrProjectNotActive:           http.StatusConflict,
		service.ErrProjectOperationRunning:    http.StatusConflict,
		errors.New("cilium: internal detail"): http.StatusBadGateway,
	}
	for err, want := range cases {
		w := callAppNetwork(appNetworkRouter(&fakeAppNetwork{err: err}, adminAccess), http.MethodPut, `{"privateNetwork":true}`)
		if w.Code != want {
			t.Errorf("%v: status %d, want %d", err, w.Code, want)
		}
		if want == http.StatusBadGateway && strings.Contains(w.Body.String(), "cilium") {
			t.Errorf("a cluster failure leaked its detail: %s", w.Body)
		}
	}
}

func TestAppNetworkRefusesAnInvalidProjectID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/projects/bad..id/app-network", nil)
	w := httptest.NewRecorder()
	appNetworkRouter(&fakeAppNetwork{}, adminAccess).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}

func TestAppNetworkGetReportsAFailedRead(t *testing.T) {
	w := callAppNetwork(appNetworkRouter(&fakeAppNetwork{err: errors.New("api down")}, adminAccess), http.MethodGet, "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", w.Code)
	}
}

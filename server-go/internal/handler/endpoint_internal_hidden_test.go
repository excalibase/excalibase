package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

func endpointViewWithClusterAddress() service.DBEndpointView {
	view := service.DBEndpointView{ProjectID: "proj-abc", Enabled: true, Available: true, Host: "proj-abc.db.excalibase.io", Port: 30111}
	view.Internal.Host = "proj-abc-postgres-rw.org-proj-abc.svc.cluster.local"
	view.Internal.Port = 5432
	view.Internal.ConnectionString = "postgresql://app@proj-abc-postgres-rw.org-proj-abc.svc.cluster.local:5432/app"
	view.Internal.MongoHost = "proj-abc-documentdb.org-proj-abc.svc.cluster.local"
	return view
}

func asUser(req *http.Request, role string) *http.Request {
	return req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "u1", Role: role, Active: true}))
}

func TestDBEndpointHidesTheClusterAddressFromACustomer(t *testing.T) {
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/projects/proj-abc/db-endpoint/", nil), "user")
	w := httptest.NewRecorder()
	dbEndpointRouter(&fakeDBEndpoints{view: endpointViewWithClusterAddress()}).ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "svc.cluster.local") || strings.Contains(w.Body.String(), `"internal"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "proj-abc.db.excalibase.io") {
		t.Errorf("the public endpoint went with it: %s", w.Body.String())
	}
}

func TestDBEndpointKeepsTheClusterAddressForAPlatformAdmin(t *testing.T) {
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/projects/proj-abc/db-endpoint/", nil), "platform_admin")
	w := httptest.NewRecorder()
	dbEndpointRouter(&fakeDBEndpoints{view: endpointViewWithClusterAddress()}).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "svc.cluster.local") {
		t.Errorf("the admin view lost the cluster address: %s", w.Body.String())
	}
}

func TestReadAddressHidesTheClusterAddressFromACustomer(t *testing.T) {
	api := &fakeReadAddress{view: service.DBReadAddressView{ProjectID: "proj-abc", Replicas: 2,
		Internal: service.DBReadAddressInternal{Host: "proj-abc-postgres-ro.ns.svc.cluster.local", Port: 5432, ConnectionString: "postgresql://app@x.svc.cluster.local:5432/app"}}}
	router := chi.NewRouter()
	router.Route("/api/projects/{projectId}/db-read-address", NewDBReadAddressHandler(api, nil).Routes)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, asUser(httptest.NewRequest(http.MethodGet, "/api/projects/proj-abc/db-read-address/", nil), "user"))
	if strings.Contains(w.Body.String(), "svc.cluster.local") {
		t.Fatalf("a customer's read address names the cluster: %s", w.Body.String())
	}
}

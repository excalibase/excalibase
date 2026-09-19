package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// deletingProjectRouter mounts the real gate over a project being torn down,
// with the route shapes main.go uses.
func deletingProjectRouter(t *testing.T, status string) (chi.Router, string) {
	t.Helper()
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: testProject, OrgID: testOrg, Status: status})
	memberID := testutil.FixturePassword("member")
	orgs := fakestore.NewOrgs()
	orgs.AddMember(testOrg, memberID, "owner")

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}", func(r chi.Router) {
		r.Use(RequireProjectAccess(instances, orgs))
		r.Get("/", ok)
		r.Delete("/", ok)
		r.Post("/backups/purge", ok)
		r.Post("/credentials/rotate", ok)
		r.Post("/pause", ok)
		r.Post("/resume", ok)
		r.Put("/maintenance-window", ok)
		r.Post("/backup/trigger", ok)
		r.Post("/backup/restore", ok)
		r.Post("/rls-policies", ok)
		r.Post("/table-grants", ok)
	})
	r.Route("/api/projects/{projectId}", func(r chi.Router) {
		r.Use(RequireProjectAccess(instances, orgs))
		r.Post("/functions", ok)
		r.Put("/cors", ok)
		r.Get("/info", ok)
	})
	return r, memberID
}

// A project under teardown accepts only the two requests its own teardown
// needs: reading its status, and the DELETE that retries it. Everything else
// would act on resources that are going away.
func TestDeletingProjectRefusesEveryOtherRoute(t *testing.T) {
	for _, status := range []string{string(domain.StatusDeleting), string(domain.StatusBackupsPendingDelete)} {
		r, memberID := deletingProjectRouter(t, status)
		cases := []struct {
			method, path string
			want         int
		}{
			{http.MethodGet, "/api/provision/" + testProject + "/", http.StatusOK},
			{http.MethodDelete, "/api/provision/" + testProject + "/", http.StatusOK},
			{http.MethodPost, "/api/provision/" + testProject + "/backups/purge", http.StatusOK},
			{http.MethodPost, "/api/provision/" + testProject + "/credentials/rotate", http.StatusConflict},
			{http.MethodPost, "/api/provision/" + testProject + "/pause", http.StatusConflict},
			{http.MethodPost, "/api/provision/" + testProject + "/resume", http.StatusConflict},
			{http.MethodPut, "/api/provision/" + testProject + "/maintenance-window", http.StatusConflict},
			{http.MethodPost, "/api/provision/" + testProject + "/backup/trigger", http.StatusConflict},
			{http.MethodPost, "/api/provision/" + testProject + "/backup/restore", http.StatusConflict},
			{http.MethodPost, "/api/provision/" + testProject + "/rls-policies", http.StatusConflict},
			{http.MethodPost, "/api/provision/" + testProject + "/table-grants", http.StatusConflict},
			{http.MethodPost, "/api/projects/" + testProject + "/functions", http.StatusConflict},
			{http.MethodPut, "/api/projects/" + testProject + "/cors", http.StatusConflict},
		}
		for _, tc := range cases {
			req := projectRequest(tc.method, testProject, memberUser(memberID), nil)
			req = httptest.NewRequest(tc.method, tc.path, nil)
			req = req.WithContext(projectRequest(tc.method, testProject, memberUser(memberID), nil).Context())
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("%s %s (%s): got %d, want %d", tc.method, tc.path, status, w.Code, tc.want)
			}
		}
	}
}

// A live project is untouched by the gate.
func TestLiveProjectIsUnaffectedByTheDeletionGate(t *testing.T) {
	r, memberID := deletingProjectRouter(t, "ACTIVE")
	for _, path := range []string{"/credentials/rotate", "/pause", "/rls-policies"} {
		req := httptest.NewRequest(http.MethodPost, "/api/provision/"+testProject+path, nil)
		req = req.WithContext(projectRequest(http.MethodPost, testProject, memberUser(memberID), nil).Context())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("POST %s on a live project: got %d, want 200", path, w.Code)
		}
	}
}

// The data plane's /info read reaches its handler, which answers 404 for a
// project under teardown; the gate must not turn that into a 409.
func TestDeletingProjectAllowsTheDataPlaneInfoRead(t *testing.T) {
	r, memberID := deletingProjectRouter(t, string(domain.StatusDeleting))
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+testProject+"/info", nil)
	req = req.WithContext(projectRequest(http.MethodGet, testProject, memberUser(memberID), nil).Context())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want the handler to be reached (200 from the stub)", w.Code)
	}
}

// A platform admin's access carries no membership row, so the gate looks the
// project up itself rather than waving the request through.
func TestDeletingProjectRefusesPlatformAdminWrites(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{
		ProjectID: testProject, OrgID: testOrg, Status: string(domain.StatusDeleting),
	})
	orgs := fakestore.NewOrgs()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}", func(r chi.Router) {
		r.Use(RequireProjectAccess(instances, orgs))
		r.Get("/", ok)
		r.Post("/pause", ok)
	})

	admin := &domain.User{ID: "admin-1", Role: testPlatformRole}
	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/provision/" + testProject + "/", http.StatusOK},
		{http.MethodPost, "/api/provision/" + testProject + "/pause", http.StatusConflict},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req = req.WithContext(projectRequest(tc.method, testProject, admin, nil).Context())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

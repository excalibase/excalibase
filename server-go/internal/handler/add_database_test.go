package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

func addDatabaseRouterFixture(t *testing.T) *clusterChangeFixture {
	t.Helper()
	f := newClusterChangeFixture(t)
	h := NewProvisioningHandler(f.svc, &adminOrgStore{})
	h.SetInstanceStore(f.store)
	f.router.Post("/api/provision/{projectId}/database", h.AddDatabase)
	if err := f.store.Create(&domain.DatabaseInstance{
		ProjectID: "apps-only", OrgID: "org1", Tier: domain.Free, Namespace: "org1-apps-only",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE", NoDatabase: true,
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAddDatabaseAnswersEachRefusal(t *testing.T) {
	cases := map[string]struct {
		project, body string
		want          int
		says          string
	}{
		"a project that has one":               {"test-db", `{"databaseType":"POSTGRESQL","postgresVersion":"17"}`, http.StatusConflict, "already has a database"},
		"no version":                           {"apps-only", `{"databaseType":"POSTGRESQL"}`, http.StatusBadRequest, "postgres version is required"},
		"a project field":                      {"apps-only", `{"projectName":"x","databaseType":"POSTGRESQL","postgresVersion":"17"}`, http.StatusBadRequest, "only database settings"},
		"unknown field":                        {"apps-only", `{"postgresVersion":"17","bogus":1}`, http.StatusBadRequest, `bogus\" is not a provisioning setting`},
		"documentDb where it is not installed": {"apps-only", `{"databaseType":"POSTGRESQL","postgresVersion":"17","documentDb":true}`, http.StatusConflict, "DocumentDB is not installed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := addDatabaseRouterFixture(t)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/provision/"+tc.project+"/database", strings.NewReader(tc.body)))
			if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.says) {
				t.Fatalf("got %d %s, want %d containing %q", w.Code, w.Body.String(), tc.want, tc.says)
			}
		})
	}
}

func TestAddDatabaseRefusesAProjectThatIsNotRunning(t *testing.T) {
	f := addDatabaseRouterFixture(t)
	row, _ := f.store.FindByProjectID("apps-only")
	row.Status = string(domain.StatusPendingDeletion)
	_ = f.store.Update(row)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/provision/apps-only/database",
		strings.NewReader(`{"databaseType":"POSTGRESQL","postgresVersion":"17"}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d %s, want 409", w.Code, w.Body.String())
	}
}

// The Studio shows "Add database" only when the server says this caller may
// add one: Admin and up, on a running project that has none.
func TestProjectStatusSaysWhetherTheCallerMayAddADatabase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project string
		access  *custommw.ProjectAccess
		want    any
	}{
		{"viewer", "apps-only", &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleViewer}}, false},
		{"developer", "apps-only", &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleDeveloper}}, false},
		{"admin", "apps-only", &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleAdmin}}, true},
		{"platform admin", "apps-only", &custommw.ProjectAccess{PlatformAdmin: true}, true},
		{"a project that has one", "test-db", &custommw.ProjectAccess{PlatformAdmin: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := addDatabaseRouterFixture(t)
			h := NewProvisioningHandler(f.svc, &adminOrgStore{})
			h.SetInstanceStore(f.store)
			f.router.Get("/api/provision/{projectId}/", h.GetStatus)
			req := httptest.NewRequest(http.MethodGet, "/api/provision/"+tc.project+"/", nil)
			req = req.WithContext(custommw.WithProjectAccess(req.Context(), tc.access))
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if got := body["canAddDatabase"]; got != tc.want {
				t.Fatalf("canAddDatabase = %v, want %v", got, tc.want)
			}
		})
	}
}

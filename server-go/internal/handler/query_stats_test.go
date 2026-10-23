package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type queryStatsFixture struct {
	router chi.Router
	mock   *k8s.MockClient
	store  *storage.FileSystemStore
}

func newQueryStatsFixture(t *testing.T, role string) *queryStatsFixture {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "test-db", OrgID: "org1", DBType: domain.PostgreSQL, Tier: domain.Free,
		Namespace: "org1-test-db", Status: "ACTIVE", PostgresVersion: "17", DatabaseName: "app",
	}); err != nil {
		t.Fatal(err)
	}
	mock := k8s.NewMockClient()
	mock.AutoReconcileClusters = true
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{ProjectID: "test-db", Namespace: "org1-test-db",
		Tier: config.TierConfig{Instances: 1, StorageSize: "2Gi", Memory: "512Mi", CPU: "0.5"}})
	if err := mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, "org1-test-db", cluster); err != nil {
		t.Fatal(err)
	}
	prov := service.NewProvisioningService(store, provisioner.NewFactory(), mock)
	h := NewQueryStatsHandler(service.NewQueryStatsService(store, prov, nil, features.NewStatic()))
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/slow-queries", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				access := &custommw.ProjectAccess{Member: &domain.OrgMember{Role: role}}
				next.ServeHTTP(w, req.WithContext(custommw.WithProjectAccess(req.Context(), access)))
			})
		})
		r.Get("/", h.List)
		r.Post("/reset", h.Reset)
		r.Post("/enable", h.Enable)
	})
	return &queryStatsFixture{router: r, mock: mock, store: store}
}

func (f *queryStatsFixture) do(method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(method, "/api/provision/test-db/slow-queries"+path, nil))
	return w
}

func TestQueryStatsHandler_ListsWithWhatTheCallerMayDo(t *testing.T) {
	for role, want := range map[string][2]bool{
		domain.OrgRoleViewer:    {false, false},
		domain.OrgRoleDeveloper: {true, false},
		domain.OrgRoleAdmin:     {true, true},
	} {
		f := newQueryStatsFixture(t, role)
		f.mock.ExecOutput["org1-test-db/test-db-postgres-1"] = `[{"query":"SELECT $1","calls":2,"totalTimeMs":3,"meanTimeMs":1.5,"rows":2}]`
		w := f.do(http.MethodGet, "/?sort=mean&limit=5")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", role, w.Code, w.Body)
		}
		var body struct {
			service.SlowQueryReport
			CanReset  bool `json:"canReset"`
			CanEnable bool `json:"canEnable"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.Enabled || body.Sort != "mean" || len(body.Queries) != 1 || body.Queries[0].Query != "SELECT $1" {
			t.Errorf("%s: body %s", role, w.Body)
		}
		if body.CanReset != want[0] || body.CanEnable != want[1] {
			t.Errorf("%s: canReset=%v canEnable=%v", role, body.CanReset, body.CanEnable)
		}
	}
}

func TestQueryStatsHandler_RefusesBadInput(t *testing.T) {
	f := newQueryStatsFixture(t, domain.OrgRoleViewer)
	for _, path := range []string{"/?sort=calls", "/?limit=ten"} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

// What failed on the platform's side is logged, never answered.
func TestQueryStatsHandler_HidesAFailedRead(t *testing.T) {
	f := newQueryStatsFixture(t, domain.OrgRoleViewer)
	f.mock.ExecOutput["org1-test-db/test-db-postgres-1"] = "not json at all"
	w := f.do(http.MethodGet, "/")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "json") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestQueryStatsHandler_ResetOfDatabaseWithoutStatisticsIsAConflict(t *testing.T) {
	f := newQueryStatsFixture(t, domain.OrgRoleDeveloper)
	f.mock.ExecOutput["org1-test-db/test-db-postgres-1"] = ""
	if w := f.do(http.MethodPost, "/reset"); w.Code != http.StatusConflict {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	f.mock.ExecOutput["org1-test-db/test-db-postgres-1"] = "reset"
	if w := f.do(http.MethodPost, "/reset"); w.Code != http.StatusNoContent {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestQueryStatsHandler_EnableTurnsTheClusterOn(t *testing.T) {
	f := newQueryStatsFixture(t, domain.OrgRoleAdmin)
	if w := f.do(http.MethodPost, "/enable"); w.Code != http.StatusAccepted {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	cluster, _ := f.mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, "org1-test-db", "test-db-postgres")
	if !k8s.ClusterHasQueryStats(cluster) {
		t.Error("cluster not changed")
	}
}

func TestQueryStatsHandler_APausedProjectIsAConflict(t *testing.T) {
	f := newQueryStatsFixture(t, domain.OrgRoleAdmin)
	inst, _ := f.store.FindByProjectID("test-db")
	inst.Status = string(domain.StatusPaused)
	if err := f.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	for _, request := range [][2]string{{http.MethodGet, "/"}, {http.MethodPost, "/reset"}, {http.MethodPost, "/enable"}} {
		if w := f.do(request[0], request[1]); w.Code != http.StatusConflict {
			t.Errorf("%s %s: %d", request[0], request[1], w.Code)
		}
	}
}

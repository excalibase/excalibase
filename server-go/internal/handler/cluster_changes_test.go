package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const clusterChangeProject = "/api/provision/test-db"

type clusterChangeFixture struct {
	router chi.Router
	store  *storage.FileSystemStore
	mock   *k8s.MockClient
	orgs   *fakestore.Orgs
	svc    *service.ProvisioningService
}

// newClusterChangeFixture is an ACTIVE FREE project whose cluster asks for
// 2Gi of the plan's 5Gi, in a FREE org.
func newClusterChangeFixture(t *testing.T) *clusterChangeFixture {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "test-db", OrgID: "org1", DBType: domain.PostgreSQL, Tier: domain.Free,
		Namespace: "org1-test-db", Status: "ACTIVE", PostgresVersion: "17",
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	mock := k8s.NewMockClient()
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "test-db", Namespace: "org1-test-db",
		Tier: config.TierConfig{Instances: 1, StorageSize: "2Gi", Memory: "512Mi", CPU: "0.5", StatementTimeout: "15s"},
	})
	if err := mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, "org1-test-db", cluster); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	mock.Capacity = k8s.ClusterCapacity{Nodes: []k8s.NodeCapacity{{Name: "roomy", AllocatableCPUMilli: 8000, AllocatableMemBytes: 32 << 30}}}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), mock)
	svc.SetOrgStore(orgs)
	h := NewProvisioningHandler(svc, &adminOrgStore{})
	h.SetInstanceStore(store)

	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}", func(r chi.Router) {
		r.Get("/cluster", h.GetClusterSettings)
		r.Post("/storage", h.ResizeStorage)
		r.Post("/tier", h.ChangeTier)
		r.Put("/parameters", h.TuneParameters)
	})
	return &clusterChangeFixture{router: r, store: store, mock: mock, orgs: orgs, svc: svc}
}

func (f *clusterChangeFixture) storageSize(t *testing.T) string {
	t.Helper()
	cluster, err := f.mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, "org1-test-db", "test-db-postgres")
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	size, _, _ := unstructured.NestedString(cluster.Object, "spec", "storage", "size")
	return size
}

func TestGetClusterSettingsReportsTheClusterAndThePlan(t *testing.T) {
	f := newClusterChangeFixture(t)
	w := doRequest(f.router, "GET", clusterChangeProject+"/cluster", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var got service.ClusterSettings
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.StorageSize != "2Gi" || got.StorageLimit != "5Gi" || got.Tier != domain.Free || got.OrgTier != domain.Free || len(got.TunableParameters) == 0 {
		t.Errorf("settings = %+v", got)
	}
}

func TestResizeStorageGrowsTheDiskAndAnswersTheNewSettings(t *testing.T) {
	f := newClusterChangeFixture(t)
	w := doRequest(f.router, "POST", clusterChangeProject+"/storage", `{"size":"4Gi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if f.storageSize(t) != "4Gi" || !strings.Contains(w.Body.String(), `"storageSize":"4Gi"`) {
		t.Errorf("cluster %s, body %s", f.storageSize(t), w.Body.String())
	}
}

func TestClusterChangeRefusalsAnswerWithTheirStatus(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		setup                    func(*clusterChangeFixture)
		want                     int
		says                     string
	}{
		{name: "shrink", method: "POST", path: "/storage", body: `{"size":"1Gi"}`, want: 400, says: "cannot be shrunk"},
		{name: "same size", method: "POST", path: "/storage", body: `{"size":"2Gi"}`, want: 400, says: "only grow"},
		{name: "bad size", method: "POST", path: "/storage", body: `{"size":"lots"}`, want: 400, says: "gibibytes"},
		{name: "above the plan", method: "POST", path: "/storage", body: `{"size":"6Gi"}`, want: 409, says: "plan allows"},
		{name: "unknown field", method: "POST", path: "/storage", body: `{"size":"4Gi","storageClass":"x"}`, want: 400},
		{name: "not json", method: "POST", path: "/storage", body: `{`, want: 400},
		{name: "volumes cannot grow", method: "POST", path: "/storage", body: `{"size":"4Gi"}`, want: 409, says: "cannot be expanded",
			setup: func(f *clusterChangeFixture) {
				f.mock.VolumeExpansionError = fmt.Errorf("%w: storage class \"secret-class\" does not allow volume expansion", k8s.ErrVolumeExpansionUnsupported)
			}},
		{name: "not the org's plan", method: "POST", path: "/tier", body: `{"tier":"ENTERPRISE"}`, want: 409, says: "FREE"},
		{name: "no such tier", method: "POST", path: "/tier", body: `{"tier":"GOLD"}`, want: 400},
		{name: "too few nodes", method: "POST", path: "/tier", body: `{"tier":"STANDARD"}`, want: 409, says: "node",
			setup: func(f *clusterChangeFixture) {
				f.orgs.AddOrg("org1", domain.Standard)
				f.mock.Capacity = k8s.ClusterCapacity{Nodes: []k8s.NodeCapacity{{Name: "one"}}}
			}},
		{name: "no room for the plan", method: "POST", path: "/tier", body: `{"tier":"FREE"}`, want: 409, says: "no room",
			setup: func(f *clusterChangeFixture) {
				f.mock.Capacity = k8s.ClusterCapacity{Nodes: []k8s.NodeCapacity{{Name: "full", AllocatableCPUMilli: 1000, RequestedCPUMilli: 1000}}}
			}},
		{name: "platform parameter", method: "PUT", path: "/parameters", body: `{"parameters":{"archive_command":"sh"}}`, want: 400, says: "set by the platform"},
		{name: "out of bounds", method: "PUT", path: "/parameters", body: `{"parameters":{"work_mem":"1GB"}}`, want: 400},
		{name: "no parameters object", method: "PUT", path: "/parameters", body: `{}`, want: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newClusterChangeFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			w := doRequest(f.router, tc.method, clusterChangeProject+tc.path, tc.body)
			if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.says) {
				t.Fatalf("got %d %s, want %d saying %q", w.Code, w.Body.String(), tc.want, tc.says)
			}
			if strings.Contains(w.Body.String(), "secret-class") {
				t.Error("the refusal named the platform's storage class")
			}
			if f.storageSize(t) != "2Gi" {
				t.Error("a refused change resized the disk")
			}
		})
	}
}

func TestClusterChangesOnAnInactiveProjectAnswerConflict(t *testing.T) {
	for _, status := range []string{"PAUSED", "PENDING_DELETION"} {
		for _, tc := range []struct{ method, path, body string }{
			{"POST", "/storage", `{"size":"4Gi"}`},
			{"POST", "/tier", `{"tier":"FREE"}`},
			{"PUT", "/parameters", `{"parameters":{"jit":"off"}}`},
		} {
			f := newClusterChangeFixture(t)
			inst, _ := f.store.FindByProjectID("test-db")
			inst.Status = status
			if err := f.store.Update(inst); err != nil {
				t.Fatalf("mark %s: %v", status, err)
			}
			if w := doRequest(f.router, tc.method, clusterChangeProject+tc.path, tc.body); w.Code != http.StatusConflict {
				t.Errorf("%s %s on %s: got %d %s, want 409", tc.method, tc.path, status, w.Code, w.Body.String())
			}
		}
	}
}

func TestClusterChangesOnADockerProjectAreBadRequests(t *testing.T) {
	f := newClusterChangeFixture(t)
	inst, _ := f.store.FindByProjectID("test-db")
	inst.DeploymentMode = domain.ModeDocker
	if err := f.store.Update(inst); err != nil {
		t.Fatalf("mark docker: %v", err)
	}
	if w := doRequest(f.router, "POST", clusterChangeProject+"/storage", `{"size":"4Gi"}`); w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

func TestClusterChangesWhileBusyAnswerConflict(t *testing.T) {
	f := newClusterChangeFixture(t)
	f.svc.SetOperationClaimer(busyClaimer{})
	if w := doRequest(f.router, "POST", clusterChangeProject+"/storage", `{"size":"4Gi"}`); w.Code != http.StatusConflict {
		t.Errorf("got %d, want 409", w.Code)
	}
}

// A failure on the platform's side is a 500 that tells the caller nothing
// about the cluster API behind it.
func TestClusterChangeFailuresDoNotLeakTheCause(t *testing.T) {
	f := newClusterChangeFixture(t)
	f.mock.UpdateCRDError = errors.New("etcdserver: leader changed at 10.0.0.7")
	w := doRequest(f.router, "PUT", clusterChangeProject+"/parameters", `{"parameters":{"jit":"off"}}`)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "10.0.0.7") {
		t.Errorf("got %d %s, want a 500 without the cause", w.Code, w.Body.String())
	}
}

func TestTuneParametersAppliesAndAnswersTheSettings(t *testing.T) {
	f := newClusterChangeFixture(t)
	w := doRequest(f.router, "PUT", clusterChangeProject+"/parameters", `{"parameters":{"work_mem":"8MB"}}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"work_mem":"8MB"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if inst, _ := f.store.FindByProjectID("test-db"); inst.Parameters["work_mem"] != "8MB" {
		t.Errorf("recorded parameters = %v", inst.Parameters)
	}
}

func TestChangeTierAppliesTheOrgsPlan(t *testing.T) {
	f := newClusterChangeFixture(t)
	w := doRequest(f.router, "POST", clusterChangeProject+"/tier", `{"tier":"FREE"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if f.storageSize(t) != "5Gi" {
		t.Errorf("disk = %s, want the FREE plan's 5Gi", f.storageSize(t))
	}
}

// Studio hides the changes from a caller who may not make them; the server
// still refuses them (route policy), this only says so up front.
func TestGetClusterSettingsSaysWhetherTheCallerMayChangeThem(t *testing.T) {
	f := newClusterChangeFixture(t)
	for _, tc := range []struct {
		name   string
		access *custommw.ProjectAccess
		want   bool
	}{
		{"viewer", &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleViewer}}, false},
		{"developer", &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleDeveloper}}, false},
		{"admin", &custommw.ProjectAccess{Member: &domain.OrgMember{Role: domain.OrgRoleAdmin}}, true},
		{"platform admin", &custommw.ProjectAccess{PlatformAdmin: true}, true},
		{"no resolved access", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", clusterChangeProject+"/cluster", nil)
			if tc.access != nil {
				req = req.WithContext(custommw.WithProjectAccess(req.Context(), tc.access))
			}
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			var got struct {
				CanChange   bool   `json:"canChange"`
				StorageSize string `json:"storageSize"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.StorageSize == "" {
				t.Fatalf("decode %s: %v", w.Body.String(), err)
			}
			if got.CanChange != tc.want {
				t.Errorf("canChange = %v, want %v", got.CanChange, tc.want)
			}
		})
	}
}

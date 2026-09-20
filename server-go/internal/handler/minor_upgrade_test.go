package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func setupUpgradeHandler(t *testing.T) (*chi.Mux, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	mock := k8s.NewMockClient()
	h := NewProvisioningHandler(service.NewProvisioningService(store, provisioner.NewFactory(), mock), nil)
	h.SetInstanceStore(store)

	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) { h.Routes(r) })
	return r, store, mock
}

func seedUpgradableProject(t *testing.T, store *storage.FileSystemStore, mock *k8s.MockClient, inst *domain.DatabaseInstance) {
	t.Helper()
	if err := store.Create(inst); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: inst.ProjectID, Namespace: inst.Namespace,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	if err := mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, inst.Namespace, cluster); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
}

func activeK8sProject(projectID, major string) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: projectID, OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.ModeK8s, Tier: domain.Free,
		Namespace: "o-" + projectID, PostgresVersion: major,
	}
}

func postUpgrade(t *testing.T, r *chi.Mux, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/provision/"+projectID+"/upgrade", nil))
	return w
}

// A minor upgrade takes the newest patch of the major the project already
// runs. The major itself is read off the project, never off the request, so
// there is no shape of call that moves a project between majors here.
func TestMinorUpgrade_StaysOnTheProjectsOwnMajor(t *testing.T) {
	r, store, mock := setupUpgradeHandler(t)
	seedUpgradableProject(t, store, mock, activeK8sProject("up-1", "16"))

	w := postUpgrade(t, r, "up-1")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}

	got, err := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, "o-up-1", "up-1-postgres")
	if err != nil {
		t.Fatalf("read cluster back: %v", err)
	}
	image, _ := got.Object["spec"].(map[string]interface{})["imageName"].(string)
	if image == "" {
		t.Fatal("cluster image was not re-pinned")
	}
	want, err := config.PostgresImage("16")
	if err == nil && image != want {
		t.Errorf("image: got %q, want the catalogue's %q", image, want)
	}

	stored, _ := store.FindByProjectID("up-1")
	if stored.PostgresVersion != "16" {
		t.Errorf("major moved: got %q, want 16", stored.PostgresVersion)
	}
}

// The response is the project as the platform now holds it, so a client shows
// what happened rather than what it hoped would happen.
func TestMinorUpgrade_AnswersWithTheStoredState(t *testing.T) {
	r, store, mock := setupUpgradeHandler(t)
	seedUpgradableProject(t, store, mock, activeK8sProject("up-2", "17"))

	w := postUpgrade(t, r, "up-2")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	stored, _ := store.FindByProjectID("up-2")
	if resp["projectId"] != "up-2" || resp["status"] != stored.Status || resp["postgresVersion"] != "17" {
		t.Errorf("response does not mirror the stored project: %v", resp)
	}
}

func TestMinorUpgrade_UnknownProjectIs404(t *testing.T) {
	r, _, _ := setupUpgradeHandler(t)
	if w := postUpgrade(t, r, "nope"); w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

// A project that never recorded a major cannot be told which patch is newest,
// and guessing one would put unchosen bits under a tenant's data.
func TestMinorUpgrade_ProjectWithoutARecordedMajorIsRefused(t *testing.T) {
	r, store, mock := setupUpgradeHandler(t)
	seedUpgradableProject(t, store, mock, activeK8sProject("up-3", ""))

	before := clusterImage(t, mock, "o-up-3", "up-3-postgres")

	w := postUpgrade(t, r, "up-3")
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if after := clusterImage(t, mock, "o-up-3", "up-3-postgres"); after != before {
		t.Errorf("cluster was patched anyway: %q -> %q", before, after)
	}
}

func clusterImage(t *testing.T, mock *k8s.MockClient, namespace, name string) string {
	t.Helper()
	got, err := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, namespace, name)
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	image, _ := got.Object["spec"].(map[string]interface{})["imageName"].(string)
	return image
}

func TestMinorUpgrade_NonActiveProjectIsRefused(t *testing.T) {
	r, store, mock := setupUpgradeHandler(t)
	inst := activeK8sProject("up-4", "16")
	inst.Status = string(domain.StatusPaused)
	seedUpgradableProject(t, store, mock, inst)

	if w := postUpgrade(t, r, "up-4"); w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", w.Code)
	}
}

// The upgrade patches a CNPG Cluster. A docker project has none, so it is
// refused rather than silently doing nothing and reporting success.
func TestMinorUpgrade_DockerProjectIsRefused(t *testing.T) {
	r, store, mock := setupUpgradeHandler(t)
	inst := activeK8sProject("up-5", "16")
	inst.DeploymentMode = domain.ModeDocker
	seedUpgradableProject(t, store, mock, inst)

	if w := postUpgrade(t, r, "up-5"); w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

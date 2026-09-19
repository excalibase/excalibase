package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	testDeleteProject   = "del-state-1"
	testDeleteNamespace = "org1-del-state-1"
)

func setupDeleteStateRouter(t *testing.T) (chi.Router, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	mock := k8s.NewMockClient()
	mock.Namespaces[testDeleteNamespace] = true
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	h := NewProvisioningHandler(svc, &adminOrgStore{})

	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) { h.Routes(r) })
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: testDeleteProject, OrgID: "org1", DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s, Namespace: testDeleteNamespace, Status: "ACTIVE",
	}); err != nil {
		t.Fatal(err)
	}
	return r, store, mock
}

func deleteProject(t *testing.T, r chi.Router) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/provision/"+testDeleteProject, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// The finding as the API sees it: cleanup was denied, so the response must
// not claim the project was deleted.
func TestDeleteReportsFailureWhenNamespaceDeleteDenied(t *testing.T) {
	r, store, mock := setupDeleteStateRouter(t)
	mock.DeleteNamespaceError = errForbiddenNamespace

	w := deleteProject(t, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "success") {
		t.Errorf("response must not report success: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "forbidden") {
		t.Errorf("client-facing error must not leak cluster internals: %s", w.Body.String())
	}
	inst, _ := store.FindByProjectID(testDeleteProject)
	if inst == nil {
		t.Fatal("row must survive a failed teardown")
	}
	if inst.Status != string(domain.StatusDeleting) {
		t.Errorf("status = %q, want %q", inst.Status, domain.StatusDeleting)
	}
}

// GET reports the DELETING row so a caller can poll or retry — the progress
// mechanism provisioning already uses.
func TestGetStatusReportsDeletingProgress(t *testing.T) {
	r, _, mock := setupDeleteStateRouter(t)
	mock.DeleteNamespaceError = errForbiddenNamespace
	deleteProject(t, r)

	req := httptest.NewRequest(http.MethodGet, "/api/provision/"+testDeleteProject, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != string(domain.StatusDeleting) {
		t.Errorf("status field = %v, want %q", got["status"], domain.StatusDeleting)
	}
	if got["deletionStep"] != domain.DeletionStepDeleteResources {
		t.Errorf("deletionStep = %v, want %s", got["deletionStep"], domain.DeletionStepDeleteResources)
	}
}

// Retrying the same DELETE once the obstacle is gone completes the teardown.
func TestDeleteRetryCompletesAfterObstacleCleared(t *testing.T) {
	r, store, mock := setupDeleteStateRouter(t)
	mock.DeleteNamespaceError = errForbiddenNamespace
	if w := deleteProject(t, r); w.Code != http.StatusInternalServerError {
		t.Fatalf("first attempt status = %d, want 500", w.Code)
	}

	mock.DeleteNamespaceError = nil
	w := deleteProject(t, r)
	if w.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if inst, _ := store.FindByProjectID(testDeleteProject); inst != nil {
		t.Error("row should be gone once the teardown completed")
	}
}

// A project being torn down must not be served to the data plane as a live
// project — its database is on the way out.
func TestProjectInfoRefusesDeletingProject(t *testing.T) {
	r, store, mock := setupDeleteStateRouter(t)
	mock.DeleteNamespaceError = errForbiddenNamespace
	deleteProject(t, r)

	inst, _ := store.FindByProjectID(testDeleteProject)
	if inst == nil || inst.Status != string(domain.StatusDeleting) {
		t.Fatal("precondition: row must be DELETING")
	}

	h := NewProvisioningHandler(
		service.NewProvisioningService(store, provisioner.NewFactory(), nil), &adminOrgStore{})
	router := chi.NewRouter()
	router.Get("/internal/projects/{projectId}/info", h.GetProjectInfo)

	req := httptest.NewRequest(http.MethodGet, "/internal/projects/"+testDeleteProject+"/info", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a project being deleted", w.Code)
	}
}

// Credentials of a project under teardown must not be handed out.
func TestCredentialsRefusedForDeletingProject(t *testing.T) {
	r, _, mock := setupDeleteStateRouter(t)
	mock.DeleteNamespaceError = errForbiddenNamespace
	deleteProject(t, r)

	req := httptest.NewRequest(http.MethodGet, "/api/provision/"+testDeleteProject+"/credentials", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

// errForbiddenNamespace is the RBAC refusal the review reproduced.
var errForbiddenNamespace = errors.New(`namespaces "org1-del-state-1" is forbidden: User cannot delete resource`)

// inMemoryInstanceStore is defined in admin_test.go; RevokeOrg must not
// delete the org while a project it owns could not be torn down.
func TestRevokeOrgKeepsOrgWhenAProjectTeardownFails(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-ok":   {ProjectID: "proj-ok", OrgID: "org-9", DBType: domain.PostgreSQL, Namespace: "org-9-ok", Status: "ACTIVE"},
		"proj-stuck": {ProjectID: "proj-stuck", OrgID: "org-9", DBType: domain.PostgreSQL, Namespace: "org-9-stuck", Status: "ACTIVE"},
	}}
	mock := k8s.NewMockClient()
	mock.Namespaces["org-9-ok"] = true
	mock.Namespaces["org-9-stuck"] = true
	mock.StuckNamespaces["org-9-stuck"] = true
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	pgProv.SetDeletionPoller(provisioner.NewPoller(0, 0))
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(pgProv), mock)
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-9", Slug: "acme"}}
	h := NewAdminHandler(provSvc, store, orgStore, &captureAudit{}, nil, "", nil)

	router := chi.NewRouter()
	router.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/orgs/org-9?cascade=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207: %s", w.Code, w.Body.String())
	}
	if orgStore.deleted {
		t.Error("org must not be deleted while one of its projects is still up")
	}
	if strings.Contains(w.Body.String(), "namespace") {
		t.Errorf("failure list must not leak cluster internals: %s", w.Body.String())
	}
	if inst, _ := store.FindByProjectID("proj-stuck"); inst == nil || inst.Status != string(domain.StatusDeleting) {
		t.Error("the stuck project must stay on record as DELETING")
	}
}

// A store that cannot be listed must abort the cascade: deleting the org
// while its projects are unknown orphans every one of them.
func TestRevokeOrgRefusesWhenProjectsCannotBeListed(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}, findAllErr: errors.New("db down")}
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-9", Slug: "acme"}}
	h := NewAdminHandler(provSvc, store, orgStore, &captureAudit{}, nil, "", nil)

	router := chi.NewRouter()
	router.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/orgs/org-9?cascade=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if orgStore.deleted {
		t.Error("org must not be deleted when its projects could not be listed")
	}
}

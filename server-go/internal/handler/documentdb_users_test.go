package handler

import (
	"context"
	"encoding/json"
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The Mongo users API (EXC-427): the password is in the create and rotate
// answers and nowhere else, and each refusal has its own status.

const mongoUsersPath = "/api/provision/doc-proj/documentdb/users"

func mongoUsersRouter(t *testing.T, documentDB bool) chi.Router {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "doc-proj", OrgID: "org1", Status: "ACTIVE",
		Namespace: "org1-doc-proj", Username: "owner", PostgresVersion: "17", DocumentDB: documentDB}); err != nil {
		t.Fatal(err)
	}
	kube := k8s.NewMockClient()
	_ = kube.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, "org1-doc-proj", &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     "Cluster",
		"metadata": map[string]interface{}{"name": "doc-proj-postgres"},
		"spec": map[string]interface{}{"postgresql": map[string]interface{}{
			"pg_ident": []interface{}{"local postgres documentdb", "local postgres owner"},
		}},
		"status": map[string]interface{}{"currentPrimary": "doc-proj-postgres-1"},
	}})
	kube.ExecOutput["org1-doc-proj/doc-proj-postgres-1"] = "t"
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), kube)
	svc.SetVault(newFakeVault())
	h := NewProvisioningHandler(svc, &adminOrgStore{})
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/documentdb/users", h.MongoUserRoutes)
	return r
}

func serveMongo(r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func TestCreatingAMongoUserAnswersThePasswordOnce(t *testing.T) {
	r := mongoUsersRouter(t, true)

	w := serveMongo(r, http.MethodPost, mongoUsersPath, `{"username":"reporting","role":"read"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var cred service.MongoUserCredential
	if err := json.NewDecoder(w.Body).Decode(&cred); err != nil || cred.Password == "" || cred.Username != "reporting" {
		t.Fatalf("create answer: %+v %v", cred, err)
	}

	list := serveMongo(r, http.MethodGet, mongoUsersPath, "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"reporting"`) {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), cred.Password) || strings.Contains(list.Body.String(), "password") {
		t.Errorf("the listing carries a password: %s", list.Body.String())
	}

	rotated := serveMongo(r, http.MethodPost, mongoUsersPath+"/reporting/rotate", "")
	if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), `"password"`) {
		t.Fatalf("rotate: %d %s", rotated.Code, rotated.Body.String())
	}
	if deleted := serveMongo(r, http.MethodDelete, mongoUsersPath+"/reporting", ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestMongoUserRefusalsHaveTheirOwnStatus(t *testing.T) {
	r := mongoUsersRouter(t, true)
	if w := serveMongo(r, http.MethodPost, mongoUsersPath, `{"username":"reporting","role":"read"}`); w.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"bad body", http.MethodPost, mongoUsersPath, `{`, http.StatusBadRequest},
		{"bad name", http.MethodPost, mongoUsersPath, `{"username":"Bad;Name","role":"read"}`, http.StatusBadRequest},
		{"reserved name", http.MethodPost, mongoUsersPath, `{"username":"postgres","role":"read"}`, http.StatusBadRequest},
		{"bad role", http.MethodPost, mongoUsersPath, `{"username":"other","role":"root"}`, http.StatusBadRequest},
		{"duplicate", http.MethodPost, mongoUsersPath, `{"username":"reporting","role":"read"}`, http.StatusConflict},
		{"rotate unknown", http.MethodPost, mongoUsersPath + "/nobody/rotate", "", http.StatusNotFound},
		{"delete unknown", http.MethodDelete, mongoUsersPath + "/nobody", "", http.StatusNotFound},
	}
	for _, tc := range cases {
		if w := serveMongo(r, tc.method, tc.path, tc.body); w.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestMongoUsersOnAPlainProjectAreAConflict(t *testing.T) {
	r := mongoUsersRouter(t, false)
	if w := serveMongo(r, http.MethodGet, mongoUsersPath, ""); w.Code != http.StatusConflict {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}
	if w := serveMongo(r, http.MethodPost, mongoUsersPath, `{"username":"reporting","role":"read"}`); w.Code != http.StatusConflict {
		t.Errorf("create: %d %s", w.Code, w.Body.String())
	}
}

func TestMongoUserFailuresAreThePlatformsWithoutDetail(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "doc-proj", OrgID: "org1", Status: "ACTIVE",
		Namespace: "org1-doc-proj", Username: "owner", PostgresVersion: "17", DocumentDB: true}); err != nil {
		t.Fatal(err)
	}
	unwired := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/documentdb/users", NewProvisioningHandler(unwired, &adminOrgStore{}).MongoUserRoutes)
	if w := serveMongo(r, http.MethodGet, mongoUsersPath, ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no vault or cluster: %d %s", w.Code, w.Body.String())
	}

	// No cluster object to read the primary from: an internal failure, reported without its detail.
	kube := k8s.NewMockClient()
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), kube)
	svc.SetVault(newFakeVault())
	r = chi.NewRouter()
	r.Route("/api/provision/{projectId}/documentdb/users", NewProvisioningHandler(svc, &adminOrgStore{}).MongoUserRoutes)
	w := serveMongo(r, http.MethodPost, mongoUsersPath, `{"username":"reporting","role":"read"}`)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "org1-doc-proj") {
		t.Errorf("internal failure: %d %s", w.Code, w.Body.String())
	}
}

func TestAMongoUserChangeOnAPausedProjectIsAConflict(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "doc-proj", OrgID: "org1", Status: string(domain.StatusPaused),
		Namespace: "org1-doc-proj", Username: "owner", PostgresVersion: "17", DocumentDB: true}); err != nil {
		t.Fatal(err)
	}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), k8s.NewMockClient())
	svc.SetVault(newFakeVault())
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/documentdb/users", NewProvisioningHandler(svc, &adminOrgStore{}).MongoUserRoutes)
	if w := serveMongo(r, http.MethodPost, mongoUsersPath, `{"username":"reporting","role":"read"}`); w.Code != http.StatusConflict {
		t.Errorf("paused project: %d %s", w.Code, w.Body.String())
	}
}

package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

func singleHostLogsRouter(t *testing.T) chi.Router {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "host-a", OrgID: "org1", Namespace: "container-id",
		DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Status: "ACTIVE", Tier: domain.Free}); err != nil {
		t.Fatal(err)
	}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	r := chi.NewRouter()
	r.Get("/api/provision/{projectId}/logs", NewProvisioningHandler(svc, nil).GetLogs)
	return r
}

// A single host whose log search is off is not a failure: the answer says the
// search is not offered, in words that name no Kubernetes.
func TestGetLogs_ASingleHostWithoutLogSearchSaysItIsNotOffered(t *testing.T) {
	w := httptest.NewRecorder()
	singleHostLogsRouter(t).ServeHTTP(w, httptest.NewRequest("GET", "/api/provision/host-a/logs", nil))
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("body: %v", err)
	}
	notOffered, _ := resp["notOffered"].(string)
	if w.Code != 200 || resp["logs"] != "" || !strings.Contains(notOffered, "single host") || strings.Contains(strings.ToLower(w.Body.String()), "kubernetes") {
		t.Fatalf("got %d %v", w.Code, resp)
	}
}

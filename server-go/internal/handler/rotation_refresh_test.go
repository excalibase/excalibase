package handler

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func rotationRouter(t *testing.T, onRotated func(*http.Request, string)) (chi.Router, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	mock := k8s.NewMockClient()
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(), mock)
	provSvc.SetVault(newFakeVault())
	provSvc.SetCredentialVerifier(acceptingRoleVerifier{})
	h := NewProvisioningHandler(provSvc, &adminOrgStore{})
	h.SetCredentialsRotatedHook(onRotated)
	r := chi.NewRouter()
	r.Post("/api/provision/{projectId}/credentials/rotate", h.RotateCredentials)
	return r, store, mock
}

// The function runtime holds the owner's login in its pool; a rotation tells
// it, or ctx.db keeps the old password until something else rolls the pod (EXC-625).
func TestRotateCredentials_TellsTheFunctionRuntime(t *testing.T) {
	var rotated []string
	r, store, mock := rotationRouter(t, func(_ *http.Request, projectID string) { rotated = append(rotated, projectID) })
	seedInstance(store, mock)

	if w := doRequest(r, "POST", "/api/provision/test-db/credentials/rotate", ""); w.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	if len(rotated) != 1 || rotated[0] != "test-db" {
		t.Fatalf("runtime told about %v, want [test-db]", rotated)
	}
}

func TestRotateCredentials_FailedRotationTellsNobody(t *testing.T) {
	called := false
	r, _, _ := rotationRouter(t, func(*http.Request, string) { called = true })
	if w := doRequest(r, "POST", "/api/provision/nonexistent/credentials/rotate", ""); w.Code == http.StatusOK {
		t.Fatal("rotating a missing project must fail")
	}
	if called {
		t.Fatal("a failed rotation must not touch the runtime")
	}
}

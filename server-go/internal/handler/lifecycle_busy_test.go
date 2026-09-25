package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// busyClaimer answers every lease request as already held by another operation.
type busyClaimer struct{}

func (busyClaimer) Claim(context.Context, string, service.ProjectOperation) (func(), bool, error) {
	return nil, false, nil
}

func busyLifecycleRouter(t *testing.T) chi.Router {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	mock := k8s.NewMockClient()
	seedInstance(store, mock)
	inst, _ := store.FindByProjectID("test-db")
	inst.PostgresVersion = "17"
	if err := store.Update(inst); err != nil {
		t.Fatalf("record major: %v", err)
	}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), mock)
	svc.SetVault(newFakeVault())
	svc.SetCredentialVerifier(acceptingRoleVerifier{})
	svc.SetOperationClaimer(busyClaimer{})
	h := NewProvisioningHandler(svc, &adminOrgStore{})
	h.SetInstanceStore(store)

	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}", func(r chi.Router) {
		r.Post("/credentials/rotate", h.RotateCredentials)
		r.Put("/maintenance-window", h.SetMaintenanceWindow)
		r.Post("/minor-upgrade", h.UpgradeMinorVersion)
	})
	return r
}

func TestLifecycleRoutesAnswerConflictWhileAnotherOperationHoldsTheProject(t *testing.T) {
	r := busyLifecycleRouter(t)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/provision/test-db/credentials/rotate", ""},
		{"PUT", "/api/provision/test-db/maintenance-window", `{"window":"sunday 02:00","durationMinutes":60}`},
		{"POST", "/api/provision/test-db/minor-upgrade", ""},
	} {
		w := doRequest(r, tc.method, tc.path, tc.body)
		if w.Code != http.StatusConflict {
			t.Errorf("%s %s: got %d, want 409 (body %s)", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestUnsettledOrAnswersConflictForAnInactiveProject(t *testing.T) {
	err := fmt.Errorf("project p is PAUSED; %w", service.ErrProjectNotActive)
	if got := unsettledOr(err, http.StatusInternalServerError); got != http.StatusConflict {
		t.Errorf("got %d, want 409", got)
	}
	if got := unsettledOr(errors.New("boom"), http.StatusInternalServerError); got != http.StatusInternalServerError {
		t.Errorf("got %d, want the fallback", got)
	}
}

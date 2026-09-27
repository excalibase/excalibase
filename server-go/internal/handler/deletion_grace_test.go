package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const graceHandlerProject = "grace-h"

type pausingStub struct {
	store *storage.FileSystemStore
	err   error
}

func (p *pausingStub) Pause(_ context.Context, projectID, _ string) error {
	if p.err != nil {
		return p.err
	}
	inst, _ := p.store.FindByProjectID(projectID)
	inst.Status = string(domain.StatusPaused)
	return p.store.Update(inst)
}

func graceRouter(t *testing.T, pauser service.DeletionPauser) (chi.Router, *storage.FileSystemStore) {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	mock := k8s.NewMockClient()
	mock.SetupPostgreSQLMock(graceHandlerProject, "org1-"+graceHandlerProject, 1)
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	svc.SetVault(newFakeVault())
	withBackupTarget(t, svc)
	if stub, ok := pauser.(*pausingStub); ok {
		stub.store = store
	}
	if pauser != nil {
		svc.SetDeletionPauser(pauser)
	}
	_ = store.Create(&domain.DatabaseInstance{
		ProjectID: graceHandlerProject, OrgID: "org1", DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s, Namespace: "org1-" + graceHandlerProject, Status: "ACTIVE",
	})
	r := chi.NewRouter()
	r.Route("/api/provision", NewProvisioningHandler(svc, nil).Routes)
	return r, store
}

func TestDeleteSchedulesAProjectHoldingData(t *testing.T) {
	r, store := graceRouter(t, &pausingStub{})

	w := doRequest(r, "DELETE", "/api/provision/"+graceHandlerProject, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("delete: %d %s, want 202", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != string(domain.StatusPendingDeletion) || body["deletionDueAt"] == nil {
		t.Fatalf("body = %v", body)
	}
	if inst, _ := store.FindByProjectID(graceHandlerProject); inst == nil || inst.Status != string(domain.StatusPendingDeletion) {
		t.Fatal("the row must stay, PENDING_DELETION")
	}
}

func TestDeleteWithoutAPauserIsRefusedAndNothingChanges(t *testing.T) {
	r, store := graceRouter(t, nil)
	w := doRequest(r, "DELETE", "/api/provision/"+graceHandlerProject, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
	if inst, _ := store.FindByProjectID(graceHandlerProject); inst.Status != "ACTIVE" {
		t.Fatal("project must be untouched")
	}
}

func TestDeleteThatCannotStopTheProjectSaysSo(t *testing.T) {
	r, store := graceRouter(t, &pausingStub{err: errors.New("backup did not complete")})
	w := doRequest(r, "DELETE", "/api/provision/"+graceHandlerProject, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != errDeletionStopFailed {
		t.Fatalf("error = %q", body["error"])
	}
	if inst, _ := store.FindByProjectID(graceHandlerProject); inst == nil {
		t.Fatal("nothing may be deleted")
	}
}

func TestCancelDeletionRoute(t *testing.T) {
	r, store := graceRouter(t, &pausingStub{})
	if w := doRequest(r, "POST", "/api/provision/"+graceHandlerProject+"/deletion/cancel", ""); w.Code != http.StatusConflict {
		t.Fatalf("cancel with nothing scheduled: %d, want 409", w.Code)
	}
	doRequest(r, "DELETE", "/api/provision/"+graceHandlerProject, "")

	w := doRequest(r, "POST", "/api/provision/"+graceHandlerProject+"/deletion/cancel", "")
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if inst, _ := store.FindByProjectID(graceHandlerProject); inst.Status != string(domain.StatusPaused) {
		t.Fatalf("status %s, want PAUSED", inst.Status)
	}
	if w := doRequest(r, "POST", "/api/provision/nope/deletion/cancel", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d, want 404", w.Code)
	}
}

func TestProtectionCannotBeToggledWhileScheduled(t *testing.T) {
	r, _ := graceRouter(t, &pausingStub{})
	doRequest(r, "DELETE", "/api/provision/"+graceHandlerProject, "")
	w := doRequest(r, "PATCH", "/api/provision/"+graceHandlerProject+"/deletion-protection", `{"enabled":true}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", w.Code)
	}
}

// The due date is an instant: it carries its zone, so a browser in any time
// zone reads the same moment.
func TestDeletionDueAtCarriesItsZone(t *testing.T) {
	r, _ := graceRouter(t, &pausingStub{})
	w := doRequest(r, "DELETE", "/api/provision/"+graceHandlerProject, "")
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	raw, _ := body["deletionDueAt"].(string)
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
		t.Fatalf("deletionDueAt %q is not RFC 3339 with a zone: %v", raw, err)
	}
}

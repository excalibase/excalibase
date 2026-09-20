package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// newRestoringOnlyStore holds one project, for the gates that take a store
// rather than a router.
func newRestoringOnlyStore(inst *domain.DatabaseInstance) storage.InstanceStore {
	instances := fakestore.NewInstances()
	instances.Create(inst)
	return instances
}

// Everything that hands out a way to reach a project's database must refuse
// one whose restore has not been confirmed. The credentials open a database
// that may never have recovered; /info is what the data plane mints JWTs and
// routes traffic from.
func TestRestoringProjectIsNotServedByTheDataPlane(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)
	inst, err := store.FindByProjectID("test-db")
	if err != nil || inst == nil {
		t.Fatalf("seeded project missing: %v", err)
	}
	inst.Status = string(domain.StatusRestoring)
	if err := store.Update(inst); err != nil {
		t.Fatalf("set RESTORING: %v", err)
	}

	if w := doRequest(r, "GET", "/api/projects/test-db/info", ""); w.Code != http.StatusNotFound {
		t.Errorf("/info: got %d, want 404; body=%s", w.Code, w.Body.String())
	}
	if w := doRequest(r, "GET", "/api/provision/test-db/credentials", ""); w.Code != http.StatusConflict {
		t.Errorf("/credentials: got %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

// Routes outside the central gate carry the same rule themselves.
func TestFunctionRoutesRefuseARestoringProject(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)
	inst, _ := store.FindByProjectID("test-db")
	inst.Status = string(domain.StatusRestoring)
	if err := store.Update(inst); err != nil {
		t.Fatalf("set RESTORING: %v", err)
	}

	w := doRequest(r, "POST", "/api/projects/test-db/functions", `{"name":"f","code":"x"}`)
	if w.Code == http.StatusOK || w.Code == http.StatusCreated {
		t.Errorf("a function must not be deployed into a project being restored, got %d", w.Code)
	}
}

func TestOrgProjectMemberWritesRefuseARestoringProject(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "p-restoring", OrgID: "o", Status: string(domain.StatusRestoring)}
	w := httptest.NewRecorder()
	store := newRestoringOnlyStore(inst)

	if !refuseWhileNotServable(w, store, "p-restoring") {
		t.Fatal("membership writes must be refused while a restore is unconfirmed")
	}
	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "being restored") {
		t.Errorf("body must name the restore: %s", w.Body.String())
	}
}

func TestRefuseWhileNotServableLetsALiveProjectThrough(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "p-live", OrgID: "o", Status: "ACTIVE"}
	w := httptest.NewRecorder()
	if refuseWhileNotServable(w, newRestoringOnlyStore(inst), "p-live") {
		t.Error("a live project must not be refused")
	}
}

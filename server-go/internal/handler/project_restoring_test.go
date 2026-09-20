package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
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

// In shared-runtime (docker) mode runtimeClientFor handed back the shared
// client before anything looked at the project, so a RESTORING or DELETING
// project's functions kept running.
func TestSharedRuntimeRefusesAProjectThatIsNotServable(t *testing.T) {
	for name, status := range map[string]string{
		"restoring": string(domain.StatusRestoring),
		"deleting":  string(domain.StatusDeleting),
	} {
		t.Run(name, func(t *testing.T) {
			h := sharedRuntimeHandler(&domain.DatabaseInstance{
				ProjectID: "proj-f", OrgID: "o", Status: status, Namespace: "ns",
			})

			if _, err := h.runtimeClientFor(context.Background(), "proj-f"); err == nil {
				t.Fatal("the shared runtime must not serve a project the platform may not serve")
			}
		})
	}
}

func TestSharedRuntimeStillServesALiveProject(t *testing.T) {
	h := sharedRuntimeHandler(&domain.DatabaseInstance{
		ProjectID: "proj-f", OrgID: "o", Status: "ACTIVE", Namespace: "ns",
	})

	if _, err := h.runtimeClientFor(context.Background(), "proj-f"); err != nil {
		t.Fatalf("a live project must still reach the shared runtime: %v", err)
	}
}

// sharedRuntimeHandler is a function handler in shared-runtime (docker) mode
// — one runtime client for every project, no per-project k8s provisioning.
func sharedRuntimeHandler(inst *domain.DatabaseInstance) *FunctionHandler {
	return NewFunctionHandler(nil, nil, edgefn.NewRuntimeClient("http://runtime.invalid", ""),
		newRestoringOnlyStore(inst), nil, "")
}

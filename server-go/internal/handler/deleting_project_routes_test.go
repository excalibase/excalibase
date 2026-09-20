package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// Routes that carry a project id but sit outside the project-access gate
// check for themselves. These are the ones that would otherwise act on a
// project being torn down.
func TestRefuseWhileDeleting(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: "live", Status: "ACTIVE"})
	instances.Create(&domain.DatabaseInstance{ProjectID: "gone", Status: string(domain.StatusDeleting)})
	instances.Create(&domain.DatabaseInstance{ProjectID: "purging", Status: string(domain.StatusBackupsPendingDelete)})

	cases := []struct {
		name      string
		projectID string
		refused   bool
	}{
		{"live project passes", "live", false},
		{"deleting project refused", "gone", true},
		{"pending-purge project refused", "purging", true},
		{"unknown project passes (the caller's own 404 follows)", "missing", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if got := refuseWhileNotServable(w, instances, tc.projectID); got != tc.refused {
				t.Fatalf("refuseWhileNotServable = %v, want %v", got, tc.refused)
			}
			if tc.refused && w.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409", w.Code)
			}
		})
	}
}

// Without an instance store the check cannot answer, and must not invent one.
func TestRefuseWhileDeletingWithoutAStore(t *testing.T) {
	w := httptest.NewRecorder()
	if refuseWhileNotServable(w, nil, "gone") {
		t.Error("an unwired store must not refuse the request")
	}
}

// A store that cannot be read must not be treated as "project is deleting"
// nor as "project is fine" — the caller's own lookup reports the failure.
func TestRefuseWhileDeletingWhenTheStoreFails(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Err = errStoreDown
	w := httptest.NewRecorder()
	if refuseWhileNotServable(w, instances, "gone") {
		t.Error("a failed lookup must fall through to the caller")
	}
}

var errStoreDown = errStore{}

type errStore struct{}

func (errStore) Error() string { return "store unavailable" }

// Invoking a project a teardown owns is refused where the handler already
// resolves the project — not by a lookup keyed on the public route's
// attacker-controlled path parameter.
func TestInvokeRefusesADeletingProject(t *testing.T) {
	h, instances := invokeHandler(t, string(domain.StatusDeleting))
	if _, err := h.runtimeClientFor(context.Background(), "proj_p1"); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("runtimeClientFor = %v, want ErrProjectDeleting", err)
	}
	if instances.reads == 0 {
		t.Error("the cold path is expected to read the project it is resolving")
	}
}

// An unknown project id never reaches the platform database through the
// invoke route: the function lookup answers first, so a public caller
// cannot make the handler read (or remember) arbitrary ids.
func TestInvokeOfAnUnknownProjectNeverReadsTheInstanceStore(t *testing.T) {
	h, instances := invokeHandler(t, "ACTIVE")
	r := chi.NewRouter()
	r.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/functions/v1/proj_unknown%d/fn1", i), nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("unknown project %d: got %d, want 404", i, w.Code)
		}
	}
	if instances.reads != 0 {
		t.Fatalf("instance-store reads for unknown projects = %d, want 0", instances.reads)
	}
}

// A warm project is served without any platform-database read, and the
// teardown claim is what makes the next invocation check again.
func TestClaimingAProjectDropsItsWarmRuntimeClient(t *testing.T) {
	h, _ := invokeHandler(t, "ACTIVE")
	h.clientMu.Lock()
	h.clients["proj_p1"] = &edgefn.RuntimeClient{}
	h.clientMu.Unlock()

	h.ProjectDeleting("proj_p1")

	h.clientMu.Lock()
	_, warm := h.clients["proj_p1"]
	h.clientMu.Unlock()
	if warm {
		t.Fatal("a claimed project must lose its cached runtime client so the next call re-checks")
	}
}

// invokeHandler builds a function handler over a counted instance store.
func invokeHandler(t *testing.T, status string) (*FunctionHandler, *countingInstances) {
	t.Helper()
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default", Namespace: "ns-1", Status: status},
	}}
	counting := &countingInstances{InstanceStore: instStore}
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()),
		edgefn.NewSecretsStore(newFakeVault()), nil, counting, nil, testAPIBase)
	h.SetK8sClient(k8s.NewMockClient(), "img", "secret")
	return h, counting
}

// Each refusal reason the delete endpoint can hit maps to its own status, so
// a caller can tell "I asked for something impossible" from "the teardown
// stopped" from "someone else is already doing it".
func TestWriteDeprovisionErrorStatuses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unknown project", fmt.Errorf("%w: p", service.ErrProjectNotFound), http.StatusNotFound},
		{"no purger wired", service.ErrBackupPurgeNotConfigured, http.StatusBadRequest},
		{"deletion protection", fmt.Errorf("%w for p", service.ErrDeletionProtected), http.StatusBadRequest},
		{"already running", fmt.Errorf("%w: p", service.ErrDeletionInProgress), http.StatusConflict},
		{"purge already confirmed", fmt.Errorf("%w: p", storage.ErrBackupPurgeAlreadyConfirmed), http.StatusConflict},
		{"teardown stopped", errors.New("delete namespace: forbidden"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeDeprovisionError(w, tc.err)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.want == http.StatusInternalServerError && strings.Contains(w.Body.String(), "forbidden") {
				t.Errorf("client-facing error leaks internals: %s", w.Body.String())
			}
		})
	}
}

// A DELETE while the project is still being built answers 409 naming the
// state, so the caller knows to retry rather than that the request was wrong.
func TestDeleteOfABuildingProjectIs409(t *testing.T) {
	w := httptest.NewRecorder()
	writeDeprovisionError(w, fmt.Errorf("%w: proj-1 is %s", storage.ErrProjectBusy, domain.StatusProvisioning))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), domain.StatusProvisioning) {
		t.Errorf("the response must name the state: %s", w.Body.String())
	}
}

// An unrecognised busy state still answers 409 without echoing the error.
func TestBusyStateFallsBackToAGenericPhrase(t *testing.T) {
	if got := busyState(fmt.Errorf("%w: proj-1 is WEIRD", storage.ErrProjectBusy)); got == "WEIRD" {
		t.Error("the response must not echo an unrecognised state back")
	}
}

// countingInstances records how often a path reaches the platform database.
type countingInstances struct {
	storage.InstanceStore
	reads int
}

func (s *countingInstances) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	s.reads++
	return s.InstanceStore.FindByProjectID(projectID)
}

// Resolving a runtime must never guess: an unknown project, an unreadable
// store and a missing instance store are all refusals, not a namespace.
func TestInstanceForRefusesWhatItCannotResolve(t *testing.T) {
	h, _ := invokeHandler(t, "ACTIVE")
	if _, err := h.instanceFor("proj_missing"); err == nil {
		t.Error("an unknown project must not resolve to a runtime")
	}

	failing := fakestore.NewInstances()
	failing.Err = errStoreDown
	h.instanceStore = failing
	if _, err := h.instanceFor("proj_p1"); err == nil {
		t.Error("an unreadable store must not resolve to a runtime")
	}

	h.instanceStore = nil
	if _, err := h.instanceFor("proj_p1"); err == nil {
		t.Error("without an instance store nothing may be resolved")
	}
}

// A live project resolves to its recorded namespace.
func TestInstanceForResolvesALiveProject(t *testing.T) {
	h, _ := invokeHandler(t, "ACTIVE")
	inst, err := h.instanceFor("proj_p1")
	if err != nil {
		t.Fatalf("instanceFor: %v", err)
	}
	if inst.Namespace != "ns-1" {
		t.Errorf("namespace = %q, want ns-1", inst.Namespace)
	}
}

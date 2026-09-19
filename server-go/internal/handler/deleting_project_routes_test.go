package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
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
			if got := refuseWhileDeleting(w, instances, tc.projectID); got != tc.refused {
				t.Fatalf("refuseWhileDeleting = %v, want %v", got, tc.refused)
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
	if refuseWhileDeleting(w, nil, "gone") {
		t.Error("an unwired store must not refuse the request")
	}
}

// A store that cannot be read must not be treated as "project is deleting"
// nor as "project is fine" — the caller's own lookup reports the failure.
func TestRefuseWhileDeletingWhenTheStoreFails(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Err = errStoreDown
	w := httptest.NewRecorder()
	if refuseWhileDeleting(w, instances, "gone") {
		t.Error("a failed lookup must fall through to the caller")
	}
}

var errStoreDown = errStore{}

type errStore struct{}

func (errStore) Error() string { return "store unavailable" }

// Public function invocation carries a project id but sits outside the
// project-access gate: invoking into a namespace being removed would fail
// with a runtime error instead of saying why.
func TestPublicInvokeRefusesADeletingProject(t *testing.T) {
	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default", Status: string(domain.StatusDeleting)},
	}}
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), nil, instStore, nil, testAPIBase)

	r := chi.NewRouter()
	r.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	r.HandleFunc("/functions/v1/{projectId}/http/*", h.PublicHttpInvoke)

	for _, path := range []string{"/functions/v1/proj_p1/fn1", "/functions/v1/proj_p1/http/anything"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Errorf("POST %s: got %d, want 409 (body=%s)", path, w.Code, w.Body.String())
		}
	}
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

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// workloadStub stops and starts a database at once.
type workloadStub struct{}

func (workloadStub) StopReplication(context.Context, string, string) error         { return nil }
func (workloadStub) Pause(context.Context, string, string) error                   { return nil }
func (workloadStub) Resume(context.Context, string, string) error                  { return nil }
func (workloadStub) WorkloadStopped(context.Context, string, string) (bool, error) { return true, nil }

// lifecycleAsyncRouter serves pause, resume and delete over a real pause
// service whose background runs the test starts itself.
func lifecycleAsyncRouter(t *testing.T, status string) (chi.Router, *storage.FileSystemStore, *[]func()) {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	mock := k8s.NewMockClient()
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	pause := service.NewPauseService(service.PauseServiceConfig{
		Instances: store,
		Pausers:   map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: workloadStub{}},
	})
	runs := &[]func(){}
	pause.SetBackgroundRunner(func(run func()) { *runs = append(*runs, run) })
	svc.SetDeletionPauser(pause)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: graceHandlerProject, OrgID: "org1", DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeK8s, Namespace: "org1-" + graceHandlerProject, Status: status,
	}); err != nil {
		t.Fatal(err)
	}
	h := NewProvisioningHandler(svc, nil)
	h.SetPauseService(pause)
	h.SetInstanceStore(store)
	r := chi.NewRouter()
	r.Route("/api/provision", h.Routes)
	return r, store, runs
}

func doRespondAsync(r chi.Router, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Prefer", "respond-async")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func answeredStatus(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	status, _ := body["status"].(string)
	return status
}

func rowStatus(store *storage.FileSystemStore) string {
	inst, _ := store.FindByProjectID(graceHandlerProject)
	return inst.Status
}

// EXC-473: Studio prefers not to wait for a pause (backup first), a resume or
// a deletion's stop; each is answered 202 once it is recorded on the project.
func TestProjectPause_PreferRespondAsyncAnswers202OncePausing(t *testing.T) {
	r, store, runs := lifecycleAsyncRouter(t, "ACTIVE")
	w := doRespondAsync(r, http.MethodPost, "/api/provision/"+graceHandlerProject+"/pause")
	if w.Code != http.StatusAccepted || answeredStatus(t, w) != string(domain.StatusPausing) {
		t.Fatalf("pause: %d %s, want 202 PAUSING", w.Code, w.Body.String())
	}
	(*runs)[0]()
	if got := rowStatus(store); got != string(domain.StatusPaused) {
		t.Fatalf("after the run: %s, want PAUSED", got)
	}
}

func TestProjectResume_PreferRespondAsyncAnswers202OnceResuming(t *testing.T) {
	r, store, runs := lifecycleAsyncRouter(t, string(domain.StatusPaused))
	w := doRespondAsync(r, http.MethodPost, "/api/provision/"+graceHandlerProject+"/resume")
	if w.Code != http.StatusAccepted || answeredStatus(t, w) != string(domain.StatusResuming) {
		t.Fatalf("resume: %d %s, want 202 RESUMING", w.Code, w.Body.String())
	}
	(*runs)[0]()
	if got := rowStatus(store); got != "ACTIVE" {
		t.Fatalf("after the run: %s, want ACTIVE", got)
	}
}

// Nothing to do is answered at once with the project as it is.
func TestProjectPause_PreferRespondAsyncOnAPausedProjectAnswersAtOnce(t *testing.T) {
	r, _, runs := lifecycleAsyncRouter(t, string(domain.StatusPaused))
	w := doRespondAsync(r, http.MethodPost, "/api/provision/"+graceHandlerProject+"/pause")
	if w.Code != http.StatusOK || answeredStatus(t, w) != string(domain.StatusPaused) || len(*runs) != 0 {
		t.Fatalf("pause of a paused project: %d %s, %d runs; want 200 PAUSED and none", w.Code, w.Body.String(), len(*runs))
	}
}

func TestProjectDelete_PreferRespondAsyncAnswers202WhileTheProjectStops(t *testing.T) {
	r, store, runs := lifecycleAsyncRouter(t, "ACTIVE")
	w := doRespondAsync(r, http.MethodDelete, "/api/provision/"+graceHandlerProject)
	if w.Code != http.StatusAccepted || answeredStatus(t, w) != string(domain.StatusPausing) {
		t.Fatalf("delete: %d %s, want 202 PAUSING", w.Code, w.Body.String())
	}
	(*runs)[0]()
	if got := rowStatus(store); got != string(domain.StatusPendingDeletion) {
		t.Fatalf("after the run: %s, want PENDING_DELETION", got)
	}
}

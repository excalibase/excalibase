package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	raceProjectID = "proj-race00001"
	raceNamespace = "org-race-proj-race00001"
)

// operatorRacingClient refuses the first write after every read of the
// Cluster, the way the API server does while the CNPG operator is writing the
// same object during a pause or a resume.
type operatorRacingClient struct {
	*k8s.MockClient
	refuseNext bool
}

func (c *operatorRacingClient) GetCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	obj, err := c.MockClient.GetCRD(ctx, gvr, namespace, name)
	if err != nil {
		return nil, err
	}
	return obj.DeepCopy(), nil
}

func (c *operatorRacingClient) UpdateCRD(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	c.refuseNext = !c.refuseNext
	if c.refuseNext {
		return apierrors.NewConflict(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource},
			obj.GetName(), errors.New("the object has been modified"))
	}
	return c.MockClient.UpdateCRD(ctx, gvr, namespace, obj)
}

func readyCluster() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]interface{}{"name": raceProjectID + "-postgres", "namespace": raceNamespace},
		"spec":       map[string]interface{}{"instances": int64(1)},
		"status":     map[string]interface{}{"readyInstances": int64(1)},
	}}
}

func setupK8sLifecycleHandler(t *testing.T, client k8s.KubeClient) (*chi.Mux, *storage.FileSystemStore) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: raceProjectID, OrgID: "org-race", Status: "ACTIVE",
		DeploymentMode: domain.ModeK8s, Tier: domain.Free, Namespace: raceNamespace,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	pauseSvc := service.NewPauseService(service.PauseServiceConfig{
		Claimer:   service.NewInProcessOperationClaimer(),
		Instances: store,
		Pausers:   map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: provisioner.NewPostgreSQLProvisioner(client, "")},
		Backups:   &fakeBackupTriggerForHandler{},
	})
	h := NewProvisioningHandler(service.NewProvisioningService(store, provisioner.NewFactory(), client), nil)
	h.SetPauseService(pauseSvc)
	h.SetInstanceStore(store)
	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) { h.Routes(r) })
	return r, store
}

func lifecycleStatus(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode body %s: %v", body, err)
	}
	return decoded.Status
}

// A pause that succeeded is followed by a resume that succeeds, even though
// the operator keeps rewriting the Cluster under both flips.
func TestPauseThenResumeAnswer200WhileTheOperatorRacesEveryFlip(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.CRDs[raceNamespace+"/"+raceProjectID+"-postgres"] = readyCluster()
	r, store := setupK8sLifecycleHandler(t, &operatorRacingClient{MockClient: mock})

	paused := doRequest(r, "POST", "/api/provision/"+raceProjectID+"/pause", `{"reason":"manual"}`)
	if paused.Code != http.StatusOK || lifecycleStatus(t, paused.Body.Bytes()) != string(domain.StatusPaused) {
		t.Fatalf("pause: got %d %s, want 200 PAUSED", paused.Code, paused.Body.String())
	}
	resumed := doRequest(r, "POST", "/api/provision/"+raceProjectID+"/resume", `{}`)
	if resumed.Code != http.StatusOK || lifecycleStatus(t, resumed.Body.Bytes()) != "ACTIVE" {
		t.Fatalf("resume: got %d %s, want 200 ACTIVE", resumed.Code, resumed.Body.String())
	}
	if inst, _ := store.FindByProjectID(raceProjectID); inst.Status != "ACTIVE" || inst.PauseReason != "" {
		t.Errorf("stored project: status %q pauseReason %q, want ACTIVE and no reason", inst.Status, inst.PauseReason)
	}
}

// A resume whose start could not be observed is a retryable conflict carrying
// the project's state, never a success and never a server fault.
func TestResumeThatIsNotObservedAnswersConflictWithTheProjectState(t *testing.T) {
	r, store, pauser, _ := setupPauseHandler(t)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "resume-db", OrgID: "o", Status: string(domain.StatusPaused),
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-resume-db",
		PauseReason: domain.PauseReasonManual,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	pauser.resumeErr = errors.New("container not healthy yet")

	w := doRequest(r, "POST", "/api/provision/resume-db/resume", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != string(domain.StatusResuming) {
		t.Errorf("body status: got %v, want RESUMING", body["status"])
	}
	if body["failureReason"] != service.ErrResumeNotObserved.Error() {
		t.Errorf("body failureReason: got %v", body["failureReason"])
	}
}

package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// A pause that could not be observed is a retryable conflict, not a server
// fault: nothing about the request was wrong, and repeating it converges.
// The body carries the project's truthful state so a caller that gave up
// waiting knows where the project stands without a second route.
func TestPauseThatIsNotObservedAnswersConflictWithTheProjectState(t *testing.T) {
	r, store, pauser, _ := setupPauseHandler(t)
	seedPausableProject(t, store)
	pauser.pauseErr = errors.New("pod still Terminating")

	w := doRequest(r, "POST", "/api/provision/pause-db/pause", `{"reason":"manual"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != string(domain.StatusPausing) {
		t.Errorf("body status: got %v, want PAUSING", body["status"])
	}
	if body["error"] != service.ErrPauseNotObserved.Error() {
		t.Errorf("body error: got %v", body["error"])
	}
}

func TestPauseWithAFailedBackupAnswersConflictAndLeavesTheProjectActive(t *testing.T) {
	r, store, _, backups := setupPauseHandler(t)
	seedPausableProject(t, store)
	backups.status = "FAILED"

	w := doRequest(r, "POST", "/api/provision/pause-db/pause", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", w.Code, w.Body.String())
	}
	inst, _ := store.FindByProjectID("pause-db")
	if inst.Status != "ACTIVE" {
		t.Errorf("project status: got %s, want ACTIVE", inst.Status)
	}
}

// A deployment mode the platform does not pause is the caller's mistake,
// not an unsettled project: 400, and never a retryable conflict.
func TestPauseOfAnUnsupportedModeIsABadRequest(t *testing.T) {
	r, store, _, _ := setupPauseHandler(t)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "byoc-db", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.DeploymentMode("operator"), Tier: domain.Free,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	w := doRequest(r, "POST", "/api/provision/byoc-db/pause", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

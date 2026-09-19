package handler

import (
	"encoding/json"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	projectA = "proj-a"
	projectB = "proj-b"
)

// seedSnapshotProject registers a project the snapshot service can dump from.
func seedSnapshotProject(store *storage.FileSystemStore, mock *k8s.MockClient, projectID string) {
	namespace := "org1-" + projectID
	store.Save(&domain.DatabaseInstance{
		ProjectID: projectID, OrgID: "org1", DBType: domain.PostgreSQL,
		Tier: domain.Free, Namespace: namespace, Status: "ACTIVE",
	})
	mock.ExecOutput[namespace+"/"+projectID+"-postgres-1"] = "-- dump of " + projectID
}

// exportSnapshotID dumps projectID through the route and returns the new
// snapshot's id.
func exportSnapshotID(t *testing.T, router chi.Router, projectID string) string {
	t.Helper()
	w := doRequest(router, "POST", "/api/provision/"+projectID+"/snapshot/export", `{"format":"plain"}`)
	if w.Code != 200 {
		t.Fatalf("export for %s: got %d, body: %s", projectID, w.Code, w.Body.String())
	}
	var info domain.SnapshotInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode export response: %v", err)
	}
	if info.ID == "" {
		t.Fatalf("export for %s returned no snapshot id", projectID)
	}
	return info.ID
}

// TestSnapshotDownloadRejectsOtherProject reproduces EXC-397: an admin of
// project A must not be able to download project B's dump by naming B's
// snapshot id under A's route.
func TestSnapshotDownloadRejectsOtherProject(t *testing.T) {
	router, store, mock := fullRouter(t)
	seedSnapshotProject(store, mock, projectA)
	seedSnapshotProject(store, mock, projectB)

	snapshotID := exportSnapshotID(t, router, projectB)

	w := doRequest(router, "GET", "/api/provision/"+projectA+"/snapshot/"+snapshotID+"/download", "")
	if w.Code != 404 {
		t.Errorf("cross-project download: got %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// TestSnapshotDeleteRejectsOtherProject reproduces the destructive half of
// EXC-397: B's snapshot must survive a delete issued under A's route.
func TestSnapshotDeleteRejectsOtherProject(t *testing.T) {
	router, store, mock := fullRouter(t)
	seedSnapshotProject(store, mock, projectA)
	seedSnapshotProject(store, mock, projectB)

	snapshotID := exportSnapshotID(t, router, projectB)

	w := doRequest(router, "DELETE", "/api/provision/"+projectA+"/snapshot/"+snapshotID, "")
	if w.Code != 404 {
		t.Errorf("cross-project delete: got %d, want 404 (body: %s)", w.Code, w.Body.String())
	}

	owner := doRequest(router, "GET", "/api/provision/"+projectB+"/snapshot/"+snapshotID+"/download", "")
	if owner.Code != 200 {
		t.Errorf("owner download after refused cross-project delete: got %d, want 200", owner.Code)
	}
}

// TestSnapshotListIsScopedToProject pins that the metadata listing never
// names another project's snapshots.
func TestSnapshotListIsScopedToProject(t *testing.T) {
	router, store, mock := fullRouter(t)
	seedSnapshotProject(store, mock, projectA)
	seedSnapshotProject(store, mock, projectB)

	exportSnapshotID(t, router, projectB)

	w := doRequest(router, "GET", "/api/provision/"+projectA+"/snapshot/", "")
	var list []domain.SnapshotInfo
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("project A sees %d snapshots, want 0", len(list))
	}
}

// TestSnapshotTraversalIDRejected pins the handler-boundary identifier check
// on both routes that take a snapshot id.
func TestSnapshotTraversalIDRejected(t *testing.T) {
	router, store, mock := fullRouter(t)
	seedSnapshotProject(store, mock, projectA)

	cases := []struct {
		name, method, path string
	}{
		{"download dot segment", "GET", "/api/provision/" + projectA + "/snapshot/..%2f..%2fusers/download"},
		{"delete dot segment", "DELETE", "/api/provision/" + projectA + "/snapshot/..%2f..%2fusers"},
		{"download empty-ish", "GET", "/api/provision/" + projectA + "/snapshot/.hidden/download"},
		{"delete space", "DELETE", "/api/provision/" + projectA + "/snapshot/two%20words"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doRequest(router, c.method, c.path, "")
			if w.Code != 400 {
				t.Errorf("%s %s: got %d, want 400", c.method, c.path, w.Code)
			}
		})
	}
}

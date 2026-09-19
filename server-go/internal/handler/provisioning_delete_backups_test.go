package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// recordingDeleter is a minimal in-memory object store for handler tests:
// one page, every key under the prefix.
type recordingDeleter struct {
	mu      sync.Mutex
	keys    []string
	deleted []string
}

func (d *recordingDeleter) ListKeys(_ context.Context, _, prefix, _ string, _ int32) ([]string, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, key := range d.keys {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out, "", nil
}

func (d *recordingDeleter) DeleteKeys(_ context.Context, _ string, keys []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, keys...)
	kept := d.keys[:0:0]
	for _, key := range d.keys {
		doomed := false
		for _, gone := range keys {
			if key == gone {
				doomed = true
			}
		}
		if !doomed {
			kept = append(kept, key)
		}
	}
	d.keys = kept
	return nil
}

func (d *recordingDeleter) deletedKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.deleted...)
}

func setupDeleteBackupsRouter(t *testing.T) (chi.Router, *storage.FileSystemStore, *recordingDeleter) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	deleter := &recordingDeleter{keys: []string{"proj-1/cloud/base/b1/data.tar.gz", "proj-2/cloud/base/b1/data.tar.gz"}}
	mock := k8s.NewMockClient()
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	creds := &domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s", Bucket: "b"}
	svc.SetBackupPurger(service.NewBackupPurger(service.StaticBackupStorage(creds), "backups/",
		func(_ context.Context, _ *domain.S3Credentials) (service.ObjectDeleter, error) { return deleter, nil }))
	h := NewProvisioningHandler(svc, &adminOrgStore{})

	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) { h.Routes(r) })
	for _, id := range []string{"proj-1", "proj-2"} {
		if err := store.Create(&domain.DatabaseInstance{ProjectID: id, OrgID: "org1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s, Status: "ACTIVE"}); err != nil {
			t.Fatal(err)
		}
	}
	return r, store, deleter
}

func TestDeleteWithoutConfirmKeepsBackups(t *testing.T) {
	r, store, deleter := setupDeleteBackupsRouter(t)
	for _, body := range []string{"", `{}`, `{"confirmDeleteBackups": false}`} {
		req := httptest.NewRequest(http.MethodDelete, "/api/provision/proj-1", strings.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		// The first body deletes the project; the rest find it already gone.
		if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
			t.Fatalf("body %q: status %d", body, w.Code)
		}
	}
	if got := deleter.deletedKeys(); len(got) != 0 {
		t.Fatalf("backups deleted without confirmation: %v", got)
	}
	if inst, _ := store.FindByProjectID("proj-1"); inst != nil {
		t.Fatal("project should be gone after the first DELETE")
	}
}

func TestDeleteWithConfirmPurgesProjectBackups(t *testing.T) {
	r, store, deleter := setupDeleteBackupsRouter(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/provision/proj-1", strings.NewReader(`{"confirmDeleteBackups": true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf(testWant200Fmt, w.Code)
	}
	got := deleter.deletedKeys()
	if len(got) != 1 || got[0] != "proj-1/cloud/base/b1/data.tar.gz" {
		t.Fatalf("deleted = %v, want only proj-1's objects", got)
	}
	if inst, _ := store.FindByProjectID("proj-1"); inst != nil {
		t.Fatal("project row should be gone")
	}
}

func TestDeleteRejectsMalformedBody(t *testing.T) {
	r, store, deleter := setupDeleteBackupsRouter(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/provision/proj-1", strings.NewReader(`{"confirmDeleteBackups": "yes"`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if inst, _ := store.FindByProjectID("proj-1"); inst == nil {
		t.Fatal("a malformed body must not deprovision anything")
	}
	if len(deleter.deletedKeys()) != 0 {
		t.Fatal("no purge on malformed body")
	}
}

func TestPurgeBackupsEndpoint(t *testing.T) {
	r, store, deleter := setupDeleteBackupsRouter(t)

	// Live project: refused, nothing deleted.
	req := httptest.NewRequest(http.MethodPost, "/api/provision/proj-1/backups/purge", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("live project: status = %d, want 409", w.Code)
	}
	if len(deleter.deletedKeys()) != 0 {
		t.Fatal("live project backups must not be purged")
	}

	// Pending marker: purged, row removed, count reported. The marker is
	// reached the way a real deletion reaches it — the store refuses to have
	// it set by a general update.
	if _, err := store.BeginDeletion("proj-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeletionFailure("proj-1", domain.StatusBackupsPendingDelete,
		domain.DeletionStepDeleteBackups, "r2 unavailable"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/provision/proj-1/backups/purge", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pending project: status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["deletedObjects"] != float64(1) || resp["projectId"] != "proj-1" {
		t.Fatalf("resp = %v", resp)
	}
	if inst, _ := store.FindByProjectID("proj-1"); inst != nil {
		t.Fatal("marker row should be removed after a successful retry")
	}

	// Unknown project: 404.
	req = httptest.NewRequest(http.MethodPost, "/api/provision/nope/backups/purge", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown project: status = %d, want 404", w.Code)
	}
}

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// adminOrgStore is a minimal storage.OrgStore for the admin RevokeOrg path.
type adminOrgStore struct {
	org     *domain.Org
	deleted bool
}

func (a *adminOrgStore) CreateOrg(context.Context, *domain.Org) error { return nil }
func (a *adminOrgStore) FindOrgByID(_ context.Context, id string) (*domain.Org, error) {
	if a.org != nil && a.org.ID == id {
		return a.org, nil
	}
	return nil, nil
}
func (a *adminOrgStore) FindOrgBySlug(context.Context, string) (*domain.Org, error) { return nil, nil }
func (a *adminOrgStore) FindOrgsByUser(context.Context, string) ([]*domain.Org, error) {
	return nil, nil
}
func (a *adminOrgStore) FindAllOrgs(context.Context) ([]*domain.Org, error) { return nil, nil }
func (a *adminOrgStore) UpdateOrg(context.Context, *domain.Org) error       { return nil }
func (a *adminOrgStore) DeleteOrg(context.Context, string) error            { a.deleted = true; return nil }
func (a *adminOrgStore) AddOrgMember(context.Context, *domain.OrgMember) error {
	return nil
}
func (a *adminOrgStore) RemoveOrgMember(context.Context, string, string) error { return nil }
func (a *adminOrgStore) UpdateOrgMemberRole(context.Context, string, string, string) error {
	return nil
}
func (a *adminOrgStore) ListOrgMembers(context.Context, string) ([]*domain.OrgMember, error) {
	return nil, nil
}
func (a *adminOrgStore) GetOrgMember(context.Context, string, string) (*domain.OrgMember, error) {
	return nil, nil
}
func (a *adminOrgStore) AddProjectMember(context.Context, *domain.ProjectMember) error { return nil }
func (a *adminOrgStore) RemoveProjectMember(context.Context, string, string) error     { return nil }
func (a *adminOrgStore) UpdateProjectMemberRole(context.Context, string, string, string) error {
	return nil
}
func (a *adminOrgStore) ListProjectMembers(context.Context, string) ([]*domain.ProjectMember, error) {
	return nil, nil
}
func (a *adminOrgStore) GetProjectMember(context.Context, string, string) (*domain.ProjectMember, error) {
	return nil, nil
}
func (a *adminOrgStore) CreatePendingInvite(context.Context, *domain.PendingInvite) error { return nil }
func (a *adminOrgStore) FindPendingInvitesByEmail(context.Context, string) ([]*domain.PendingInvite, error) {
	return nil, nil
}
func (a *adminOrgStore) DeletePendingInvite(context.Context, int64) error { return nil }
func (a *adminOrgStore) ListPendingInvites(context.Context, string) ([]*domain.PendingInvite, error) {
	return nil, nil
}

// captureAudit records audit entries so the admin path's auditFireAndForget
// has a non-nil writer to call.
type captureAudit struct {
	entries []*domain.AuditEntry
}

func (c *captureAudit) LogAudit(_ context.Context, e *domain.AuditEntry) error {
	c.entries = append(c.entries, e)
	return nil
}

// adminDockerMock is a DockerClient for the admin deprovision path. It
// tracks removal so ContainerStatus reports the container gone, which is
// what teardown waits for.
type adminDockerMock struct{ removed bool }

func (adminDockerMock) CreateContainer(context.Context, string, string, map[string]string, map[string]string) (string, error) {
	return "ctr", nil
}
func (adminDockerMock) StartContainer(context.Context, string) error { return nil }
func (adminDockerMock) StopContainer(context.Context, string) error  { return nil }
func (m *adminDockerMock) RemoveContainer(context.Context, string) error {
	m.removed = true
	return nil
}
func (m *adminDockerMock) ContainerStatus(context.Context, string) (string, error) {
	if m.removed {
		return "not_found", nil
	}
	return "running", nil
}
func (adminDockerMock) WaitForHealthy(context.Context, string) error { return nil }
func (adminDockerMock) ExecInContainer(context.Context, string, []string) (int, error) {
	return 0, nil
}
func (adminDockerMock) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}
func (adminDockerMock) CopyFromContainer(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func newDockerProvSvc(t *testing.T, store *inMemoryInstanceStore) *service.ProvisioningService {
	t.Helper()
	dp := provisioner.NewDockerPostgreSQLProvisioner(&adminDockerMock{})
	factory := provisioner.NewFactory(dp)
	return service.NewProvisioningService(store, factory, k8s.NewMockClient())
}

func TestAdmin_ForceDropProject_HappyPath(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-x": {ProjectID: "proj-x", OrgID: "org-1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-x"},
	}}
	provSvc := newDockerProvSvc(t, store)
	audit := &captureAudit{}
	h := NewAdminHandler(provSvc, store, nil, audit, nil, "", nil)

	r := chi.NewRouter()
	r.Delete("/api/admin/projects/{projectId}", h.ForceDropProject)
	req := httptest.NewRequest("DELETE", "/api/admin/projects/proj-x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ForceDrop: %d body=%s", w.Code, w.Body.String())
	}
	if _, err := store.FindByProjectID("proj-x"); err == nil {
		if got, _ := store.FindByProjectID("proj-x"); got != nil {
			t.Error("project should be removed after force drop")
		}
	}
}

func TestAdmin_ForceDropProject_ClearsDeletionProtection(t *testing.T) {
	prot := true
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-p": {ProjectID: "proj-p", OrgID: "org-1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-p", DeletionProtection: &prot},
	}}
	provSvc := newDockerProvSvc(t, store)
	h := NewAdminHandler(provSvc, store, nil, &captureAudit{}, nil, "", nil)

	r := chi.NewRouter()
	r.Delete("/api/admin/projects/{projectId}", h.ForceDropProject)
	req := httptest.NewRequest("DELETE", "/api/admin/projects/proj-p", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ForceDrop with protection: %d body=%s", w.Code, w.Body.String())
	}
}

func TestAdmin_RevokeOrg_HappyPath(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-1": {ProjectID: "proj-1", OrgID: "org-9", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-1"},
		"proj-2": {ProjectID: "proj-2", OrgID: "org-9", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-2"},
		"other":  {ProjectID: "other", OrgID: "org-x"},
	}}
	provSvc := newDockerProvSvc(t, store)
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-9", Slug: "acme"}}
	h := NewAdminHandler(provSvc, store, orgStore, &captureAudit{}, nil, "", nil)

	r := chi.NewRouter()
	r.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	req := httptest.NewRequest("DELETE", "/api/admin/orgs/org-9?cascade=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RevokeOrg: %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["dropped"] == nil {
		t.Errorf("expected dropped count, got %v", resp)
	}
	if !orgStore.deleted {
		t.Error("org row should be deleted after successful cascade")
	}
	// The org-x project must survive.
	if got, _ := store.FindByProjectID("other"); got == nil {
		t.Error("project in a different org must not be dropped")
	}
}

func TestAdmin_RevokeOrg_NotFound(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	provSvc := newDockerProvSvc(t, store)
	h := NewAdminHandler(provSvc, store, &adminOrgStore{}, &captureAudit{}, nil, "", nil)
	r := chi.NewRouter()
	r.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	req := httptest.NewRequest("DELETE", "/api/admin/orgs/ghost?cascade=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown org should 404, got %d", w.Code)
	}
}

func TestAdmin_ForceDropProject_ConfirmDeleteBackups(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-b": {ProjectID: "proj-b", OrgID: "org-1", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: "ctr-b"},
	}}
	provSvc := newDockerProvSvc(t, store)
	deleter := &recordingDeleter{keys: []string{"backups/proj-b/manual/a.tar.gz", "backups/proj-c/manual/keep.tar.gz"}}
	creds := &domain.S3Credentials{AccessKeyID: "k", SecretAccessKey: "s", Bucket: "b"}
	provSvc.SetBackupPurger(service.NewBackupPurger(service.StaticBackupStorage(creds), "backups/",
		func(_ context.Context, _ *domain.S3Credentials) (service.ObjectDeleter, error) { return deleter, nil }))
	h := NewAdminHandler(provSvc, store, nil, &captureAudit{}, nil, "", nil)

	r := chi.NewRouter()
	r.Delete("/api/admin/projects/{projectId}", h.ForceDropProject)
	req := httptest.NewRequest("DELETE", "/api/admin/projects/proj-b", strings.NewReader(`{"confirmDeleteBackups":true}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ForceDrop: %d body=%s", w.Code, w.Body.String())
	}
	if got := deleter.deletedKeys(); len(got) != 1 || got[0] != "backups/proj-b/manual/a.tar.gz" {
		t.Fatalf("deleted = %v, want only proj-b's backup", got)
	}
}

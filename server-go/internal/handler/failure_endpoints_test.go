package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

func asRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "member-1", Role: role, Active: true})))
		})
	}
}

// failingBuild serves create and add-database against a cluster that fails
// the build with crashText, answering synchronously.
func failingBuild(t *testing.T, role string) chi.Router {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.CRDError = errors.New(crashText)
	mock.Namespaces["org1-apps-only"] = true
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "apps-only", OrgID: "org1", Tier: domain.Free, Namespace: "org1-apps-only",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE", NoDatabase: true,
	}); err != nil {
		t.Fatal(err)
	}
	mock.Capacity = k8s.ClusterCapacity{Nodes: []k8s.NodeCapacity{{Name: "roomy", AllocatableCPUMilli: 8000, AllocatableMemBytes: 32 << 30}}}
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	orgs.AddMember("org1", "member-1", domain.OrgRoleAdmin)
	orgs.AddOrg("org2", domain.Free)
	orgs.AddMember("org2", "member-1", domain.OrgRoleAdmin)
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	svc.SetOrgStore(orgs)
	svc.SetCredentialVerifier(acceptingVerifier{})
	withBackupTarget(t, svc)
	h := NewProvisioningHandler(svc, memberOfOrg1{orgs})
	h.SetInstanceStore(store)
	r := chi.NewRouter()
	r.Use(asRole(role))
	r.Post("/api/provision/", h.Provision)
	r.Post("/api/provision/{projectId}/database", h.AddDatabase)
	return r
}

func syncPost(r chi.Router, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const (
	failCreateBody = `{"projectName":"fresh","orgId":"org2","databaseType":"POSTGRESQL","postgresVersion":"17"}`
	failAddBody    = `{"databaseType":"POSTGRESQL","postgresVersion":"17"}`
)

func TestFailedCreateAndAddDatabaseAnswerWithTheKindForACustomerAndTheTextForStaff(t *testing.T) {
	for what, call := range map[string]struct{ path, body string }{
		"create":       {"/api/provision/", failCreateBody},
		"add database": {"/api/provision/apps-only/database", failAddBody},
	} {
		customer := syncPost(failingBuild(t, "user"), call.path, call.body)
		got := decodeFailure(t, customer.Body.String())
		if got.FailureReason == "" && got.FailureKind == "" {
			t.Fatalf("%s did not fail the build, so it proves nothing: %d %s", what, customer.Code, customer.Body.String())
		}
		assertCustomerView(t, what, got, customer.Body.String())

		staff := syncPost(failingBuild(t, "platform_admin"), call.path, call.body)
		if got := decodeFailure(t, staff.Body.String()); got.FailureReason == "" || got.FailureKind != "crashing" {
			t.Errorf("%s: staff lost the raw text: %s", what, staff.Body.String())
		}
	}
}

// failedRestoreAdapter answers a restore with a failed project, as an adapter
// whose build failed would.
type failedRestoreAdapter struct{ service.BackupAdapter }

func (failedRestoreAdapter) BackupsConfigured() bool { return true }
func (failedRestoreAdapter) Restore(context.Context, *domain.DatabaseInstance, domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	return &domain.ProvisioningResponse{ProjectID: "p2", Status: string(domain.StageFailed), Namespace: "org1-p1", FailureReason: crashText, RollbackLog: crashRollup}, nil
}

func restoreWithoutOrchestrator(t *testing.T, role string) *httptest.ResponseRecorder {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	backupSvc := service.NewBackupService(store, k8s.NewMockClient(), dir, testBackupStorage())
	backupSvc.SetBackupCredentials(testBackupCredentials(t))
	backupSvc.SetOrgProjectCapacity(unlimitedCapacity{})
	backupSvc.RegisterAdapter(domain.ModeK8s, failedRestoreAdapter{})
	_ = store.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", DeploymentMode: domain.ModeK8s, Status: "ACTIVE"})
	h := NewBackupHandler(backupSvc)
	r := chi.NewRouter()
	r.Use(asRole(role))
	r.Route("/api/provision/{projectId}/backup", func(r chi.Router) { h.Routes(r) })
	return syncPost(r, "/api/provision/p1/backup/restore", `{"newProjectName":"copy"}`)
}

func TestARestoreAnswerHasTheKindForACustomerAndTheTextForStaff(t *testing.T) {
	customer := restoreWithoutOrchestrator(t, "user")
	if customer.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", customer.Code, customer.Body.String())
	}
	assertCustomerView(t, "restore", decodeFailure(t, customer.Body.String()), customer.Body.String())
	staff := restoreWithoutOrchestrator(t, "platform_operator")
	assertStaffView(t, "restore (staff)", decodeFailure(t, staff.Body.String()))
}

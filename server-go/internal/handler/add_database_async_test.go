package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const addPostgresBody = `{"databaseType":"POSTGRESQL","postgresVersion":"17"}`

func postAddDatabase(f *asyncAddFixture, project, body, prefer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/provision/"+project+"/database", strings.NewReader(body))
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

type asyncAddFixture struct {
	router chi.Router
	store  *storage.FileSystemStore
}

// asyncAddDatabaseFixture serves the add over a provisioner that can build a
// database, and keeps the builds it accepts for the test to run.
func asyncAddDatabaseFixture(t *testing.T) (*asyncAddFixture, *[]func()) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, inst := range []*domain.DatabaseInstance{
		{ProjectID: "apps-only", OrgID: "org1", Tier: domain.Free, Namespace: "org1-apps-only",
			DeploymentMode: domain.ModeK8s, Status: "ACTIVE", NoDatabase: true},
		{ProjectID: "test-db", OrgID: "org1", DBType: domain.PostgreSQL, Tier: domain.Free,
			Namespace: "org1-test-db", DeploymentMode: domain.ModeK8s, Status: "ACTIVE", PostgresVersion: "17"},
	} {
		if err := store.Create(inst); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.Namespaces["org1-apps-only"] = true
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	withBackupTarget(t, svc)
	svc.SetVault(newFakeVault())
	svc.SetOrgStore(orgs)
	builds := &[]func(){}
	svc.SetBackgroundRunner(func(build func()) { *builds = append(*builds, build) })
	h := NewProvisioningHandler(svc, &adminOrgStore{})
	h.SetInstanceStore(store)
	r := chi.NewRouter()
	r.Post("/api/provision/{projectId}/database", h.AddDatabase)
	return &asyncAddFixture{router: r, store: store}, builds
}

// Studio prefers not to wait for the build (EXC-426): the answer is 202 with
// the project PROVISIONING, and the project's status carries the outcome.
func TestAddDatabase_PreferRespondAsyncAnswers202WhileTheDatabaseIsBuilt(t *testing.T) {
	f, builds := asyncAddDatabaseFixture(t)

	w := postAddDatabase(f, "apps-only", addPostgresBody, "respond-async")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202; body %s", w.Code, w.Body.String())
	}
	var got domain.ProvisioningResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProjectID != "apps-only" || got.Status != string(domain.StatusProvisioning) || !got.NoDatabase {
		t.Fatalf("answer %+v, want the project PROVISIONING without its database yet", got)
	}
	if len(*builds) != 1 {
		t.Fatalf("builds started: %d, want 1", len(*builds))
	}
	(*builds)[0]()
	if row, _ := f.store.FindByProjectID("apps-only"); row.NoDatabase || row.Status != string(domain.StatusActive) {
		t.Fatalf("after the build: %+v, want ACTIVE with its database", row)
	}
}

// Refusals are still answered in the response, and start nothing.
func TestAddDatabase_PreferRespondAsyncStillAnswersARefusal(t *testing.T) {
	f, builds := asyncAddDatabaseFixture(t)

	if w := postAddDatabase(f, "test-db", addPostgresBody, "respond-async"); w.Code != http.StatusConflict {
		t.Fatalf("a project that has one: %d %s, want 409", w.Code, w.Body.String())
	}
	if w := postAddDatabase(f, "apps-only", `{"databaseType":"POSTGRESQL"}`, "respond-async"); w.Code != http.StatusBadRequest {
		t.Fatalf("no version: %d %s, want 400", w.Code, w.Body.String())
	}
	if len(*builds) != 0 {
		t.Fatalf("refusals started %d builds", len(*builds))
	}
}

// Without the preference the answer comes when the database is built.
func TestAddDatabase_WithoutPreferIsAnsweredWhenTheDatabaseIsBuilt(t *testing.T) {
	f, builds := asyncAddDatabaseFixture(t)

	w := postAddDatabase(f, "apps-only", addPostgresBody, "")
	if w.Code != http.StatusOK || len(*builds) != 0 {
		t.Fatalf("status %d, background builds %d; want 200 and none", w.Code, len(*builds))
	}
	if !strings.Contains(w.Body.String(), `"status":"ACTIVE"`) {
		t.Fatalf("body %s, want the project ACTIVE", w.Body.String())
	}
}

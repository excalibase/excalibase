package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const (
	chosenPassword = "correct horse battery staple"
	oneTimeBodyFmt = `{"projectName":"fresh","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17"%s}`
	passwordMember = `,"password":"` + chosenPassword + `"`
)

func oneTimeProvisionRouter(t *testing.T, flagOn bool) chi.Router {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	withBackupTarget(t, svc)
	svc.SetOrgStore(orgs)
	svc.SetBackgroundRunner(func(func()) {})
	if flagOn {
		svc.SetFeatures(features.NewStatic(features.OneTimeCredentials))
	}
	h := NewProvisioningHandler(svc, nil)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "test-admin", Role: "platform_admin", Active: true})))
		})
	})
	r.Post(testProvisionPath, h.Provision)
	return r
}

func createdBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestCreateReturnsAGeneratedPasswordOnce(t *testing.T) {
	r := oneTimeProvisionRouter(t, true)
	body := createdBody(t, postProvision(r, strings.Replace(oneTimeBodyFmt, "%s", "", 1), "respond-async"))
	password, _ := body["password"].(string)
	if len(password) < 16 {
		t.Fatalf("create returned password %q, want a generated one", password)
	}
}

func TestCreateReturnsTheChosenPasswordAndRefusesAWeakOne(t *testing.T) {
	r := oneTimeProvisionRouter(t, true)
	body := createdBody(t, postProvision(r, strings.Replace(oneTimeBodyFmt, "%s", passwordMember, 1), "respond-async"))
	if body["password"] != chosenPassword {
		t.Errorf("create returned %v, want the chosen password", body["password"])
	}
	weak := postProvision(r, strings.Replace(oneTimeBodyFmt, "%s", `,"password":"short"`, 1), "respond-async")
	if weak.Code != http.StatusBadRequest || strings.Contains(weak.Body.String(), "short\"") && strings.Contains(weak.Body.String(), `"password"`) {
		t.Errorf("weak password: %d %s, want 400 that does not echo it", weak.Code, weak.Body.String())
	}
}

func TestCreateWithTheFlagOffNeitherReturnsNorAcceptsAPassword(t *testing.T) {
	r := oneTimeProvisionRouter(t, false)
	body := createdBody(t, postProvision(r, strings.Replace(oneTimeBodyFmt, "%s", "", 1), "respond-async"))
	if _, has := body["password"]; has {
		t.Errorf("flag off, yet create returned a password: %v", body)
	}
}

type acceptingVerifier struct{}

func (acceptingVerifier) VerifyRole(context.Context, *domain.DatabaseInstance, string, string, string) error {
	return nil
}

const oneTimeRotProject = "rot-proj"

func rotateRouter(t *testing.T, audit *recordingAudit) (chi.Router, *fakeVault) {
	return rotateRouterWithFlag(t, audit, true)
}

func rotateRouterWithFlag(t *testing.T, audit *recordingAudit, flagOn bool) (chi.Router, *fakeVault) {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.ExecOutput["org1-rot-proj/rot-proj-postgres-1"] = "ALTER ROLE"
	port := 5432
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{oneTimeRotProject: {
		ProjectID: oneTimeRotProject, OrgID: "org1", Namespace: "org1-rot-proj", DBType: domain.PostgreSQL, Status: "ACTIVE",
		Host: "h.local", Port: &port, DatabaseName: "app", Username: "owner", SSLMode: "require",
	}}}
	vault := newFakeVault()
	vault.data["projects/rot-proj/credentials/admin"] = map[string]string{"username": "owner", "password": "the-old-owner-password"}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	svc.SetVault(vault)
	svc.SetCredentialVerifier(acceptingVerifier{})
	if flagOn {
		svc.SetFeatures(features.NewStatic(features.OneTimeCredentials))
	}
	h := NewProvisioningHandler(svc, nil)
	if audit != nil {
		h.SetAuditLog(audit)
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "an-admin", Role: "platform_admin", Active: true})))
		})
	})
	r.Get("/api/provision/{projectId}/credentials", h.GetCredentials)
	r.Post("/api/provision/{projectId}/credentials/rotate", h.RotateCredentials)
	return r, vault
}

func rotatePost(r chi.Router, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/provision/"+oneTimeRotProject+"/credentials/rotate", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRotateShowsTheNewPasswordOnceAndTheCredentialsRouteNever(t *testing.T) {
	audit := &recordingAudit{}
	r, vault := rotateRouter(t, audit)
	w := rotatePost(r, "")
	if w.Code != http.StatusOK {
		t.Fatalf("rotate %d: %s", w.Code, w.Body.String())
	}
	var rotated map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &rotated)
	password, _ := rotated["password"].(string)
	if password == "" || password != vault.data["projects/rot-proj/credentials/admin"]["password"] {
		t.Fatalf("rotate answered %v, the vault holds %v", rotated["password"], vault.data["projects/rot-proj/credentials/admin"]["password"])
	}
	read := httptest.NewRecorder()
	r.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/provision/"+oneTimeRotProject+"/credentials", nil))
	if strings.Contains(read.Body.String(), password) || strings.Contains(read.Body.String(), `"password"`) {
		t.Errorf("the credentials route returned the password: %s", read.Body.String())
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != auditDBPasswordReset || strings.Contains(audit.entries[0].Details, password) {
		t.Errorf("audit %+v, want one reset entry without the password", audit.entries)
	}
}

func TestRotateToAChosenPasswordAndRefusesAWeakOne(t *testing.T) {
	r, vault := rotateRouter(t, nil)
	if w := rotatePost(r, `{"password":"`+chosenPassword+`"}`); w.Code != http.StatusOK ||
		vault.data["projects/rot-proj/credentials/admin"]["password"] != chosenPassword || !strings.Contains(w.Body.String(), chosenPassword) {
		t.Fatalf("chosen: %d %s", w.Code, w.Body.String())
	}
	weak := rotatePost(r, `{"password":"short"}`)
	if weak.Code != http.StatusBadRequest || strings.Contains(weak.Body.String(), "short\"") {
		t.Errorf("weak: %d %s, want 400 that does not echo it", weak.Code, weak.Body.String())
	}
}

func TestResetRefusesAChosenPasswordWhileTheFlagIsOff(t *testing.T) {
	r, _ := rotateRouterWithFlag(t, nil, false)
	if w := rotatePost(r, `{"password":"`+chosenPassword+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
}

func TestResetAnswers400ForAnUnreadableBodyAnd409ForAnUnfinishedReset(t *testing.T) {
	r, vault := rotateRouter(t, nil)
	if w := rotatePost(r, `{"password":`); w.Code != http.StatusBadRequest {
		t.Errorf("unreadable body: %d", w.Code)
	}
	vault.data["projects/rot-proj/credentials/pending/admin"] = map[string]string{"password": "left-by-an-interrupted-reset", "username": "owner"}
	w := rotatePost(r, `{"password":"`+chosenPassword+`"}`)
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), chosenPassword) {
		t.Errorf("unfinished reset: %d %s", w.Code, w.Body.String())
	}
}

func TestCreateRefusesAChosenPasswordWithNoDatabaseOrWithTheFlagOff(t *testing.T) {
	withDatabase := strings.Replace(oneTimeBodyFmt, "%s", passwordMember, 1)
	if w := postProvision(oneTimeProvisionRouter(t, false), withDatabase, "respond-async"); w.Code != http.StatusBadRequest {
		t.Errorf("flag off: %d %s", w.Code, w.Body.String())
	}
	noDatabase := `{"projectName":"fresh","orgId":"org1","noDatabase":true` + passwordMember + `}`
	if w := postProvision(oneTimeProvisionRouter(t, true), noDatabase, "respond-async"); w.Code != http.StatusBadRequest {
		t.Errorf("no database: %d %s", w.Code, w.Body.String())
	}
}

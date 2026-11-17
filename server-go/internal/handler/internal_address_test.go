package handler

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
	internalHostName = "p1-postgres-rw.org1-p1.svc.cluster.local"
	internalNS       = "org1-p1"
)

// memberOfOrg1 is an org store whose one member belongs to org1.
type memberOfOrg1 struct{ *fakestore.Orgs }

func (memberOfOrg1) FindOrgsByUser(context.Context, string) ([]*domain.Org, error) {
	return []*domain.Org{{ID: "org1"}}, nil
}

// customerRouter serves one project as a plain member: the answers a customer sees.
func customerRouter(t *testing.T, role string) chi.Router {
	return customerRouterWithFlag(t, role, true)
}

func customerRouterWithFlag(t *testing.T, role string, oneTime bool) chi.Router {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.ExecOutput[internalNS+"/p1-postgres-1"] = "ALTER ROLE"
	mock.Secrets["org1-p1/p1-postgres-app"] = map[string][]byte{"username": []byte("owner"), "password": []byte("generated-at-create")}
	mock.CRDs["org1-p1/p1-postgres"] = &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"currentPrimary": "p1-postgres-1"},
	}}
	port := 5432
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{"p1": {
		ProjectID: "p1", OrgID: "org1", Namespace: internalNS, DBType: domain.PostgreSQL, Status: "ACTIVE",
		Host: internalHostName, ReadOnlyHost: "p1-postgres-r.org1-p1.svc.cluster.local", Port: &port, DatabaseName: "app",
		Username: "owner", SSLMode: "require", PoolerHost: "p1-pooler.org1-p1.svc.cluster.local",
		MetricsEndpoint: "http://p1-postgres-1.org1-p1.svc.cluster.local:9187/metrics",
	}}}
	vault := newFakeVault()
	vault.data["projects/p1/credentials/admin"] = map[string]string{"username": "owner", "password": "the-old-owner-password"}
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	orgs.AddMember("org1", "member-1", domain.OrgRoleAdmin)
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	svc.SetVault(vault)
	svc.SetOrgStore(orgs)
	svc.SetCredentialVerifier(acceptingVerifier{})
	if oneTime {
		svc.SetFeatures(features.NewStatic(features.OneTimeCredentials))
	}
	svc.SetBackgroundRunner(func(func()) {})
	withBackupTarget(t, svc)
	h := NewProvisioningHandler(svc, memberOfOrg1{orgs})
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "member-1", Role: role, Active: true})))
		})
	})
	r.Get("/api/provision/", h.ListInstances)
	r.Post("/api/provision/", h.Provision)
	r.Get("/api/provision/{projectId}/", h.GetStatus)
	r.Get("/api/provision/{projectId}/credentials", h.GetCredentials)
	r.Post("/api/provision/{projectId}/credentials/rotate", h.RotateCredentials)
	return r
}

func answer(r chi.Router, method, path, body string) string {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Prefer", "respond-async")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Body.String()
}

func assertNoClusterAddress(t *testing.T, what, body string) {
	t.Helper()
	for _, leaked := range []string{"svc.cluster.local", internalNS, `"namespace":"org`} {
		if strings.Contains(body, leaked) {
			t.Errorf("%s answers with %q: %s", what, leaked, body)
		}
	}
}

func TestNoCustomerAnswerCarriesAnInternalAddressOrANamespace(t *testing.T) {
	r := customerRouter(t, "user")
	answers := map[string]string{
		"credentials":    answer(r, "GET", "/api/provision/p1/credentials", ""),
		"project get":    answer(r, "GET", "/api/provision/p1/", ""),
		"project list":   answer(r, "GET", "/api/provision/", ""),
		"reset password": answer(r, "POST", "/api/provision/p1/credentials/rotate", ""),
		"create":         answer(r, "POST", "/api/provision/", `{"projectName":"fresh","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17"}`),
	}
	for what, body := range answers {
		if body == "" || strings.Contains(body, `"error"`) && what != "create" {
			t.Fatalf("%s: unexpected answer %q", what, body)
		}
		assertNoClusterAddress(t, what, body)
	}
	if !strings.Contains(answers["project list"], `"projectId":"p1"`) {
		t.Errorf("the list did not include the project, so it proves nothing: %s", answers["project list"])
	}
}

func TestAPlatformAdminStillSeesTheProjectsInternalAddress(t *testing.T) {
	r := customerRouter(t, "platform_admin")
	if body := answer(r, "GET", "/api/provision/p1/", ""); !strings.Contains(body, internalHostName) || !strings.Contains(body, internalNS) {
		t.Errorf("the admin view lost the host or namespace: %s", body)
	}
}

func TestCredentialsTemplateNamesHostAndPortNotTheClusterAddress(t *testing.T) {
	r := customerRouter(t, "user")
	var creds map[string]any
	if err := json.Unmarshal([]byte(answer(r, "GET", "/api/provision/p1/credentials", "")), &creds); err != nil {
		t.Fatal(err)
	}
	if url, _ := creds["connectionUrl"].(string); !strings.Contains(url, "@<host>:<port>/") {
		t.Errorf("connection URL = %q", url)
	}
	for _, key := range []string{"host", "readOnlyHost"} {
		if _, has := creds[key]; has {
			t.Errorf("credentials carry %s", key)
		}
	}
}

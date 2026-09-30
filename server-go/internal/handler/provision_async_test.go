package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const asyncProvisionBody = `{"projectName":"%s","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17"}`

func asyncProvisionRouter(t *testing.T, store *inMemoryInstanceStore) (chi.Router, *[]func()) {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	svc := service.NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	withBackupTarget(t, svc)
	svc.SetOrgStore(orgs)
	builds := &[]func(){}
	svc.SetBackgroundRunner(func(f func()) { *builds = append(*builds, f) })
	h := NewProvisioningHandler(svc, nil)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "test-admin", Role: "platform_admin", Active: true})))
		})
	})
	r.Post(testProvisionPath, h.Provision)
	return r, builds
}

func postProvision(r chi.Router, body string, prefer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, testProvisionPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A caller that prefers not to wait (Studio) is answered 202 as soon as the
// project exists, PROVISIONING, and follows the build through its status.
func TestProvision_PreferRespondAsyncAnswers202WithTheProvisioningProject(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	r, builds := asyncProvisionRouter(t, store)

	w := postProvision(r, fmt.Sprintf(asyncProvisionBody, "later"), "respond-async")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202; body %s", w.Code, w.Body.String())
	}
	var got domain.ProvisioningResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProjectID == "" || got.Status != domain.StatusProvisioning {
		t.Fatalf("answer %+v, want the project, PROVISIONING", got)
	}
	if len(*builds) != 1 {
		t.Fatalf("builds started: %d, want 1", len(*builds))
	}
}

// Refusals that need no build are still answered in the response itself.
func TestProvision_PreferRespondAsyncStillRefusesAFullOrganisation(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	r, builds := asyncProvisionRouter(t, store)
	if w := postProvision(r, fmt.Sprintf(asyncProvisionBody, "first"), "respond-async"); w.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	w := postProvision(r, fmt.Sprintf(asyncProvisionBody, "second"), "respond-async")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), orgLimitRefusal) {
		t.Fatalf("second: %d %s, want 409 with the limit", w.Code, w.Body.String())
	}
	if len(*builds) != 1 {
		t.Fatalf("builds started: %d, want only the first", len(*builds))
	}
}

// Without the preference nothing changes for API clients: the answer comes
// when the build is done.
func TestProvision_WithoutPreferIsAnsweredWhenTheBuildIsDone(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	r, builds := asyncProvisionRouter(t, store)
	w := postProvision(r, fmt.Sprintf(asyncProvisionBody, "now"), "")
	if w.Code != http.StatusOK || len(*builds) != 0 {
		t.Fatalf("status %d, background builds %d; want 200 and none", w.Code, len(*builds))
	}
}

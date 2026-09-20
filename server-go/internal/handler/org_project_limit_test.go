package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// orgLimitRefusal is the exact text a caller at their project limit is given.
// It names the limit and the tier, and nothing about the organisation.
const orgLimitRefusal = "organization has reached its project limit of 1 for the FREE tier"

// unlimitedCapacity is the capacity checker for restore tests whose subject is
// not the organisation's project limit.
type unlimitedCapacity struct{}

func (unlimitedCapacity) EnsureOrgProjectCapacity(context.Context, string, domain.TierType) error {
	return nil
}

// brokenInstanceStore cannot register anything, standing in for a platform
// database that is down.
type brokenInstanceStore struct {
	*inMemoryInstanceStore
}

func (brokenInstanceStore) CreateWithinOrgLimit(*domain.DatabaseInstance, int) error {
	return errors.New("pq: could not connect to server: platform-db:5432")
}

func provisionRouter(t *testing.T, store storage.InstanceStore) chi.Router {
	t.Helper()
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	return provisionRouterOn(t, store, mock)
}

func provisionRouterOn(t *testing.T, store storage.InstanceStore, mock *k8s.MockClient) chi.Router {
	t.Helper()
	svc := service.NewProvisioningService(store,
		provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, "")), mock)
	h := NewProvisioningHandler(svc, nil)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "test-admin", Role: "platform_admin", Active: true})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Post(testProvisionPath, h.Provision)
	return r
}

// A full organisation is a conflict with the caller's current state, not a
// malformed request: 409, with the fixed refusal as the whole message.
func TestProvision_OrgAtItsLimitAnswers409(t *testing.T) {
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-held00001": {ProjectID: "proj-held00001", OrgID: "org-secret", Tier: domain.Free, Status: "ACTIVE"},
	}}
	r := provisionRouter(t, store)

	w := doRequest(r, "POST", testProvisionPath,
		`{"projectName":"second","orgId":"org-secret","databaseType":"POSTGRESQL","tier":"FREE"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != orgLimitRefusal {
		t.Fatalf("body: got %q, want %q", body.Error, orgLimitRefusal)
	}
	if strings.Contains(w.Body.String(), "org-secret") {
		t.Fatalf("the response must not echo the organisation id: %s", w.Body.String())
	}
}

// The organisation's limit is what the caller can act on, so it is the answer
// they get — a full cluster must not turn their 409 into a 400 about capacity
// they are not asking for. The limit is therefore resolved before anything
// about the cluster is looked at.
func TestProvision_OrgAtItsLimitAnswers409EvenOnAFullCluster(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	// Every milli of the cluster is already requested: a provision that got
	// as far as the capacity check would be refused 400.
	mock.Capacity = k8s.ClusterCapacity{
		AllocatableCPUMilli: 2000, AllocatableMemBytes: 4 << 30,
		RequestedCPUMilli: 2000, RequestedMemBytes: 4 << 30,
	}
	store := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-held00002": {ProjectID: "proj-held00002", OrgID: "org-full", Tier: domain.Free, Status: "ACTIVE"},
	}}
	r := provisionRouterOn(t, store, mock)

	w := doRequest(r, "POST", testProvisionPath,
		`{"projectName":"second","orgId":"org-full","databaseType":"POSTGRESQL","tier":"FREE"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != orgLimitRefusal {
		t.Fatalf("body: got %q, want %q", body.Error, orgLimitRefusal)
	}
}

// A platform database that cannot answer is a server failure, and the caller
// is told nothing about it.
func TestProvision_StoreFailureAnswers500WithoutInternals(t *testing.T) {
	store := brokenInstanceStore{inMemoryInstanceStore: &inMemoryInstanceStore{
		insts: map[string]*domain.DatabaseInstance{},
	}}
	r := provisionRouter(t, store)

	w := doRequest(r, "POST", testProvisionPath,
		`{"projectName":"unlucky","orgId":"org1","databaseType":"POSTGRESQL","tier":"FREE"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500; body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "platform-db") || strings.Contains(w.Body.String(), "pq:") {
		t.Fatalf("the response leaks the store failure: %s", w.Body.String())
	}
	if len(store.insts) != 0 {
		t.Fatalf("nothing may be created: %v", store.insts)
	}
}

// A restore creates a project, so a full organisation is refused the same way
// — before the restore is even submitted.
func TestRestore_OrgAtItsLimitAnswers409(t *testing.T) {
	r, store, _ := fullRouter(t)
	// FREE allows one project, and this is it.
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-onlyone01", OrgID: "org-free", DBType: domain.PostgreSQL,
		Tier: domain.Free, Namespace: "org-free-proj-onlyone01", Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := doRequest(r, "POST", "/api/provision/proj-onlyone01/backup/restore", `{"backupId":"bk-1","newProjectName":"recovered"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != orgLimitRefusal {
		t.Fatalf("body: got %q, want %q", body.Error, orgLimitRefusal)
	}
}

// A source project whose tier cannot be resolved fails the restore, and the
// caller is told nothing beyond that it failed.
func TestRestore_UnresolvableTierAnswers500(t *testing.T) {
	r, store, _ := fullRouter(t)
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-notier001", OrgID: "org-notier", DBType: domain.PostgreSQL,
		Tier: domain.TierType("PLATINUM"), Namespace: "org-notier-proj-notier001", Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := doRequest(r, "POST", "/api/provision/proj-notier001/backup/restore", `{"newProjectName":"recovered"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500; body %s", w.Code, w.Body.String())
	}
}

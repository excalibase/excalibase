package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// leaseCountingClaimer records how many lifecycle leases are held at once.
// Each lease pins a platform-database connection for the whole teardown, so
// the peak is what the cascade costs the pool.
type leaseCountingClaimer struct {
	mu      sync.Mutex
	held    int
	maxHeld int
	claimed []string
}

func (c *leaseCountingClaimer) Claim(_ context.Context, projectID string, _ service.ProjectOperation) (func(), bool, error) {
	c.mu.Lock()
	c.held++
	if c.held > c.maxHeld {
		c.maxHeld = c.held
	}
	c.claimed = append(c.claimed, projectID)
	c.mu.Unlock()
	// Hold the lease long enough that overlapping claims are observable
	// rather than passing by on scheduling luck.
	time.Sleep(2 * time.Millisecond)
	return func() {
		c.mu.Lock()
		c.held--
		c.mu.Unlock()
	}, true, nil
}

func (c *leaseCountingClaimer) peak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxHeld
}

func (c *leaseCountingClaimer) claims() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.claimed...)
}

// An org cascade deprovisions its projects one at a time. Running them
// together would hold one platform-database connection per project for the
// whole teardown — up to DELETION_WAIT_TIMEOUT each — and an org with more
// projects than the pool has connections would starve every other caller,
// including the queries the teardowns themselves run.
func TestRevokeOrgCascadeHoldsOneLifecycleLeaseAtATime(t *testing.T) {
	insts := map[string]*domain.DatabaseInstance{}
	mock := k8s.NewMockClient()
	for _, id := range []string{"proj-c1", "proj-c2", "proj-c3", "proj-c4"} {
		ns := "org-cascade-" + id
		insts[id] = &domain.DatabaseInstance{
			ProjectID: id, OrgID: "org-cascade", DBType: domain.PostgreSQL,
			Namespace: ns, Status: "ACTIVE",
		}
		mock.Namespaces[ns] = true
	}
	projectCount := len(insts)
	store := &inMemoryInstanceStore{insts: insts}
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	pgProv.SetDeletionPoller(provisioner.NewPoller(0, 0))
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(pgProv), mock)
	claimer := &leaseCountingClaimer{}
	provSvc.SetOperationClaimer(claimer)
	orgStore := &adminOrgStore{org: &domain.Org{ID: "org-cascade", Slug: "cascade"}}
	h := NewAdminHandler(provSvc, store, orgStore, &captureAudit{}, nil, "", nil)

	router := chi.NewRouter()
	router.Delete("/api/admin/orgs/{orgId}", h.RevokeOrg)
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/orgs/org-cascade?cascade=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if claimed := claimer.claims(); len(claimed) != projectCount {
		t.Fatalf("every project must be claimed for teardown, got %v", claimed)
	}
	if peak := claimer.peak(); peak != 1 {
		t.Errorf("cascade held %d lifecycle leases at once, want 1", peak)
	}
}

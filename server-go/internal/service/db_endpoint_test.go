package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeEndpointStore is an in-memory DatabaseEndpointStore with the one
// behaviour the service depends on: a port, once taken, stays taken until it
// is released.
type fakeEndpointStore struct {
	mu         sync.Mutex
	rows       map[string]domain.DBEndpoint
	quarantine map[int]time.Time
	next       int
	allocErr   error
	releases   []string
	deletes    []string
}

func newFakeEndpointStore() *fakeEndpointStore {
	return &fakeEndpointStore{
		rows:       map[string]domain.DBEndpoint{},
		quarantine: map[int]time.Time{},
		next:       30000,
	}
}

// key identifies one of a project's holdings. A DocumentDB project holds a
// port per protocol (EXC-409), from this one allocator.
func endpointKey(projectID string, role domain.DBEndpointRole) string {
	return projectID + "/" + string(role)
}

func (f *fakeEndpointStore) row(projectID string, role domain.DBEndpointRole) domain.DBEndpoint {
	if row, ok := f.rows[endpointKey(projectID, role)]; ok {
		return row
	}
	return domain.DefaultDBEndpointForRole(projectID, role)
}

// quarantined reports whether a released port is still being held back.
func (f *fakeEndpointStore) quarantined(port int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, held := f.quarantine[port]
	return held
}

func (f *fakeEndpointStore) GetDatabaseEndpoint(_ context.Context, projectID string) (domain.DBEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.row(projectID, domain.DBEndpointRolePostgres), nil
}

func (f *fakeEndpointStore) GetDatabaseEndpointForRole(
	_ context.Context, projectID string, role domain.DBEndpointRole,
) (domain.DBEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.row(projectID, role), nil
}

func (f *fakeEndpointStore) AllocateDatabaseEndpointPort(_ context.Context, projectID string, role domain.DBEndpointRole, _ domain.PortRange, _ time.Duration) (domain.DBEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.allocErr != nil {
		return domain.DBEndpoint{}, f.allocErr
	}
	row := f.row(projectID, role)
	if row.Port == 0 {
		row.Port = f.next
		f.next++
	}
	f.rows[endpointKey(projectID, role)] = row
	return row, nil
}

func (f *fakeEndpointStore) SetDatabaseEndpointPublic(_ context.Context, projectID string, public bool) (domain.DBEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.row(projectID, domain.DBEndpointRolePostgres)
	row.PublicEnabled = public
	f.rows[endpointKey(projectID, domain.DBEndpointRolePostgres)] = row
	return row, nil
}

func (f *fakeEndpointStore) SetDatabaseEndpointRequireTLS(_ context.Context, projectID string, requireTLS bool) (domain.DBEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.row(projectID, domain.DBEndpointRolePostgres)
	row.RequireTLS = requireTLS
	f.rows[endpointKey(projectID, domain.DBEndpointRolePostgres)] = row
	return row, nil
}

func (f *fakeEndpointStore) ReleaseDatabaseEndpointPort(_ context.Context, projectID string, role domain.DBEndpointRole, releasedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.row(projectID, role)
	if row.Port > 0 {
		f.quarantine[row.Port] = releasedAt
	}
	row.Port = 0
	row.PublicEnabled = false
	f.rows[endpointKey(projectID, role)] = row
	f.releases = append(f.releases, projectID)
	return nil
}

func (f *fakeEndpointStore) DeleteDatabaseEndpoint(_ context.Context, projectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, role := range []domain.DBEndpointRole{domain.DBEndpointRolePostgres, domain.DBEndpointRoleMongo} {
		delete(f.rows, endpointKey(projectID, role))
	}
	f.deletes = append(f.deletes, projectID)
	return nil
}

var _ storage.DatabaseEndpointStore = (*fakeEndpointStore)(nil)

const (
	endpointProject   = "proj-endpoint"
	endpointNamespace = "org-proj-endpoint"
)

func endpointInstance(status string) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID:      endpointProject,
		Namespace:      endpointNamespace,
		DeploymentMode: domain.ModeK8s,
		Status:         status,
		DatabaseName:   "appdb",
		Username:       "app_user",
		Host:           endpointProject + "-postgres-rw." + endpointNamespace + ".svc.cluster.local",
	}
}

// endpointHarness wires the service over fakes and hands back the pieces a
// test needs to assert on.
type endpointHarness struct {
	svc      *DBEndpointService
	store    *fakeEndpointStore
	kube     *k8s.MockClient
	instance *domain.DatabaseInstance
}

func newEndpointHarness(t *testing.T, status string) *endpointHarness {
	t.Helper()
	window, err := domain.NewPortRange(30000, 30099)
	if err != nil {
		t.Fatalf("NewPortRange: %v", err)
	}
	store := newFakeEndpointStore()
	kube := k8s.NewMockClient()
	kube.Secrets[endpointNamespace+"/"+endpointProject+"-postgres-ca"] = map[string][]byte{"ca.crt": []byte("-----BEGIN CERTIFICATE-----")}
	inst := endpointInstance(status)
	svc := NewDBEndpointService(DBEndpointServiceConfig{
		Endpoints:    store,
		Instances:    &stubInstanceLookup{inst: inst},
		Kube:         kube,
		DomainSuffix: "db.excalibase.io",
		Ports:        window,
		Quarantine:   30 * 24 * time.Hour,
		SharedIPKey:  "excalibase-db-edge",
	})
	return &endpointHarness{svc: svc, store: store, kube: kube, instance: inst}
}

// stubInstanceLookup answers with one project.
type stubInstanceLookup struct{ inst *domain.DatabaseInstance }

func (s *stubInstanceLookup) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	if s.inst == nil || s.inst.ProjectID != projectID {
		return nil, nil
	}
	return s.inst.Clone(), nil
}

func (h *endpointHarness) serviceExists(t *testing.T) bool {
	t.Helper()
	exists, err := h.kube.PublicDBServiceExists(context.Background(), endpointNamespace, domain.DBEndpointServiceName(endpointProject))
	if err != nil {
		t.Fatalf("PublicDBServiceExists: %v", err)
	}
	return exists
}

func TestDescribeReportsNoPublicEndpointUntilTheCustomerAsks(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")

	view, err := h.svc.Describe(context.Background(), endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if view.Enabled || view.Available || view.Port != 0 {
		t.Fatalf("a fresh project must not be publicly reachable: %+v", view)
	}
	if !view.RequireTLS {
		t.Fatal("TLS must be required by default")
	}
	if view.Internal.Host != h.instance.Host || view.Internal.Port != 5432 {
		t.Fatalf("the in-cluster endpoint must always be reported: %+v", view.Internal)
	}
	if h.serviceExists(t) {
		t.Fatal("no Service may exist before the customer opts in")
	}
}

func TestEnableCreatesTheServiceBeforeRecordingTheChoice(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")

	view, err := h.svc.SetPublic(context.Background(), endpointProject, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	if !view.Enabled || !view.Available {
		t.Fatalf("view = %+v", view)
	}
	if view.Port < 30000 || view.Port > 30099 {
		t.Fatalf("port %d is outside the configured window", view.Port)
	}
	if view.Host != endpointProject+".db.excalibase.io" {
		t.Fatalf("host = %q", view.Host)
	}
	if !h.serviceExists(t) {
		t.Fatal("the Service must exist")
	}
}

func TestEnableIsRefusedWhenTheServiceCannotBeCreated(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.kube.EnsurePublicDBError = errors.New("apiserver said no")

	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); err == nil {
		t.Fatal("SetPublic must fail when the Service was not created")
	}
	view, err := h.svc.Describe(context.Background(), endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if view.Enabled {
		t.Fatal("the choice must not be recorded before the result is observed")
	}
}

func TestDisableDeletesTheServiceAndQuarantinesThePort(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	enabled, err := h.svc.SetPublic(ctx, endpointProject, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	view, err := h.svc.SetPublic(ctx, endpointProject, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if view.Enabled || view.Available || view.Port != 0 {
		t.Fatalf("view = %+v", view)
	}
	if h.serviceExists(t) {
		t.Fatal("the port must stop answering")
	}
	if _, quarantined := h.store.quarantine[enabled.Port]; !quarantined {
		t.Fatalf("port %d was freed without quarantine", enabled.Port)
	}
}

func TestConnectionStringsShowBothTLSChoices(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	view, err := h.svc.Describe(ctx, endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(view.Connection.RequireTLS, "sslmode=verify-full") {
		t.Fatalf("RequireTLS = %q", view.Connection.RequireTLS)
	}
	if !strings.Contains(view.Connection.AllowPlaintext, "sslmode=prefer") {
		t.Fatalf("AllowPlaintext = %q", view.Connection.AllowPlaintext)
	}
	if view.CACertificate == "" {
		t.Fatal("verify-full needs the CA and the API must hand it over")
	}
	if !strings.Contains(view.Internal.ConnectionString, ".svc.cluster.local") {
		t.Fatalf("the in-cluster string must name the in-cluster host: %q", view.Internal.ConnectionString)
	}
}

func TestRequireTLSSurvivesTurningTheEndpointOffAndOn(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	if _, err := h.svc.SetRequireTLS(ctx, endpointProject, false); err != nil {
		t.Fatalf("SetRequireTLS: %v", err)
	}
	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := h.svc.SetPublic(ctx, endpointProject, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	view, err := h.svc.Describe(ctx, endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if view.RequireTLS {
		t.Fatal("the TLS choice belongs to the project, not to the port")
	}
	if !strings.Contains(view.Connection.RequireTLS, "verify-full") {
		t.Fatal("both strings must still be shown so the customer sees what they chose")
	}
}

func TestWithdrawAndPublishKeepTheSamePortAcrossAPause(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	enabled, err := h.svc.SetPublic(ctx, endpointProject, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := h.svc.Withdraw(ctx, h.instance); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if h.serviceExists(t) {
		t.Fatal("a paused project must have no Service")
	}
	view, err := h.svc.Describe(ctx, endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !view.Enabled {
		t.Fatal("a pause must not revoke the customer's choice")
	}
	if view.Available {
		t.Fatal("a paused project is not reachable and must not claim to be")
	}
	if err := h.svc.Publish(ctx, h.instance); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	resumed, err := h.svc.Describe(ctx, endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if resumed.Port != enabled.Port {
		t.Fatalf("resume moved the port from %d to %d", enabled.Port, resumed.Port)
	}
	if !resumed.Available {
		t.Fatal("resume must bring the endpoint back")
	}
}

func TestPublishDoesNothingForAProjectThatNeverOptedIn(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	if err := h.svc.Publish(context.Background(), h.instance); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if h.serviceExists(t) {
		t.Fatal("a project that never asked for a public endpoint must not get one on resume")
	}
}

func TestReleaseFreesThePortAndRemovesTheRow(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	enabled, err := h.svc.SetPublic(ctx, endpointProject, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := h.svc.Release(ctx, h.instance); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if h.serviceExists(t) {
		t.Fatal("a deleted project must have no Service")
	}
	if _, quarantined := h.store.quarantine[enabled.Port]; !quarantined {
		t.Fatalf("port %d was freed without quarantine", enabled.Port)
	}
	if len(h.store.deletes) != 1 {
		t.Fatalf("deletes = %v", h.store.deletes)
	}
	if err := h.svc.Release(ctx, h.instance); err != nil {
		t.Fatalf("Release must be idempotent for a retried teardown: %v", err)
	}
}

func TestEnableIsRefusedForAProjectThatIsNotServable(t *testing.T) {
	for _, status := range []string{string(domain.StatusDeleting), string(domain.StatusRestoring)} {
		t.Run(status, func(t *testing.T) {
			h := newEndpointHarness(t, status)
			if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); err == nil {
				t.Fatal("a project that is not servable must not get a public endpoint")
			}
			if h.serviceExists(t) {
				t.Fatal("no Service may exist")
			}
		})
	}
}

func TestEnableOnAPausedProjectRecordsTheChoiceWithoutAService(t *testing.T) {
	h := newEndpointHarness(t, string(domain.StatusPaused))

	view, err := h.svc.SetPublic(context.Background(), endpointProject, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	if !view.Enabled {
		t.Fatal("the choice must be recorded")
	}
	if view.Available {
		t.Fatal("a paused project is not reachable")
	}
	if h.serviceExists(t) {
		t.Fatal("a paused project must have no Service")
	}
	if view.Port == 0 {
		t.Fatal("the port must be held so the resume comes back on it")
	}
}

func TestDescribeRefusesWhenNoEndpointDomainIsConfigured(t *testing.T) {
	window, err := domain.NewPortRange(30000, 30099)
	if err != nil {
		t.Fatalf("NewPortRange: %v", err)
	}
	inst := endpointInstance("ACTIVE")
	svc := NewDBEndpointService(DBEndpointServiceConfig{
		Endpoints:   newFakeEndpointStore(),
		Instances:   &stubInstanceLookup{inst: inst},
		Kube:        k8s.NewMockClient(),
		Ports:       window,
		Quarantine:  time.Hour,
		SharedIPKey: "excalibase-db-edge",
	})
	if _, err := svc.Describe(context.Background(), endpointProject); !errors.Is(err, ErrDBEndpointNotConfigured) {
		t.Fatalf("error = %v, want ErrDBEndpointNotConfigured", err)
	}
}

func TestDescribeRefusesADeploymentModeWithNoLoadBalancer(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.instance.DeploymentMode = domain.ModeDocker

	if _, err := h.svc.Describe(context.Background(), endpointProject); !errors.Is(err, ErrDBEndpointUnsupported) {
		t.Fatalf("error = %v, want ErrDBEndpointUnsupported", err)
	}
}

func TestDescribeRefusesAnUnknownProject(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	if _, err := h.svc.Describe(context.Background(), "proj-nobody"); err == nil {
		t.Fatal("an unknown project must not describe an endpoint")
	}
}

func TestDescribeFailsWhenTheClusterCAIsMissing(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	delete(h.kube.Secrets, endpointNamespace+"/"+endpointProject+"-postgres-ca")
	if _, err := h.svc.Describe(ctx, endpointProject); err == nil {
		t.Fatal("a verify-full connection string with no CA is not something to hand a customer")
	}
}

func TestDescribeFailsWhenTheClusterCASecretHoldsNoCertificate(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	h.kube.Secrets[endpointNamespace+"/"+endpointProject+"-postgres-ca"] = map[string][]byte{}
	if _, err := h.svc.Describe(ctx, endpointProject); err == nil {
		t.Fatal("an empty CA secret must be reported, not passed off as a CA")
	}
}

func TestDescribeFailsWhenTheServiceStateCannotBeRead(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	h.kube.PublicDBServiceExistsError = errors.New("apiserver said no")
	if _, err := h.svc.Describe(ctx, endpointProject); err == nil {
		t.Fatal("an unreadable Service must not be reported as absent")
	}
}

func TestEnableIsRefusedWhenNoPortIsLeft(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.store.allocErr = storage.ErrDBEndpointPortsExhausted

	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); !errors.Is(err, storage.ErrDBEndpointPortsExhausted) {
		t.Fatalf("error = %v, want ErrDBEndpointPortsExhausted", err)
	}
	if h.serviceExists(t) {
		t.Fatal("no Service may exist")
	}
}

func TestEnableIsRefusedWhenTheServiceIsNotSeenAfterwards(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.kube.PublicDBServiceExistsError = errors.New("apiserver said no")

	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); err == nil {
		t.Fatal("an unconfirmed Service must not be recorded as published")
	}
}

func TestDisableIsRefusedWhenTheServiceCannotBeDeleted(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	enabled, err := h.svc.SetPublic(ctx, endpointProject, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	h.kube.DeletePublicDBError = errors.New("apiserver said no")
	if _, err := h.svc.SetPublic(ctx, endpointProject, false); err == nil {
		t.Fatal("the port must not be freed while something may still be listening on it")
	}
	if _, quarantined := h.store.quarantine[enabled.Port]; quarantined {
		t.Fatalf("port %d was quarantined before the Service was gone", enabled.Port)
	}
}

func TestReleaseIsRefusedWhenTheServiceCannotBeConfirmedGone(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	h.kube.PublicDBServiceExistsError = errors.New("apiserver said no")
	if err := h.svc.Release(ctx, h.instance); err == nil {
		t.Fatal("a teardown must not free a port it cannot confirm is dead")
	}
	if len(h.store.deletes) != 0 {
		t.Fatalf("the endpoint row was dropped anyway: %v", h.store.deletes)
	}
}

func TestPublishIsRefusedWhenTheServiceCannotBeCreated(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()

	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	h.kube.EnsurePublicDBError = errors.New("apiserver said no")
	if err := h.svc.Publish(ctx, h.instance); err == nil {
		t.Fatal("a resume that cannot republish the endpoint must say so")
	}
}

func TestLifecycleCallsAreNoOpsForADeploymentModeWithNoLoadBalancer(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.instance.DeploymentMode = domain.ModeDocker
	ctx := context.Background()

	for name, call := range map[string]func() error{
		"withdraw": func() error { return h.svc.Withdraw(ctx, h.instance) },
		"publish":  func() error { return h.svc.Publish(ctx, h.instance) },
		"release":  func() error { return h.svc.Release(ctx, h.instance) },
	} {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestSetRequireTLSRefusesAnUnknownProject(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	if _, err := h.svc.SetRequireTLS(context.Background(), "proj-nobody", true); err == nil {
		t.Fatal("an unknown project has no TLS setting to change")
	}
}

func TestSetPublicRefusesAnUnknownProject(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	for _, public := range []bool{true, false} {
		if _, err := h.svc.SetPublic(context.Background(), "proj-nobody", public); err == nil {
			t.Fatalf("an unknown project must not be reachable (public=%v)", public)
		}
	}
}

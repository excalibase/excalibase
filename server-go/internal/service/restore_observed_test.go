package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	restoreTargetNS      = "org-dst"
	restoreTargetCluster = "dst-postgres"
	restoreTargetPod     = "dst-postgres-1"
)

// restoreClock drives the adapter's readiness poll without sleeping. Each
// tick may mutate the cluster the adapter is observing, which is how a test
// says "the operator reconciled on the Nth look".
type restoreClock struct {
	now  time.Time
	tick func(round int)
	seen int
}

func (c *restoreClock) Now() time.Time { return c.now }

func (c *restoreClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	c.seen++
	if c.tick != nil {
		c.tick(c.seen)
	}
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// storeRegistrar is a ProjectRegistrar that persists to a real store, so the
// tests exercise the RESTORING row, the ACTIVE flip and the compensation that
// removes an unverified row.
type storeRegistrar struct {
	store storage.InstanceStore
	err   error
	opts  []RegistrationOptions
}

func (r *storeRegistrar) RegisterProject(_ context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions) error {
	r.opts = append(r.opts, opts)
	if r.err != nil {
		return r.err
	}
	if opts.Unverified {
		markProjectRestoring(inst)
	} else {
		markProjectActive(inst)
	}
	return r.store.Create(inst)
}

// probeFunc adapts a function to DatabaseProbe.
type probeFunc struct {
	err    error
	calls  []string
	status []string
	store  storage.InstanceStore
}

func (p *probeFunc) Probe(_ context.Context, projectID string) error {
	p.calls = append(p.calls, projectID)
	if p.store != nil {
		if inst, err := p.store.FindByProjectID(projectID); err == nil && inst != nil {
			p.status = append(p.status, inst.Status)
		}
	}
	return p.err
}

type observedRestoreFixture struct {
	mock    *k8s.MockClient
	store   storage.InstanceStore
	reg     *storeRegistrar
	probe   *probeFunc
	clock   *restoreClock
	adapter *K8sBackupAdapter
}

// newObservedRestore wires an adapter whose readiness wait is driven by a fake
// clock. By default the operator never reconciles: the cluster CR carries no
// status, so a restore can only end in a timeout.
func newObservedRestore(t *testing.T) *observedRestoreFixture {
	t.Helper()
	mock := k8s.NewMockClient()
	store := emptyInstanceStore(t)
	f := &observedRestoreFixture{
		mock:  mock,
		store: store,
		reg:   &storeRegistrar{store: store},
		probe: &probeFunc{store: store},
		clock: &restoreClock{now: time.Unix(0, 0)},
	}
	adapter := NewK8sBackupAdapter(mock, t.TempDir(), StaticBackupStorage(r2Storage()))
	adapter.SetInstanceStore(store)
	adapter.SetProjectRegistrar(f.reg)
	adapter.SetDatabaseProbe(f.probe)
	adapter.SetReadyPoller(provisioner.Poller{
		Interval: time.Second,
		Timeout:  10 * time.Second,
		Now:      f.clock.Now,
		After:    f.clock.After,
	})
	f.adapter = adapter
	return f
}

func (f *observedRestoreFixture) restore(t *testing.T) (*domain.ProvisioningResponse, error) {
	t.Helper()
	return f.adapter.Restore(context.Background(), sourceInstance(),
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
}

// reconcile stamps the CNPG status a healthy recovered cluster reports.
func (f *observedRestoreFixture) reconcile() {
	f.setClusterStatus(map[string]interface{}{
		"phase":          "Cluster in healthy state",
		"currentPrimary": restoreTargetPod,
		"readyInstances": int64(1),
	})
	f.mock.PodReady[restoreTargetNS+"/"+restoreTargetPod] = true
}

func (f *observedRestoreFixture) setClusterStatus(status map[string]interface{}) {
	obj, ok := f.mock.CRDs[restoreTargetNS+"/"+restoreTargetCluster]
	if !ok {
		return
	}
	_ = unstructured.SetNestedMap(obj.Object, status, "status")
}

func (f *observedRestoreFixture) assertNothingLeftBehind(t *testing.T) {
	t.Helper()
	if f.mock.Namespaces[restoreTargetNS] {
		t.Errorf("namespace %s must be compensated away", restoreTargetNS)
	}
	if _, ok := f.mock.CRDs[restoreTargetNS+"/"+restoreTargetCluster]; ok {
		t.Errorf("restore cluster CR must be compensated away")
	}
	inst, err := f.store.FindByProjectID("dst")
	if err != nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if inst != nil {
		t.Errorf("target project must not be left registered, got status %q", inst.Status)
	}
}

func TestK8sRestoreDoesNotCompleteWhileOperatorIsUnavailable(t *testing.T) {
	f := newObservedRestore(t)

	resp, err := f.restore(t)
	if err == nil {
		t.Fatalf("restore must fail when the cluster never becomes ready, got %+v", resp)
	}
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err: got %v, want ErrRestoreNotObserved", err)
	}
	if len(f.reg.opts) != 0 {
		t.Errorf("nothing may be registered before the cluster is observed ready")
	}
	if len(f.probe.calls) != 0 {
		t.Errorf("the database must not be probed before the cluster is observed ready")
	}
	f.assertNothingLeftBehind(t)
}

func TestK8sRestoreFailsAndCleansUpWhenClusterReportsFailure(t *testing.T) {
	f := newObservedRestore(t)
	f.clock.tick = func(int) {
		f.setClusterStatus(map[string]interface{}{"phase": "Cluster is in an unrecoverable state"})
	}

	if _, err := f.restore(t); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err: got %v, want ErrRestoreNotObserved", err)
	}
	if f.clock.seen > 2 {
		t.Errorf("a failed cluster must stop the wait immediately, polled %d times", f.clock.seen)
	}
	f.assertNothingLeftBehind(t)
}

func TestK8sRestoreCompletesOnlyAfterReadinessAndQuery(t *testing.T) {
	f := newObservedRestore(t)
	f.clock.tick = func(round int) {
		if round == 2 {
			f.reconcile()
		}
	}

	resp, err := f.restore(t)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("status: got %q, want ACTIVE", resp.Status)
	}
	if len(f.probe.calls) != 1 || f.probe.calls[0] != "dst" {
		t.Fatalf("the restored database must be probed exactly once, got %v", f.probe.calls)
	}
	if len(f.probe.status) != 1 || f.probe.status[0] != string(domain.StatusRestoring) {
		t.Errorf("the project must still be RESTORING while it is probed, got %v", f.probe.status)
	}
	if len(f.reg.opts) != 1 || !f.reg.opts[0].Unverified {
		t.Errorf("registration must be unverified until the query succeeds, got %+v", f.reg.opts)
	}
	inst, _ := f.store.FindByProjectID("dst")
	if inst == nil || inst.Status != "ACTIVE" {
		t.Fatalf("the row must be flipped to ACTIVE after the probe, got %+v", inst)
	}
}

func TestK8sRestoreCleansUpWhenRestoredDatabaseDoesNotAnswer(t *testing.T) {
	f := newObservedRestore(t)
	f.probe.err = errors.New("dial tcp: connection refused")
	f.clock.tick = func(round int) {
		if round == 1 {
			f.reconcile()
		}
	}

	if _, err := f.restore(t); !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err: got %v, want ErrRestoreNotObserved", err)
	}
	f.assertNothingLeftBehind(t)
}

func TestK8sRestoreStopsWhenTargetProjectIsBeingDeleted(t *testing.T) {
	f := newObservedRestore(t)
	f.clock.tick = func(round int) {
		if round == 1 {
			f.reconcile()
		}
	}
	// DELETE lands between the unverified registration and the ACTIVE flip.
	f.probe.err = nil
	probeDeletes := &deleteOnProbe{inner: f.probe, store: f.store}
	f.adapter.SetDatabaseProbe(probeDeletes)

	_, err := f.restore(t)
	if !errors.Is(err, ErrRestoreTargetDeleting) {
		t.Fatalf("err: got %v, want ErrRestoreTargetDeleting", err)
	}
	inst, _ := f.store.FindByProjectID("dst")
	if inst == nil || inst.Status != string(domain.StatusDeleting) {
		t.Fatalf("deletion must keep the row it claimed, got %+v", inst)
	}
	if !f.mock.Namespaces[restoreTargetNS] {
		t.Errorf("restore must not compensate resources deletion has taken over")
	}
}

// deleteOnProbe claims the target for deletion at the moment the restore
// probes it, so the ACTIVE flip meets the one-way DELETING door.
type deleteOnProbe struct {
	inner *probeFunc
	store storage.InstanceStore
}

func (p *deleteOnProbe) Probe(ctx context.Context, projectID string) error {
	if err := p.inner.Probe(ctx, projectID); err != nil {
		return err
	}
	_, err := p.store.BeginDeletion(projectID, nil)
	return err
}

func TestRestoreFailureMessageDoesNotLeakInternals(t *testing.T) {
	msg := ErrRestoreNotObserved.Error()
	for _, leak := range []string{"namespace", "cnpg", "vault", "postgres://", "dial tcp"} {
		if strings.Contains(strings.ToLower(msg), leak) {
			t.Errorf("client-facing restore error leaks %q: %s", leak, msg)
		}
	}
}

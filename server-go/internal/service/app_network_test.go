package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type fakeAppNetworkSettings struct {
	values map[string]bool
	setErr error
	getErr error
}

func (f *fakeAppNetworkSettings) GetAppPrivateNetwork(_ context.Context, projectID string) (bool, error) {
	return f.values[projectID], f.getErr
}

func (f *fakeAppNetworkSettings) SetAppPrivateNetwork(_ context.Context, projectID string, enabled bool) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.values[projectID] = enabled
	return nil
}

type fakeAppNetworkProjects map[string]*domain.DatabaseInstance

func (f fakeAppNetworkProjects) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	return f[projectID], nil
}

const netTestProject, netTestNamespace = "proj-net1", "org1-proj-net1"

func newAppNetworkFixture(status string) (*AppNetworkService, *fakeAppNetworkSettings, *k8s.MockClient) {
	settings := &fakeAppNetworkSettings{values: map[string]bool{}}
	kube := k8s.NewMockClient()
	projects := fakeAppNetworkProjects{netTestProject: {
		ProjectID: netTestProject, Namespace: netTestNamespace, Status: status, DeploymentMode: domain.ModeK8s,
	}}
	return NewAppNetworkService(settings, projects, kube, nil), settings, kube
}

func TestAppNetwork_OffByDefault(t *testing.T) {
	svc, _, _ := newAppNetworkFixture(string(domain.StatusActive))
	view, err := svc.Describe(context.Background(), netTestProject)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if view.PrivateNetwork || view.Applied {
		t.Fatalf("a new project must be closed: %+v", view)
	}
}

func TestAppNetwork_OnAppliesThePolicyThenRecordsIt(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	view, err := svc.Set(context.Background(), netTestProject, true)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !view.PrivateNetwork || !view.Applied {
		t.Fatalf("view = %+v, want on and applied", view)
	}
	if !settings.values[netTestProject] || !kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatalf("setting=%v policy=%v", settings.values[netTestProject], kube.AppPrivateNetwork[netTestNamespace])
	}
}

func TestAppNetwork_OffRemovesThePolicyThenRecordsIt(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	ctx := context.Background()
	if _, err := svc.Set(ctx, netTestProject, true); err != nil {
		t.Fatalf("on: %v", err)
	}
	view, err := svc.Set(ctx, netTestProject, false)
	if err != nil {
		t.Fatalf("off: %v", err)
	}
	if view.PrivateNetwork || view.Applied || settings.values[netTestProject] || kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatalf("off must close and record: view=%+v", view)
	}
}

// Nothing is recorded as on unless the cluster took the policy.
func TestAppNetwork_AFailedApplyRecordsNothing(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	kube.AppPrivateNetworkErr = errors.New("cilium refused")
	if _, err := svc.Set(context.Background(), netTestProject, true); err == nil {
		t.Fatal("a refused policy must fail the change")
	}
	if settings.values[netTestProject] {
		t.Fatal("the setting was recorded on although the policy was never applied")
	}
}

// A recorded-off project must never be left open: a failed write closes it again.
func TestAppNetwork_AFailedWriteClosesTheNetworkAgain(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	settings.setErr = errors.New("db down")
	if _, err := svc.Set(context.Background(), netTestProject, true); err == nil {
		t.Fatal("a failed write must fail the change")
	}
	if kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatal("the policy stayed applied while the setting says off")
	}
}

func TestAppNetwork_TurningOnNeedsAnActiveProject(t *testing.T) {
	svc, _, kube := newAppNetworkFixture(string(domain.StatusPaused))
	if _, err := svc.Set(context.Background(), netTestProject, true); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("err = %v, want ErrProjectNotActive", err)
	}
	if kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatal("nothing may be applied to an inactive project")
	}
	// Closing is always allowed: it only ever narrows what apps accept.
	if _, err := svc.Set(context.Background(), netTestProject, false); err != nil {
		t.Fatalf("off on a paused project: %v", err)
	}
}

func TestAppNetwork_RefusesUnknownAndNonKubernetesProjects(t *testing.T) {
	svc, _, _ := newAppNetworkFixture(string(domain.StatusActive))
	if _, err := svc.Set(context.Background(), "proj-missing", true); !errors.Is(err, ErrAppNetworkProjectNotFound) {
		t.Fatalf("unknown project: err = %v", err)
	}
	docker := NewAppNetworkService(&fakeAppNetworkSettings{values: map[string]bool{}},
		fakeAppNetworkProjects{netTestProject: {ProjectID: netTestProject, Namespace: netTestNamespace,
			Status: string(domain.StatusActive), DeploymentMode: domain.ModeDocker}}, k8s.NewMockClient(), nil)
	if _, err := docker.Describe(context.Background(), netTestProject); !errors.Is(err, ErrAppNetworkUnsupported) {
		t.Fatalf("docker project: err = %v", err)
	}
}

// The view reports what the cluster holds, not only what was recorded.
func TestAppNetwork_DescribeReportsTheObservedPolicy(t *testing.T) {
	svc, settings, _ := newAppNetworkFixture(string(domain.StatusActive))
	settings.values[netTestProject] = true
	view, err := svc.Describe(context.Background(), netTestProject)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !view.PrivateNetwork || view.Applied {
		t.Fatalf("view = %+v, want on but not applied", view)
	}
}

type stubClaimer struct {
	claimed bool
	err     error
}

func (c stubClaimer) Claim(context.Context, string, ProjectOperation) (func(), bool, error) {
	return func() {}, c.claimed, c.err
}

// closeFails lets opening succeed while every close is refused.
type closeFails struct{ *k8s.MockClient }

func (c closeFails) SetAppPrivateNetwork(ctx context.Context, namespace string, open bool) error {
	if !open {
		return errors.New("delete refused")
	}
	return c.MockClient.SetAppPrivateNetwork(ctx, namespace, open)
}

type failingProjects struct{}

func (failingProjects) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, errors.New("db down")
}

func activeProjects() fakeAppNetworkProjects {
	return fakeAppNetworkProjects{netTestProject: {ProjectID: netTestProject, Namespace: netTestNamespace,
		Status: string(domain.StatusActive), DeploymentMode: domain.ModeK8s}}
}

func TestAppNetwork_ABusyProjectIsRefused(t *testing.T) {
	settings := &fakeAppNetworkSettings{values: map[string]bool{}}
	busy := NewAppNetworkService(settings, activeProjects(), k8s.NewMockClient(), stubClaimer{claimed: false})
	if _, err := busy.Set(context.Background(), netTestProject, true); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("err = %v, want ErrProjectOperationRunning", err)
	}
	broken := NewAppNetworkService(settings, activeProjects(), k8s.NewMockClient(), stubClaimer{err: errors.New("lease store down")})
	if _, err := broken.Set(context.Background(), netTestProject, true); err == nil {
		t.Fatal("a failed claim must fail the change")
	}
}

func TestAppNetwork_AFailedCloseRecordsNothing(t *testing.T) {
	settings := &fakeAppNetworkSettings{values: map[string]bool{netTestProject: true}}
	svc := NewAppNetworkService(settings, activeProjects(), closeFails{k8s.NewMockClient()}, nil)
	if _, err := svc.Set(context.Background(), netTestProject, false); err == nil {
		t.Fatal("a refused close must fail the change")
	}
	if !settings.values[netTestProject] {
		t.Fatal("off was recorded although the policy is still there")
	}
}

// When neither the write nor the compensating close works, both failures are reported.
func TestAppNetwork_AFailedWriteAndCloseReportBoth(t *testing.T) {
	settings := &fakeAppNetworkSettings{values: map[string]bool{}, setErr: errors.New("db down")}
	svc := NewAppNetworkService(settings, activeProjects(), closeFails{k8s.NewMockClient()}, nil)
	_, err := svc.Set(context.Background(), netTestProject, true)
	if err == nil || !errors.Is(err, settings.setErr) {
		t.Fatalf("err = %v, want the write failure and the close failure", err)
	}
}

func TestAppNetwork_ReadFailuresAreReported(t *testing.T) {
	settings := &fakeAppNetworkSettings{values: map[string]bool{}, getErr: errors.New("db down")}
	if _, err := NewAppNetworkService(settings, activeProjects(), k8s.NewMockClient(), nil).Describe(context.Background(), netTestProject); err == nil {
		t.Error("a failed setting read must fail the view")
	}
	kube := k8s.NewMockClient()
	kube.AppPrivateNetworkErr = errors.New("api down")
	fine := &fakeAppNetworkSettings{values: map[string]bool{}}
	if _, err := NewAppNetworkService(fine, activeProjects(), kube, nil).Describe(context.Background(), netTestProject); err == nil {
		t.Error("a failed cluster read must fail the view")
	}
	if _, err := NewAppNetworkService(fine, failingProjects{}, k8s.NewMockClient(), nil).Describe(context.Background(), netTestProject); err == nil {
		t.Error("a failed project lookup must fail the view")
	}
}

// No fallback: without a place to record the setting there is no service.
func TestAppNetworkServiceFor(t *testing.T) {
	settings := &fakeAppNetworkSettings{values: map[string]bool{}}
	if _, ok := AppNetworkServiceFor(struct{}{}, activeProjects(), k8s.NewMockClient(), nil); ok {
		t.Error("a store that cannot record the setting must mean no service")
	}
	if _, ok := AppNetworkServiceFor(settings, activeProjects(), nil, nil); ok {
		t.Error("no cluster client must mean no service")
	}
	if svc, ok := AppNetworkServiceFor(settings, activeProjects(), k8s.NewMockClient(), nil); !ok || svc == nil {
		t.Error("a store and a cluster must build the service")
	}
}

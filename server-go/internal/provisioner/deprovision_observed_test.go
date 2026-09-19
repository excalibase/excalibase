package provisioner

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// fastPoller advances its clock one interval per look, so a wait that never
// clears gives up after maxLooks checks without sleeping.
func fastPoller(maxLooks int) Poller {
	fired := make(chan time.Time)
	close(fired)
	looks := 0
	start := time.Unix(0, 0)
	return Poller{
		Interval: time.Second,
		Timeout:  time.Duration(maxLooks) * time.Second,
		Now: func() time.Time {
			looks++
			return start.Add(time.Duration(looks) * time.Second)
		},
		After: func(time.Duration) <-chan time.Time { return fired },
	}
}

// deprovisionDocker is a DockerClient that reports the container present
// until RemoveContainer lands — unless stuck, in which case it never goes.
type deprovisionDocker struct {
	stuck   bool
	missing bool
	removed bool
	stops   int
}

func (d *deprovisionDocker) CreateContainer(context.Context, string, string, map[string]string, map[string]string) (string, error) {
	return "ctr", nil
}
func (d *deprovisionDocker) StartContainer(context.Context, string) error { return nil }
func (d *deprovisionDocker) StopContainer(context.Context, string) error  { d.stops++; return nil }
func (d *deprovisionDocker) RemoveContainer(context.Context, string) error {
	d.removed = true
	return nil
}
func (d *deprovisionDocker) ContainerStatus(context.Context, string) (string, error) {
	if d.missing || (d.removed && !d.stuck) {
		return containerNotFound, nil
	}
	return "running", nil
}
func (d *deprovisionDocker) WaitForHealthy(context.Context, string) error { return nil }
func (d *deprovisionDocker) ExecInContainer(context.Context, string, []string) (int, error) {
	return 0, nil
}
func (d *deprovisionDocker) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}
func (d *deprovisionDocker) CopyFromContainer(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func newDockerProv(t *testing.T, docker DockerClient) *DockerPostgreSQLProvisioner {
	t.Helper()
	prov := NewDockerPostgreSQLProvisioner(docker)
	prov.SetDeletionPoller(fastPoller(3))
	return prov
}

func TestDockerDeprovisionWaitsForContainerToBeGone(t *testing.T) {
	docker := &deprovisionDocker{}
	if err := newDockerProv(t, docker).Deprovision(context.Background(), "ctr-1", "proj"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if !docker.removed {
		t.Error("container should have been removed")
	}
}

// Docker accepting the removal is not the same as the container being gone.
func TestDockerDeprovisionFailsWhileContainerSurvives(t *testing.T) {
	docker := &deprovisionDocker{stuck: true}
	err := newDockerProv(t, docker).Deprovision(context.Background(), "ctr-1", "proj")
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want a wait timeout", err)
	}
}

// A container an earlier attempt already removed counts as done.
func TestDockerDeprovisionIsIdempotent(t *testing.T) {
	docker := &deprovisionDocker{missing: true}
	if err := newDockerProv(t, docker).Deprovision(context.Background(), "ctr-1", "proj"); err != nil {
		t.Fatalf("already-gone container must count as removed: %v", err)
	}
	if docker.stops != 0 {
		t.Error("nothing to stop when the container is already gone")
	}
}

// The namespace wait covers pods and claims, not just the namespace object:
// a Terminating pod still holds the CPU request capacity plans against.
func TestNamespaceWaitReportsRemainingPodsAndClaims(t *testing.T) {
	mock := k8s.NewMockClient()
	const namespace = "org1-stuck"
	mock.Namespaces[namespace] = true
	mock.StuckNamespaces[namespace] = true
	mock.SetupPostgreSQLMock("stuck", namespace, 1)
	mock.PVCs[namespace] = []string{"stuck-postgres-1"}

	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(2))

	err := prov.Deprovision(context.Background(), namespace, "stuck")
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want a wait timeout", err)
	}
	for _, want := range []string{"namespace/" + namespace, "pod/stuck-postgres-1", "pvc/stuck-postgres-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got %v", want, err)
		}
	}
}

// A context that ends mid-wait reports that, not a false success.
func TestWaitUntilClearHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	poller := Poller{Interval: time.Hour, Timeout: time.Hour, Now: time.Now, After: time.After}
	err := poller.WaitUntilClear(ctx, "thing", func(context.Context) ([]string, error) {
		return []string{"thing"}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A check that cannot answer is an error, never "nothing left".
func TestWaitUntilClearReportsCheckErrors(t *testing.T) {
	poller := NewPoller(time.Millisecond, time.Millisecond)
	want := errors.New("api unreachable")
	err := poller.WaitUntilClear(context.Background(), "thing", func(context.Context) ([]string, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// A resource already gone clears on the first look.
func TestWaitUntilClearReturnsImmediatelyWhenClear(t *testing.T) {
	poller := NewPoller(time.Hour, time.Hour)
	if err := poller.WaitUntilClear(context.Background(), "thing", func(context.Context) ([]string, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("WaitUntilClear: %v", err)
	}
}

// The watcher must be stopped before anything else; a failure to stop it
// leaves a pod that can still publish, so teardown goes no further.
func TestDeprovisionStopsWhenWatcherUninstallFails(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.Namespaces["org1-watch"] = true
	mock.UninstallHelmError = errors.New("helm backend unavailable")
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(2))

	if err := prov.Deprovision(context.Background(), "org1-watch", "watch"); err == nil {
		t.Fatal("expected the watcher uninstall failure to be reported")
	}
	if !mock.Namespaces["org1-watch"] {
		t.Error("namespace must survive a teardown that never got past the watcher")
	}
}

// The database Cluster deleting is not the same as it being gone: CNPG
// finalizers keep the object until the data is released.
func TestDeprovisionWaitsForClusterToDisappear(t *testing.T) {
	mock := k8s.NewMockClient()
	const namespace = "org1-cluster"
	mock.Namespaces[namespace] = true
	mock.CRDs[namespace+"/cluster-postgres"] = nil
	mock.DeleteCRDError = nil
	// DeleteCRD is accepted but the object is kept, the way a finalizer
	// holds it: seed it again by making the delete a no-op.
	mock.StuckNamespaces[namespace] = true
	prov := NewPostgreSQLProvisioner(mock, "")
	prov.SetDeletionPoller(fastPoller(2))

	if err := prov.Deprovision(context.Background(), namespace, "cluster"); err == nil {
		t.Fatal("expected a wait on the namespace")
	}
}

// A cluster API that cannot be read is an error, never "already gone".
func TestNamespaceWaitReportsReadFailures(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*k8s.MockClient)
	}{
		{"namespace lookup", func(m *k8s.MockClient) { m.NamespaceExistsError = errors.New("api down") }},
		{"pod list", func(m *k8s.MockClient) { m.GetPodsError = errors.New("api down") }},
		{"claim list", func(m *k8s.MockClient) { m.ListPVCsError = errors.New("api down") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := k8s.NewMockClient()
			mock.Namespaces["org1-read"] = true
			tc.apply(mock)
			prov := NewPostgreSQLProvisioner(mock, "")
			prov.SetDeletionPoller(fastPoller(2))

			if err := prov.Deprovision(context.Background(), "org1-read", "read"); err == nil {
				t.Fatal("a failed check must not pass as a clear namespace")
			}
		})
	}
}

func TestDockerDeprovisionReportsStatusAndRemovalFailures(t *testing.T) {
	statusErr := &failingDocker{statusErr: errors.New("daemon unreachable")}
	if err := newDockerProv(t, statusErr).Deprovision(context.Background(), "c", "p"); err == nil {
		t.Error("a daemon that cannot be asked must not pass as removed")
	}
	removeErr := &failingDocker{removeErr: errors.New("container in use")}
	if err := newDockerProv(t, removeErr).Deprovision(context.Background(), "c", "p"); err == nil {
		t.Error("a removal that failed must be reported")
	}
}

// failingDocker reports the container running and fails the chosen call.
type failingDocker struct {
	deprovisionDocker
	statusErr error
	removeErr error
}

func (d *failingDocker) ContainerStatus(context.Context, string) (string, error) {
	if d.statusErr != nil {
		return "", d.statusErr
	}
	return "running", nil
}
func (d *failingDocker) RemoveContainer(context.Context, string) error { return d.removeErr }

// A poller with no injected clock falls back to the wall clock.
func TestPollerWithoutInjectedClockUsesWallClock(t *testing.T) {
	poller := Poller{Interval: time.Microsecond, Timeout: time.Microsecond}
	err := poller.WaitUntilClear(context.Background(), "thing", func(context.Context) ([]string, error) {
		return []string{"thing"}, nil
	})
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err = %v, want a wait timeout", err)
	}
}

package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// slowDiskProbe measures a stopped app's disk the way the cluster does: a Job
// that takes a while. started closes when the probe begins; finish lets it end.
type slowDiskProbe struct {
	*k8s.MockClient
	started chan struct{}
	finish  chan struct{}
}

func (p *slowDiskProbe) AppDiskUsage(ctx context.Context, namespace, appID string, disk apphost.AppDisk, opts k8s.DiskJobOptions) (k8s.AppDiskUsage, error) {
	close(p.started)
	<-p.finish
	return k8s.AppDiskUsage{UsedBytes: 1 << 20, SizeBytes: 5 << 30}, nil
}

// Studio reads the disk card again as soon as a pause settles, and for a
// stopped app that read runs a probe Job holding the app's lease. A resume
// pressed during it must wait for the probe, not be told the project is busy.
func TestResumeApp_WaitsForADiskMeasurementInsteadOfRefusing(t *testing.T) {
	f := diskLifecycleFixture(t)
	f.app.Status = apphost.StatusStopped
	setStoredApp(f.svc, f.app)
	probe := &slowDiskProbe{MockClient: f.kube, started: make(chan struct{}), finish: make(chan struct{})}
	f.svc.kube = probe

	measured := make(chan error, 1)
	go func() {
		_, err := f.svc.AppDiskStatus(context.Background(), f.app.ProjectID, f.app.ID)
		measured <- err
	}()
	<-probe.started
	go func() {
		time.Sleep(3 * deployLeaseRetry)
		close(probe.finish)
	}()

	app, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")
	if err != nil {
		t.Fatalf("resume during a disk measurement: %v, want it to wait and succeed", err)
	}
	if app.Status != apphost.StatusRunning {
		t.Fatalf("status = %q, want %q", app.Status, apphost.StatusRunning)
	}
	if err := <-measured; err != nil {
		t.Fatalf("measurement: %v", err)
	}
}

// Pause and delete queue the same way: a read of the disk never refuses them.
func TestPauseAndDeleteApp_WaitForADiskMeasurement(t *testing.T) {
	for name, run := range map[string]func(f *lifecycleFixture) error{
		"pause": func(f *lifecycleFixture) error {
			_, err := f.svc.PauseApp(context.Background(), f.app.ProjectID, f.app.ID)
			return err
		},
		"delete": func(f *lifecycleFixture) error {
			return f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := diskLifecycleFixture(t)
			release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationAppDiskUsage)
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				time.Sleep(3 * deployLeaseRetry)
				release()
			}()
			if err := run(f); err != nil {
				t.Fatalf("%s during a disk measurement: %v, want it to wait and succeed", name, err)
			}
		})
	}
}

// The wait is bounded: an operation that outlasts it is still refused, so a
// stuck holder cannot park requests forever.
func TestResumeApp_RefusedWhenTheLeaseOutlastsTheWait(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.lifecycleLeaseWait = 0
	release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationAppDiskUsage)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err == nil {
		t.Fatal("want ErrProjectOperationRunning")
	}
}

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-784: on a single host the closed WAL waits in the container until the sweep ships it, so a
// store that refuses uploads fills the container's disk the way R2 did on Kubernetes.
func TestHostPostgresWALSweep_AlertsWhenShippingKeepsFailingAndResolvesOnRecovery(t *testing.T) {
	adapter, store, _, _, _ := setupDockerAdapter(t)
	dc := &fakeDockerClientForAdapter{failOn: "copyFrom"}
	adapter.SetDockerClient(dc)
	on := true
	_ = store.Create(&domain.DatabaseInstance{ProjectID: "live", OrgID: "org", Namespace: "live", Status: "ACTIVE",
		DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, BackupEnabled: &on})
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	alerts, notifier := NewAlertingService(t.TempDir()), &walNotifier{}
	sweep := NewHostPostgresWALSweep(adapter, store)
	sweep.SetAlerts(alerts, notifier, 30*time.Minute, func() time.Time { return now })
	ctx := context.Background()

	sweep.Sweep(ctx)
	now = now.Add(20 * time.Minute)
	sweep.Sweep(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("raised after 20 minutes: %+v", got)
	}

	now = now.Add(11 * time.Minute)
	sweep.Sweep(ctx)
	sweep.Sweep(ctx)
	open := alerts.GetActiveAlerts()
	if len(open) != 1 || open[0].Metric != "wal.shipping:live" || !strings.Contains(open[0].Message, "live") {
		t.Fatalf("open alerts = %+v", open)
	}
	if len(notifier.changes) != 1 || notifier.changes[0].Raised == nil {
		t.Fatalf("notified %d times, want one raise", len(notifier.changes))
	}

	dc.failOn, dc.walArchive = "", map[string][]byte{segment: []byte("w")}
	sweep.Sweep(ctx)
	if got := alerts.GetActiveAlerts(); len(got) != 0 {
		t.Fatalf("still open after shipping worked: %+v", got)
	}
}

func TestHostPostgresWALSweep_WithoutAlertsStillSweeps(t *testing.T) {
	adapter, store, _, uploader, _ := setupDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{walArchive: map[string][]byte{segment: []byte("w")}})
	on := true
	_ = store.Create(&domain.DatabaseInstance{ProjectID: "live", OrgID: "org", Namespace: "live", Status: "ACTIVE",
		DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, BackupEnabled: &on})
	NewHostPostgresWALSweep(adapter, store).Sweep(context.Background())
	if len(uploader.objects) != 1 {
		t.Fatalf("shipped %v", uploader.objects)
	}
}

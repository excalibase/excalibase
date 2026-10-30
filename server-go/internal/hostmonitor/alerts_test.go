package hostmonitor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
)

func f64(v float64) *float64 { return &v }

func activeHost(t *testing.T) (*Monitor, *fakeEngine, *fakeStore, *recordedNotifier, *time.Time) {
	t.Helper()
	engine, store := baseEngine(), &fakeStore{}
	monitor, notifier, now := newTestMonitor(t, engine, store, project())
	setStats(engine, *now, 1, 1)
	return monitor, engine, store, notifier, now
}

func openAlerts(monitor *Monitor, projectID string) map[string]domain.Alert {
	open := map[string]domain.Alert{}
	for _, alert := range monitor.deps.Alerts.GetActiveAlertsForProject(projectID) {
		open[alert.Metric] = alert
	}
	return open
}

func TestHostFiguresOverTheirThresholdRaiseOnceAndResolve(t *testing.T) {
	monitor, _, store, notifier, now := activeHost(t)
	store.averages = hostmetrics.Averages{Samples: 5, CPUPercent: f64(95), MemoryPercent: f64(50), DiskPercent: f64(92), TemperatureCelsius: f64(60)}
	monitor.Sample(context.Background())
	open := openAlerts(monitor, "")
	if len(open) != 2 || open["host.cpu"].ID == "" || open["host.disk"].ID == "" {
		t.Fatalf("open = %+v, want cpu and disk with the default 90%% thresholds", open)
	}
	if !strings.Contains(open["host.cpu"].Message, "Host CPU averaged 95.0% over 5 min") || *open["host.cpu"].Threshold != 90 {
		t.Errorf("cpu alert = %+v", open["host.cpu"])
	}
	*now = now.Add(time.Minute)
	monitor.Sample(context.Background())
	if len(notifier.changes) != 2 {
		t.Errorf("a held breach is not sent again: %d notices", len(notifier.changes))
	}
	store.averages.CPUPercent = f64(40)
	*now = now.Add(time.Minute)
	monitor.Sample(context.Background())
	if _, still := openAlerts(monitor, "")["host.cpu"]; still {
		t.Error("cpu back under the threshold resolves")
	}
	last := notifier.changes[len(notifier.changes)-1]
	if last.Resolved == nil || last.Resolved.Metric != "host.cpu" {
		t.Errorf("the resolve is sent: %+v", last)
	}
}

func TestTooFewSamplesInTheWindowNeitherRaiseNorResolve(t *testing.T) {
	monitor, _, store, _, _ := activeHost(t)
	store.averages = hostmetrics.Averages{Samples: 2, CPUPercent: f64(99)}
	monitor.Sample(context.Background())
	if len(openAlerts(monitor, "")) != 0 {
		t.Error("two minutes of a five-minute window is not enough to alert")
	}
}

func TestSavedRulesReplaceTheDefaultsAndDisablingResolves(t *testing.T) {
	monitor, _, store, _, now := activeHost(t)
	store.averages = hostmetrics.Averages{Samples: 10, CPUPercent: f64(70), Load5: f64(9), NetworkBytesPerSec: f64(200e6 / 8)}
	store.settings = &domain.MonitoringAlertSettings{Enabled: true, CPUPercent: f64(60), Load5: f64(8), NetworkMbps: f64(100), WindowMinutes: 10, DownMinutes: 2}
	monitor.Sample(context.Background())
	open := openAlerts(monitor, "")
	if len(open) != 3 || open["host.cpu"].ID == "" || open["host.load"].ID == "" || open["host.network"].ID == "" {
		t.Fatalf("open = %+v", open)
	}
	if !strings.Contains(open["host.network"].Message, "200.0 Mbit/s") {
		t.Errorf("network alert = %q", open["host.network"].Message)
	}
	store.settings.Enabled = false
	*now = now.Add(time.Minute)
	monitor.Sample(context.Background())
	if len(openAlerts(monitor, "")) != 0 {
		t.Error("turning alerts off resolves what is open")
	}
}

func TestAStoppedDatabaseAlertsItsProjectAfterTheDownTime(t *testing.T) {
	monitor, engine, _, notifier, now := activeHost(t)
	engine.managed[0].State = "exited"
	monitor.Sample(context.Background())
	if len(openAlerts(monitor, "p1")) != 0 {
		t.Fatal("a container down for less than the down time does not alert yet")
	}
	*now = now.Add(2 * time.Minute)
	monitor.Sample(context.Background())
	alert, ok := openAlerts(monitor, "p1")["container.down:excalibase-shop-postgres"]
	if !ok || alert.Severity != "CRITICAL" || !strings.Contains(alert.Message, "not running (exited)") {
		t.Fatalf("p1 alerts = %+v", openAlerts(monitor, "p1"))
	}
	if len(openAlerts(monitor, "")) != 0 {
		t.Error("a tenant's container alert is the tenant's, not the platform's")
	}
	engine.managed[0].State = "running"
	*now = now.Add(time.Minute)
	monitor.Sample(context.Background())
	if len(openAlerts(monitor, "p1")) != 0 {
		t.Error("the restarted database resolves its alert")
	}
	if last := notifier.changes[len(notifier.changes)-1]; last.Resolved == nil {
		t.Errorf("the resolve is sent: %+v", last)
	}
}

func TestAPausedProjectsDatabaseIsNotAnOutage(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	engine.managed[0].State = "exited"
	paused := project()
	paused[0].Status = "PAUSED"
	monitor, _, now := newTestMonitor(t, engine, store, paused)
	setStats(engine, *now, 1, 1)
	store.settings = &domain.MonitoringAlertSettings{Enabled: true, DownMinutes: 0, WindowMinutes: 5}
	monitor.Sample(context.Background())
	if len(openAlerts(monitor, "p1")) != 0 {
		t.Errorf("a paused database is stopped on purpose: %+v", openAlerts(monitor, "p1"))
	}
}

func TestAMissingOrUnhealthyPlatformContainerAlertsThePlatform(t *testing.T) {
	monitor, engine, store, _, _ := activeHost(t)
	store.settings = &domain.MonitoringAlertSettings{Enabled: true, DownMinutes: 0, WindowMinutes: 5}
	delete(engine.platform, "excalibase-nats")
	unhealthy := engine.platform["excalibase-postgres"]
	unhealthy.Health = "unhealthy"
	engine.platform["excalibase-postgres"] = unhealthy
	monitor.Sample(context.Background())
	open := openAlerts(monitor, "")
	if !strings.Contains(open["container.down:excalibase-nats"].Message, "missing") ||
		!strings.Contains(open["container.down:excalibase-postgres"].Message, "unhealthy") {
		t.Errorf("platform alerts = %+v", open)
	}
}

func TestAnUnhealthyAppAlertsItsProjectButAStoppedAppDoesNot(t *testing.T) {
	monitor, engine, store, _, now := activeHost(t)
	store.settings = &domain.MonitoringAlertSettings{Enabled: true, DownMinutes: 0, WindowMinutes: 5}
	engine.managed[2].Health = "unhealthy"
	engine.managed[1].State = "exited"
	monitor.Sample(context.Background())
	open := openAlerts(monitor, "p1")
	if _, ok := open["container.down:excalibase-app-1"]; !ok {
		t.Errorf("unhealthy app: %+v", open)
	}
	if _, ok := open["container.down:excalibase-p1-watcher"]; ok {
		t.Error("only databases and the platform are expected to run")
	}
	engine.managed = engine.managed[:1]
	*now = now.Add(time.Minute)
	monitor.Sample(context.Background())
	if _, ok := openAlerts(monitor, "p1")["container.down:excalibase-app-1"]; ok {
		t.Error("an app that is gone resolves its alert")
	}
}

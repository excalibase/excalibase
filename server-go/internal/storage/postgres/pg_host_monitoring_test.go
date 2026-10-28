//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
)

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func hostAt(at time.Time, cpu float64) hostmetrics.Sample {
	count, source := 4, hostmetrics.NetworkFromHost
	return hostmetrics.Sample{
		Timestamp: at, CPUPercent: f64(cpu), CPUCount: &count, Load1: f64(1), Load5: f64(2), Load15: f64(3),
		MemoryTotalBytes: i64(1000), MemoryUsedBytes: i64(500), SwapTotalBytes: i64(10), SwapUsedBytes: i64(0),
		DiskTotalBytes: i64(2000), DiskUsedBytes: i64(1800), DiskReadBytesPerSec: f64(5), DiskWriteBytesPerSec: f64(6),
		NetworkRxBytesPerSec: f64(100), NetworkTxBytesPerSec: f64(200), NetworkSource: &source,
		TemperatureMaxCelsius: f64(50), Temperatures: []hostmetrics.Sensor{{Sensor: "coretemp Package id 0", Celsius: 50}},
	}
}

func TestHostMetrics_AppendLatestHistoryAndAverages(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	for minute := 0; minute < 10; minute++ {
		if err := store.AppendHostSample(ctx, hostAt(now.Add(time.Duration(minute-9)*time.Minute), float64(minute*10))); err != nil {
			t.Fatalf("AppendHostSample: %v", err)
		}
	}
	unmeasured := hostmetrics.Sample{Timestamp: now.Add(-time.Hour)}
	if err := store.AppendHostSample(ctx, unmeasured); err != nil {
		t.Fatalf("an all-nil sample is stored: %v", err)
	}

	latest, err := store.LatestHostSample(ctx)
	if err != nil || latest == nil {
		t.Fatalf("LatestHostSample: %v %v", latest, err)
	}
	if *latest.CPUPercent != 90 || len(latest.Temperatures) != 1 || latest.Temperatures[0].Celsius != 50 || *latest.NetworkSource != "host" {
		t.Errorf("latest = %+v", latest)
	}

	points, err := store.HostHistory(ctx, now.Add(-2*time.Hour), 5*time.Minute)
	if err != nil {
		t.Fatalf("HostHistory: %v", err)
	}
	if len(points) < 3 || points[0].CPUPercent != nil {
		t.Fatalf("history = %d points, first %+v; the unmeasured hour-old row is a gap", len(points), points[0])
	}
	for i := 1; i < len(points); i++ {
		if !points[i].Timestamp.After(points[i-1].Timestamp) {
			t.Errorf("history is oldest first: %v then %v", points[i-1].Timestamp, points[i].Timestamp)
		}
	}

	averages, err := store.HostAverages(ctx, now.Add(-4*time.Minute-time.Second))
	if err != nil {
		t.Fatalf("HostAverages: %v", err)
	}
	if averages.Samples != 5 || averages.CPUPercent == nil || *averages.CPUPercent != 70 {
		t.Errorf("averages = %+v, want 5 samples at cpu 50..90 = 70", averages)
	}
	if *averages.MemoryPercent != 50 || *averages.DiskPercent != 90 || *averages.TemperatureCelsius != 50 || *averages.Load5 != 2 || *averages.NetworkBytesPerSec != 300 {
		t.Errorf("averages = %+v", averages)
	}
}

func TestHostMetrics_EmptyTablesReadAsNothing(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	latest, err := store.LatestHostSample(ctx)
	if err != nil || latest != nil {
		t.Errorf("latest of nothing = %v, %v", latest, err)
	}
	containers, at, err := store.LatestContainerSamples(ctx)
	if err != nil || len(containers) != 0 || at != nil {
		t.Errorf("latest containers of nothing = %v %v %v", containers, at, err)
	}
	averages, err := store.HostAverages(ctx, time.Now().Add(-time.Hour))
	if err != nil || averages.Samples != 0 || averages.CPUPercent != nil {
		t.Errorf("averages of nothing = %+v %v", averages, err)
	}
}

func containerRow(name, project string, at time.Time, cores float64) domain.ContainerSample {
	return domain.ContainerSample{Name: name, ProjectID: project, Component: domain.ContainerDatabase, State: "running", Health: "healthy",
		Timestamp: at, CPUCores: f64(cores), CPUPercent: f64(cores * 25), MemoryBytes: i64(100), MemoryLimitBytes: i64(1000),
		NetworkRxBytesPerSec: f64(1), NetworkTxBytesPerSec: f64(2)}
}

func TestContainerMetrics_LatestHistoryAndProjectScope(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	for minute := 0; minute < 3; minute++ {
		at := now.Add(time.Duration(minute-2) * time.Minute)
		batch := []domain.ContainerSample{
			containerRow("excalibase-p1-postgres", "p1", at, float64(minute)),
			containerRow("excalibase-p2-postgres", "p2", at, 1),
			{Name: "excalibase-nats", Component: domain.ContainerPlatform, State: "exited", Timestamp: at},
		}
		if err := store.AppendContainerSamples(ctx, batch); err != nil {
			t.Fatalf("AppendContainerSamples: %v", err)
		}
	}
	if err := store.AppendContainerSamples(ctx, nil); err != nil {
		t.Errorf("appending nothing: %v", err)
	}

	latest, at, err := store.LatestContainerSamples(ctx)
	if err != nil || at == nil || !at.Equal(now) || len(latest) != 3 {
		t.Fatalf("latest = %+v at %v, %v", latest, at, err)
	}
	for _, sample := range latest {
		if sample.Name == "excalibase-nats" && (sample.State != "exited" || sample.CPUCores != nil) {
			t.Errorf("a stopped container has no figures: %+v", sample)
		}
		if sample.Name == "excalibase-p1-postgres" && (*sample.CPUCores != 2 || *sample.MemoryLimitBytes != 1000 || sample.Health != "healthy") {
			t.Errorf("p1 = %+v", sample)
		}
	}

	history, err := store.ContainerHistory(ctx, "excalibase-p1-postgres", now.Add(-time.Hour), time.Minute)
	if err != nil || len(history) != 3 || *history[0].CPUCores != 0 || *history[2].CPUCores != 2 {
		t.Fatalf("history = %+v, %v", history, err)
	}

	own, err := store.ProjectContainerHistory(ctx, "p1", now.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range own {
		if sample.ProjectID != "p1" || sample.Name != "excalibase-p1-postgres" {
			t.Errorf("project p1 read another's container: %+v", sample)
		}
	}
	if len(own) == 0 || *own[len(own)-1].CPUCores != 1 {
		t.Errorf("one hourly bucket averaging 0,1,2 = 1: %+v", own)
	}
}

func TestHostMonitoring_PruneKeepsThirtyDaysAndThinsOldContainerRows(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{time.Hour, 10 * 24 * time.Hour, 31 * 24 * time.Hour} {
		if err := store.AppendHostSample(ctx, hostAt(now.Add(-age), 1)); err != nil {
			t.Fatal(err)
		}
	}
	keptAligned := now.Add(-10 * 24 * time.Hour)         // minute 0: kept past seven days
	thinned := now.Add(-10*24*time.Hour + 3*time.Minute) // minute 3: thinned past seven days
	recent := now.Add(-time.Hour + 3*time.Minute)        // minute 3 but recent: kept
	expired := now.Add(-31 * 24 * time.Hour)             // older than thirty days
	for _, at := range []time.Time{keptAligned, thinned, recent, expired} {
		if err := store.AppendContainerSamples(ctx, []domain.ContainerSample{containerRow("c", "p", at, 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PruneHostMonitoring(ctx, now); err != nil {
		t.Fatalf("PruneHostMonitoring: %v", err)
	}
	hosts, err := store.HostHistory(ctx, now.Add(-60*24*time.Hour), time.Minute)
	if err != nil || len(hosts) != 2 {
		t.Errorf("host rows after prune = %d, %v; want the hour- and ten-day-old", len(hosts), err)
	}
	rows, err := store.ContainerHistory(ctx, "c", now.Add(-60*24*time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[time.Time]bool{}
	for _, row := range rows {
		kept[row.Timestamp.UTC()] = true
	}
	if len(rows) != 2 || !kept[keptAligned] || !kept[recent] {
		t.Errorf("container rows after prune = %v; want the aligned ten-day-old and the recent", kept)
	}
}

func TestMonitoringAlertSettings_DefaultsUntilSavedThenRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	got, found, err := store.GetMonitoringAlertSettings(ctx)
	if err != nil || found {
		t.Fatalf("nothing saved: %+v found=%v %v", got, found, err)
	}
	saved := domain.MonitoringAlertSettings{Enabled: false, CPUPercent: f64(80), DiskPercent: f64(95), Load5: f64(8),
		WindowMinutes: 10, DownMinutes: 3, Emails: []string{"ops@example.com"}, WebhookDisplay: "https://hooks.slack.com/..."}
	if err := store.SaveMonitoringAlertSettings(ctx, saved); err != nil {
		t.Fatal(err)
	}
	saved.CPUPercent = f64(85)
	if err := store.SaveMonitoringAlertSettings(ctx, saved); err != nil {
		t.Fatalf("a second save updates the one row: %v", err)
	}
	got, found, err = store.GetMonitoringAlertSettings(ctx)
	if err != nil || !found {
		t.Fatalf("saved settings: %v %v", found, err)
	}
	if got.Enabled || *got.CPUPercent != 85 || got.MemoryPercent != nil || *got.DiskPercent != 95 || *got.Load5 != 8 || got.TemperatureCelsius != nil ||
		got.WindowMinutes != 10 || got.DownMinutes != 3 || len(got.Emails) != 1 || got.Emails[0] != "ops@example.com" || got.WebhookDisplay != "https://hooks.slack.com/..." {
		t.Errorf("round trip = %+v", got)
	}
}

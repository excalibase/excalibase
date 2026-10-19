package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const diskTestExporter = "HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\n\r\n" +
	"cnpg_backends_total{state=\"active\",datname=\"app\"} 2\n" +
	"cnpg_pg_database_size_bytes{datname=\"app\"} 8388608\n"

// dfOutput is `df -P -k` of a volume of sizeKiB with usedKiB in use.
func dfOutput(sizeKiB, usedKiB string) string {
	return "Filesystem 1024-blocks Used Available Capacity Mounted on\n" +
		"/dev/sdb " + sizeKiB + " " + usedKiB + " 0 50% /data\n"
}

func newDiskMetricsService(t *testing.T, inst *domain.DatabaseInstance, answer func(pod string, cmd []string) (string, error), pods ...string) (*MetricsService, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	mock := k8s.NewMockClient()
	mock.ExecAnswerInPod = answer
	// Another workload in the namespace is never read as the database.
	for _, name := range append(pods, "web-7d9f-abc") {
		mock.Pods[inst.Namespace] = append(mock.Pods[inst.Namespace], corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: inst.Namespace}})
	}
	return NewMetricsService(store, mock, dir), mock
}

// EXC-651: diskUsagePercent and diskUsageGB were declared but never set. They
// come from the database volume's filesystem, read in the database container.
func TestPostgresMetricsReportTheDataVolumesUsage(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "pg", Namespace: "org-pg", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Free, DatabaseName: "app"}
	var dfCommands []string
	svc, _ := newDiskMetricsService(t, inst, func(pod string, cmd []string) (string, error) {
		if cmd[0] == "df" {
			dfCommands = append(dfCommands, pod+" "+strings.Join(cmd, " "))
			return dfOutput("10485760", "2621440"), nil // 10 GiB, 2.5 GiB used
		}
		return diskTestExporter, nil
	}, "pg-postgres-1")

	m, err := svc.GetCurrentMetrics(context.Background(), "pg")
	if err != nil {
		t.Fatal(err)
	}
	if m.DiskUsagePercent == nil || *m.DiskUsagePercent != 25 {
		t.Fatalf("diskUsagePercent = %v, want 25", m.DiskUsagePercent)
	}
	if m.DiskUsageGB == nil || *m.DiskUsageGB != 2 {
		t.Errorf("diskUsageGB = %v, want 2", m.DiskUsageGB)
	}
	if m.DiskUsedBytes == nil || *m.DiskUsedBytes != 2621440*1024 || m.DiskSizeBytes == nil || *m.DiskSizeBytes != 10485760*1024 {
		t.Errorf("disk bytes = %v of %v", m.DiskUsedBytes, m.DiskSizeBytes)
	}
	if len(dfCommands) != 1 || dfCommands[0] != "pg-postgres-1 df -P -k /var/lib/postgresql/data" {
		t.Errorf("df commands = %v, want the primary's data directory", dfCommands)
	}
}

// Every instance has its own volume; the fullest one is the one that stops
// the database. Instances are found by label, so one CNPG replaced (-4) counts.
func TestPostgresDiskUsageIsTheFullestInstances(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "pg3", Namespace: "org-pg3", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Standard, DatabaseName: "app"}
	used := map[string]string{"pg3-postgres-1": "1048576", "pg3-postgres-4": "9437184", "pg3-postgres-3": "2097152"}
	svc, _ := newDiskMetricsService(t, inst, func(pod string, cmd []string) (string, error) {
		if cmd[0] == "df" {
			return dfOutput("10485760", used[pod]), nil
		}
		return diskTestExporter, nil
	}, "pg3-postgres-1", "pg3-postgres-4", "pg3-postgres-3")
	m, err := svc.GetCurrentMetrics(context.Background(), "pg3")
	if err != nil {
		t.Fatal(err)
	}
	if m.DiskUsagePercent == nil || *m.DiskUsagePercent != 90 {
		t.Fatalf("diskUsagePercent = %v, want the fullest instance's 90", m.DiskUsagePercent)
	}
}

// A volume that cannot be read reports no figure rather than a guessed one.
func TestDiskUsageThatCannotBeReadStaysUnset(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "pg", Namespace: "org-pg", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Free, DatabaseName: "app"}
	svc, _ := newDiskMetricsService(t, inst, func(pod string, cmd []string) (string, error) {
		if cmd[0] == "df" {
			return "", errors.New("exec refused")
		}
		return diskTestExporter, nil
	}, "pg-postgres-1")
	m, err := svc.GetCurrentMetrics(context.Background(), "pg")
	if err != nil {
		t.Fatal(err)
	}
	if !m.MetricsAvailable || m.DiskUsagePercent != nil || m.DiskUsageGB != nil {
		t.Fatalf("available=%v disk=%v/%v, want the other metrics and no disk figure", m.MetricsAvailable, m.DiskUsagePercent, m.DiskUsageGB)
	}
}

// A MySQL project has no CNPG exporter; its disk comes from mysqld's data directory.
func TestMySQLMetricsReportTheDataVolumesUsage(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "my", Namespace: "org-my", Status: "ACTIVE",
		DBType: domain.MySQL, Tier: domain.Free, DatabaseName: "shop"}
	var execs []string
	svc, _ := newDiskMetricsService(t, inst, func(pod string, cmd []string) (string, error) {
		execs = append(execs, pod+" "+strings.Join(cmd, " "))
		if cmd[0] == "df" {
			return dfOutput("1048576", "524288"), nil
		}
		return "", errors.New("no exporter")
	}, "moco-my-mysql-0")
	m, err := svc.GetCurrentMetrics(context.Background(), "my")
	if err != nil {
		t.Fatal(err)
	}
	if m.DiskUsagePercent == nil || *m.DiskUsagePercent != 50 {
		t.Fatalf("diskUsagePercent = %v (%v), want 50", m.DiskUsagePercent, m.UnavailableReason)
	}
	if len(execs) != 1 || execs[0] != "moco-my-mysql-0 df -P -k /var/lib/mysql" {
		t.Errorf("execs = %v, want df of mysqld's data directory only", execs)
	}
}

// A single host has no Kubernetes to read from; the answer says so instead of failing.
func TestMetricsWithoutKubernetesSayTheyAreUnavailable(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "sh", Namespace: "sh", Status: "ACTIVE", DBType: domain.PostgreSQL, Tier: domain.Free}); err != nil {
		t.Fatal(err)
	}
	m, err := NewMetricsService(store, nil, dir).GetCurrentMetrics(context.Background(), "sh")
	if err != nil {
		t.Fatal(err)
	}
	if m.MetricsAvailable || m.UnavailableReason == nil {
		t.Fatalf("metrics = %+v, want unavailable with a reason", m)
	}
}

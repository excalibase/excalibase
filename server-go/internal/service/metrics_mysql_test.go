package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func limitedContainer(name, cpu, memory string) corev1.Container {
	return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory),
	}}}
}

// mocoPod is a MOCO instance pod: mysqld beside MOCO's agent sidecar.
func mocoPod(namespace, name, role, cpu, memory string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"moco.cybozu.com/role": role}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			limitedContainer("agent", "100m", "64Mi"),
			limitedContainer("mysqld", cpu, memory),
		}},
	}
}

// mocoUsage is metrics-server's reading of a MOCO pod, container by container.
func mocoUsage(name string, mysqldMillis, mysqldMB int64) k8s.PodResourceMetrics {
	return k8s.PodResourceMetrics{Name: name, CPUMillis: mysqldMillis + 50, MemoryMB: mysqldMB + 32, Containers: []k8s.ContainerResourceMetrics{
		{Name: "agent", CPUMillis: 50, MemoryMB: 32},
		{Name: "mysqld", CPUMillis: mysqldMillis, MemoryMB: mysqldMB},
	}}
}

func setupMySQLMetricsTest(t *testing.T, tier domain.TierType) (*MetricsService, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "my", Namespace: "org-my", Status: "ACTIVE",
		DBType: domain.MySQL, Tier: tier, DatabaseName: "shop", DeploymentMode: domain.ModeK8s}); err != nil {
		t.Fatal(err)
	}
	mock := k8s.NewMockClient()
	mock.WildcardExecOutput = dfOutput("1048576", "524288")
	return NewMetricsService(store, mock, dir), mock
}

// EXC-653: a MySQL project's CPU and memory are mysqld's own usage against
// mysqld's own limits; MOCO's sidecars are not the database.
func TestMySQLUsageIsMeasuredAgainstTheMysqldContainersOwnLimits(t *testing.T) {
	svc, mock := setupMySQLMetricsTest(t, domain.Free)
	mock.Pods["org-my"] = []corev1.Pod{mocoPod("org-my", "moco-my-mysql-0", "primary", "500m", "1Gi")}
	mock.Metrics["org-my"] = []k8s.PodResourceMetrics{mocoUsage("moco-my-mysql-0", 250, 256), {Name: "web-7d9f-abc", CPUMillis: 900, MemoryMB: 900}}

	m, err := svc.GetCurrentMetrics(context.Background(), "my")
	if err != nil {
		t.Fatal(err)
	}
	if !m.MetricsAvailable {
		t.Fatalf("unavailable: %v", m.UnavailableReason)
	}
	if !near(m.CPUUsageCores, 0.25) || m.MemoryUsageMB == nil || *m.MemoryUsageMB != 256 {
		t.Fatalf("usage = %v cores / %v MB, want mysqld's 0.25 / 256", deref(m.CPUUsageCores), m.MemoryUsageMB)
	}
	if !near(m.CPULimitCores, 0.5) || m.MemoryLimitMB == nil || *m.MemoryLimitMB != 1024 {
		t.Fatalf("limits = %v cores / %v MB, want mysqld's 0.5 / 1024", deref(m.CPULimitCores), m.MemoryLimitMB)
	}
	if !near(m.CPUUsagePercent, 50) || !near(m.MemoryUsagePercent, 25) {
		t.Fatalf("share = %v%% / %v%%, want 50 / 25", deref(m.CPUUsagePercent), deref(m.MemoryUsagePercent))
	}
	if len(m.Pods) != 1 || m.Pods[0].Name != "moco-my-mysql-0" || m.Pods[0].Role != "primary" || m.Pods[0].CPULimitCores != 0.5 {
		t.Fatalf("pods = %+v", m.Pods)
	}
}

// MOCO's primary moves on failover; the role is the one MOCO labels.
func TestMySQLPodRolesAreMOCOs(t *testing.T) {
	svc, mock := setupMySQLMetricsTest(t, domain.Standard)
	mock.Pods["org-my"] = []corev1.Pod{
		mocoPod("org-my", "moco-my-mysql-0", "replica", "1", "2Gi"),
		mocoPod("org-my", "moco-my-mysql-1", "primary", "1", "2Gi"),
		mocoPod("org-my", "moco-my-mysql-2", "replica", "1", "2Gi"),
	}
	mock.Metrics["org-my"] = []k8s.PodResourceMetrics{
		mocoUsage("moco-my-mysql-0", 100, 512), mocoUsage("moco-my-mysql-1", 400, 1024), mocoUsage("moco-my-mysql-2", 100, 512),
	}

	m, _ := svc.GetCurrentMetrics(context.Background(), "my")
	roles := map[string]string{}
	for _, pod := range m.Pods {
		roles[pod.Name] = pod.Role
	}
	if roles["moco-my-mysql-1"] != "primary" || roles["moco-my-mysql-0"] != "replica" || roles["moco-my-mysql-2"] != "replica" {
		t.Fatalf("roles = %v", roles)
	}
	if m.InstanceCount == nil || *m.InstanceCount != 3 || !near(m.CPULimitCores, 3) || !near(m.CPUUsagePercent, 20) {
		t.Fatalf("instances %v, limit %v, share %v", m.InstanceCount, deref(m.CPULimitCores), deref(m.CPUUsagePercent))
	}
}

// A mysqld without limits is measured against its tier's row, as Postgres is.
func TestMySQLUsageFallsBackToTheTierRow(t *testing.T) {
	svc, mock := setupMySQLMetricsTest(t, domain.Free)
	pod := mocoPod("org-my", "moco-my-mysql-0", "primary", "1", "1Gi")
	pod.Spec.Containers[1].Resources = corev1.ResourceRequirements{}
	mock.Pods["org-my"] = []corev1.Pod{pod}
	mock.Metrics["org-my"] = []k8s.PodResourceMetrics{mocoUsage("moco-my-mysql-0", 100, 256)}
	svc.SetTierLimits(func(context.Context, domain.TierType) (config.TierConfig, error) {
		return config.TierConfig{Instances: 1, CPU: "0.5", Memory: "512Mi"}, nil
	})

	m, _ := svc.GetCurrentMetrics(context.Background(), "my")
	if !near(m.CPUUsagePercent, 20) || !near(m.MemoryUsagePercent, 50) {
		t.Fatalf("share = %v%% / %v%%, want 20 / 50", deref(m.CPUUsagePercent), deref(m.MemoryUsagePercent))
	}
}

// The loop samples a running MySQL project, so its history fills like Postgres's.
func TestSampleAllRecordsARunningMySQLProject(t *testing.T) {
	metrics, history, _, _, mock := samplerFixture(t)
	mock.Pods["org-my-db"] = []corev1.Pod{mocoPod("org-my-db", "moco-my-db-mysql-0", "primary", "500m", "512Mi")}
	mock.Metrics["org-my-db"] = []k8s.PodResourceMetrics{mocoUsage("moco-my-db-mysql-0", 100, 128)}

	metrics.SampleAll(context.Background())

	var row *domain.DatabaseMetrics
	for i := range history.rows {
		if history.rows[i].ProjectID == "my-db" {
			row = &history.rows[i]
		}
	}
	if row == nil || !near(row.CPUUsagePercent, 20) || !near(row.MemoryUsagePercent, 25) {
		t.Fatalf("my-db row = %+v", row)
	}
	got, err := metrics.GetMetricsHistory(context.Background(), "my-db", 10)
	if err != nil || got.TotalPoints != 1 {
		t.Fatalf("history = %+v, %v", got, err)
	}
}

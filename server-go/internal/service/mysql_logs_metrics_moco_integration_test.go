//go:build integration

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-653 on a real MOCO cluster: a MySQL project's logs search reads its
// mysqld's log and its metrics measure mysqld against mysqld's own limits,
// as the CNPG integration test reads a Postgres project's.
//
//	MOCO_IT_KUBECONFIG=... [MOCO_IT_LOKI_URL=http://localhost:3100] \
//	go test -tags=integration ./internal/service/ -run TestMOCOLogsAndMetrics -v -timeout 20m
//
// With MOCO_IT_LOKI_URL the search must also find the lines the collector
// shipped, which is what proves its labels match the MOCO pods.
func TestMOCOLogsAndMetrics(t *testing.T) {
	kubeconfig := os.Getenv("MOCO_IT_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("MOCO_IT_KUBECONFIG is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	raw, err := k8s.NewClientWith(k8s.ClientOptions{KubeconfigPath: kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	client := raw.WithProjectAccess(k8s.ProjectAccess{ClusterRole: "cluster-admin", ServiceAccount: "default", Namespace: "default"})
	project := fmt.Sprintf("it653-%d", time.Now().UnixNano()%1_000_000)
	plan := config.TierConfig{Instances: 1, StorageSize: "2Gi", CPU: "0.5", Memory: "1Gi"}
	lab := &mocoLab{client: client}
	namespace := "org1-" + project
	t.Cleanup(func() { lab.cleanup(t, namespace) })
	result, err := provisioner.NewMySQLProvisioner(client).Provision(ctx, domain.ProvisioningRequest{ProjectName: project, OrgID: "org1",
		DBType: domain.MySQL, DatabaseName: "app", MySQLVersion: config.DefaultMySQLVersion()}, plan, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	store, _ := storage.NewFileSystemStore(t.TempDir())
	inst := &domain.DatabaseInstance{ProjectID: project, OrgID: "org1", Namespace: result.Namespace, DBType: domain.MySQL,
		DeploymentMode: domain.ModeK8s, Status: string(domain.StatusActive), Tier: domain.Free}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}

	svc := NewProvisioningService(store, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(client, "")), client)
	logs, err := svc.SearchLogs(ctx, project, LogSearch{Lines: 500, Term: "ready for connections"})
	if err != nil || !strings.Contains(logs, "ready for connections") {
		t.Fatalf("pod log search = %q, %v; want mysqld's startup line", logs, err)
	}
	if lokiURL := os.Getenv("MOCO_IT_LOKI_URL"); lokiURL != "" {
		requireLokiStream(ctx, t, lokiURL, databaseLogSelector(inst))
	}

	metrics := NewMetricsService(store, client, t.TempDir())
	var m *domain.DatabaseMetrics
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if m, err = metrics.GetCurrentMetrics(ctx, project); err != nil {
			t.Fatal(err)
		}
		// metrics-server reports a new pod after its first scrape.
		if m.CPUUsageCores != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Second)
	}
	if !m.MetricsAvailable || !near(m.CPULimitCores, 0.5) || m.MemoryLimitMB == nil || *m.MemoryLimitMB != 1024 {
		t.Fatalf("metrics = %+v; want mysqld's 0.5 core / 1024 MB limits", m)
	}
	if m.CPUUsagePercent == nil || m.MemoryUsagePercent == nil || len(m.Pods) != 1 || m.Pods[0].Role != "primary" {
		t.Fatalf("usage = %v / %v, pods %+v; want mysqld's share on the primary", deref(m.CPUUsagePercent), deref(m.MemoryUsagePercent), m.Pods)
	}
}

// requireLokiStream waits for the collector to ship a line under selector.
func requireLokiStream(ctx context.Context, t *testing.T, lokiURL, selector string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		end := time.Now()
		u := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=5", lokiURL, url.QueryEscape(selector),
			end.Add(-time.Hour).UnixNano(), end.UnixNano())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"values":[[`) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Loki holds no lines for %s", selector)
		}
		time.Sleep(10 * time.Second)
	}
}

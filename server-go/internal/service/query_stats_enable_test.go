package service

import (
	"context"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-654: a project created before the feature turns statistics on from its
// page; its cluster gains the settings once and keeps its own.
func TestEnableQueryStatsAddsTheSettingsToTheCluster(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	if err := svc.EnableQueryStats(context.Background(), testOpsDB); err != nil {
		t.Fatal(err)
	}
	cluster := opsCluster(t, mock)
	if !k8s.ClusterHasQueryStats(cluster) {
		t.Fatal("statistics not on")
	}
	if clusterField(t, cluster, "postgresql", "parameters", "work_mem") != "4MB" {
		t.Error("the tenant's settings were lost")
	}
}

// A second call does not touch the cluster, so it cannot restart it again.
func TestEnableQueryStatsOnAClusterThatHasThemChangesNothing(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	if err := svc.EnableQueryStats(context.Background(), testOpsDB); err != nil {
		t.Fatal(err)
	}
	updates := countCalls(mock, "UpdateCRD:")
	if err := svc.EnableQueryStats(context.Background(), testOpsDB); err != nil {
		t.Fatal(err)
	}
	if countCalls(mock, "UpdateCRD:") != updates {
		t.Error("the cluster was written again")
	}
}

func countCalls(mock *k8s.MockClient, prefix string) int {
	n := 0
	for _, call := range mock.Calls {
		if len(call) >= len(prefix) && call[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func TestEnableQueryStatsOnMySQLGrantsTheDigests(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	before := len(mock.ExecStdin)
	if err := svc.EnableQueryStats(context.Background(), inst.ProjectID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(mock.ExecStdin[before:], provisioner.MySQLQueryStatsGrantSQL) {
		t.Errorf("stdin %v", mock.ExecStdin[before:])
	}
}

// A MySQL project created with the feature on can be read from the start.
func TestProvisionMySQLWithQueryStatsGrantsTheDigests(t *testing.T) {
	for _, on := range []bool{false, true} {
		svc, store, mock := setupMySQLTest(t)
		svc.SetQueryStats(on)
		provisionMySQL(t, svc, store, "")
		if got := slices.Contains(mock.ExecStdin, provisioner.MySQLQueryStatsGrantSQL); got != on {
			t.Errorf("feature %v: grant ran = %v", on, got)
		}
	}
}

// A copy restored while the feature is on is created like a new project.
func TestK8sRestoreWithQueryStatsTurnsThemOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		mock := k8s.NewMockClient()
		adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
		adapter.SetQueryStats(on)
		if _, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if got := k8s.ClusterHasQueryStats(mock.CRDs["org-dst/dst-postgres"]); got != on {
			t.Errorf("feature %v: restored cluster statistics = %v", on, got)
		}
	}
}

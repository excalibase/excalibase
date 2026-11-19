package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func archiveTimeoutOf(cluster *unstructured.Unstructured) (string, bool) {
	value, found, _ := unstructured.NestedString(cluster.Object, "spec", "postgresql", "parameters", "archive_timeout")
	return value, found
}

// EXC-784: a plan that names an archive timeout gets it on a new cluster.
func TestBuildPostgreSQLCluster_CarriesThePlansArchiveTimeout(t *testing.T) {
	opts := clusterOpts("p", "ns")
	opts.Tier.ArchiveTimeout = "1h"
	if got, found := archiveTimeoutOf(BuildPostgreSQLCluster(opts)); !found || got != "1h" {
		t.Fatalf("archive_timeout = %q (found %v), want 1h", got, found)
	}
}

// A plan that names none leaves the operator's own five minutes alone.
func TestBuildPostgreSQLCluster_WithoutAPlanTimeoutSetsNoArchiveTimeout(t *testing.T) {
	if got, found := archiveTimeoutOf(BuildPostgreSQLCluster(clusterOpts("p", "ns"))); found {
		t.Fatalf("archive_timeout = %q, want it unset", got)
	}
}

// A project that moves off Free must not keep Free's hourly upload.
func TestWithTier_DropsTheArchiveTimeoutOfAPlanThatHasNone(t *testing.T) {
	free := clusterOpts("p", "ns")
	free.Tier = config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5", ArchiveTimeout: "1h"}
	cluster := BuildPostgreSQLCluster(free)

	moved := WithTier(cluster, standardTier(), "50Gi")
	if got, found := archiveTimeoutOf(moved); found {
		t.Fatalf("archive_timeout = %q after leaving Free, want it unset", got)
	}
}

func TestWithTier_GivesAFreePlanItsArchiveTimeout(t *testing.T) {
	tier := standardTier()
	tier.ArchiveTimeout = "1h"
	moved := WithTier(BuildPostgreSQLCluster(clusterOpts("p", "ns")), tier, "5Gi")
	if got, _ := archiveTimeoutOf(moved); got != "1h" {
		t.Fatalf("archive_timeout = %q, want 1h", got)
	}
}

func TestWithArchiveTimeout_ChangesOnlyWhatDiffers(t *testing.T) {
	cluster := BuildPostgreSQLCluster(clusterOpts("p", "ns"))
	changed, differs := WithArchiveTimeout(cluster, "1h")
	if !differs {
		t.Fatal("a cluster without the setting was reported as already right")
	}
	if got, _ := archiveTimeoutOf(changed); got != "1h" {
		t.Fatalf("archive_timeout = %q, want 1h", got)
	}
	if _, found := archiveTimeoutOf(cluster); found {
		t.Fatal("the input cluster was mutated")
	}
	if _, differs := WithArchiveTimeout(changed, "1h"); differs {
		t.Fatal("a cluster already at 1h was reported as different")
	}
}

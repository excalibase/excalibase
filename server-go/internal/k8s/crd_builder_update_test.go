package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func updatePolicy(t *testing.T, instances int) (strategy, method string) {
	t.Helper()
	opts := clusterOpts("dst", "org-dst")
	opts.Tier.Instances = instances
	spec := BuildPostgreSQLCluster(opts).Object
	strategy, _, _ = unstructured.NestedString(spec, "spec", "primaryUpdateStrategy")
	method, _, _ = unstructured.NestedString(spec, "spec", "primaryUpdateMethod")
	return strategy, method
}

// With standbys, an image update rolls them first and then switches the
// primary over to an updated one: clients reconnect instead of waiting out a
// restart.
func TestMultiInstanceClusterUpdatesThePrimaryBySwitchover(t *testing.T) {
	if strategy, method := updatePolicy(t, 3); strategy != "unsupervised" || method != "switchover" {
		t.Errorf("got %q/%q, want unsupervised/switchover", strategy, method)
	}
}

// A single instance has nothing to switch to; it restarts in place.
func TestSingleInstanceClusterUpdatesByRestartingInPlace(t *testing.T) {
	if strategy, method := updatePolicy(t, 1); strategy != "unsupervised" || method != "restart" {
		t.Errorf("got %q/%q, want unsupervised/restart", strategy, method)
	}
}

// EXC-532 (owner decision 2026-10-01): a multi-instance cluster acknowledges
// a commit only once a standby has it, so a lost primary loses no
// acknowledged write. Writes wait rather than proceed when no standby is up.
func synchronousOf(t *testing.T, cluster *unstructured.Unstructured) (map[string]interface{}, bool) {
	t.Helper()
	sync, found, err := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "synchronous")
	if err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	return sync, found
}

func assertQuorumOfOne(t *testing.T, cluster *unstructured.Unstructured) {
	t.Helper()
	sync, found := synchronousOf(t, cluster)
	if !found {
		t.Fatal("a multi-instance cluster must replicate synchronously")
	}
	if sync["method"] != "any" || sync["number"] != int64(1) || sync["dataDurability"] != "required" {
		t.Fatalf("synchronous = %v, want any/1/required", sync)
	}
}

func TestMultiInstanceClustersReplicateSynchronously(t *testing.T) {
	for _, instances := range []int{3, 5} {
		for _, documentDB := range []bool{false, true} {
			opts := clusterOpts("dst", "org-dst")
			opts.Tier.Instances = instances
			opts.DocumentDB = documentDB
			assertQuorumOfOne(t, BuildPostgreSQLCluster(opts))
		}
	}
}

func TestASingleInstanceClusterHasNoSynchronousStandby(t *testing.T) {
	opts := clusterOpts("dst", "org-dst")
	opts.Tier.Instances = 1
	if sync, found := synchronousOf(t, BuildPostgreSQLCluster(opts)); found {
		t.Fatalf("a single instance has no standby to wait for: %v", sync)
	}
}

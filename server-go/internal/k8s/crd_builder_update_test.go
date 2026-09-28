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

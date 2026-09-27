package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMultiInstanceClusterRequiresOneInstancePerNode(t *testing.T) {
	opts := clusterOpts("dst", "org-dst")
	opts.Tier.Instances = 3
	affinity, found, _ := unstructured.NestedMap(BuildPostgreSQLCluster(opts).Object, "spec", "affinity")
	if !found {
		t.Fatal("a multi-instance cluster must carry an anti-affinity rule")
	}
	if affinity["enablePodAntiAffinity"] != true ||
		affinity["podAntiAffinityType"] != "required" ||
		affinity["topologyKey"] != "kubernetes.io/hostname" {
		t.Errorf("instances must be required on separate hosts, got %v", affinity)
	}
}

func TestSingleInstanceClusterHasNoAntiAffinityRule(t *testing.T) {
	if _, found, _ := unstructured.NestedMap(BuildPostgreSQLCluster(clusterOpts("dst", "org-dst")).Object, "spec", "affinity"); found {
		t.Error("a single instance has nothing to spread; its cluster names no affinity")
	}
}

// EXC-497 made a restore render through the same sizing as a provision; this
// pins that the spread rule comes along with the tier's instances.
func TestRestoredMultiInstanceClusterKeepsTheSpreadRule(t *testing.T) {
	restored := mustBuildRestore(t, restoreOf(fullProjectCluster()))
	instances, _, _ := unstructured.NestedInt64(restored.Object, "spec", "instances")
	podAntiAffinity, _, _ := unstructured.NestedString(restored.Object, "spec", "affinity", "podAntiAffinityType")
	if instances != 3 || podAntiAffinity != "required" {
		t.Errorf("restore must keep the tier's instances on separate nodes, got %d / %q", instances, podAntiAffinity)
	}
}

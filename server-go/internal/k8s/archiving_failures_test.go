package k8s

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func clusterWithArchiving(name, status string, since time.Time, hibernated bool) *unstructured.Unstructured {
	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": name, "namespace": "ns-" + name},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True"},
			map[string]interface{}{"type": "ContinuousArchiving", "status": status,
				"lastTransitionTime": since.UTC().Format(time.RFC3339), "message": "Continuous archiving is failing"},
		}},
	}}
	if hibernated {
		cluster.SetAnnotations(map[string]string{"cnpg.io/hibernation": "on"})
	}
	return cluster
}

// EXC-784: only a False ContinuousArchiving counts, with the moment it turned False.
func TestArchivingFailures_ReportsAFalseConditionWithItsStart(t *testing.T) {
	since := time.Date(2026, 11, 5, 14, 55, 0, 0, time.UTC)
	failures := ArchivingFailures([]*unstructured.Unstructured{
		clusterWithArchiving("bad", "False", since, false),
		clusterWithArchiving("good", "True", since, false),
	})
	if len(failures) != 1 {
		t.Fatalf("failures = %+v", failures)
	}
	got := failures[0]
	if got.Cluster != "bad" || got.Namespace != "ns-bad" || !got.Since.Equal(since) {
		t.Fatalf("failure = %+v", got)
	}
}

// A paused project's cluster is hibernated: nothing runs, nothing archives, nothing is lost.
func TestArchivingFailures_IgnoresAHibernatedCluster(t *testing.T) {
	failures := ArchivingFailures([]*unstructured.Unstructured{clusterWithArchiving("paused", "False", time.Now(), true)})
	if len(failures) != 0 {
		t.Fatalf("failures = %+v", failures)
	}
}

func TestArchivingFailures_IgnoresClustersThatDoNotArchive(t *testing.T) {
	bare := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "bare"}}}
	if failures := ArchivingFailures([]*unstructured.Unstructured{bare, nil}); len(failures) != 0 {
		t.Fatalf("failures = %+v", failures)
	}
}

// A False condition with no readable time cannot be aged, so it is reported as just started rather than dropped.
func TestArchivingFailures_UnreadableTimeIsKeptWithAZeroSince(t *testing.T) {
	cluster := clusterWithArchiving("odd", "False", time.Now(), false)
	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	conditions[1].(map[string]interface{})["lastTransitionTime"] = "garbage"
	_ = unstructured.SetNestedSlice(cluster.Object, conditions, "status", "conditions")
	failures := ArchivingFailures([]*unstructured.Unstructured{cluster})
	if len(failures) != 1 || !failures[0].Since.IsZero() {
		t.Fatalf("failures = %+v", failures)
	}
}

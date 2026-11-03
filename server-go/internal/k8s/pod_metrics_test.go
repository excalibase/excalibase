package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func usage(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}
}

// EXC-653: a pod's reading keeps each container's share, so a database is
// measured without its sidecars.
func TestPodMetricsKeepEachContainersUsage(t *testing.T) {
	got := podResourceMetrics([]metricsv1beta1.PodMetrics{{
		ObjectMeta: metav1.ObjectMeta{Name: "moco-p-mysql-0"},
		Containers: []metricsv1beta1.ContainerMetrics{
			{Name: "agent", Usage: usage("20m", "16Mi")},
			{Name: "mysqld", Usage: usage("230m", "512Mi")},
		},
	}})
	if len(got) != 1 || got[0].Name != "moco-p-mysql-0" || got[0].CPUMillis != 250 || got[0].MemoryMB != 528 {
		t.Fatalf("pod = %+v", got)
	}
	mysqld, ok := got[0].Container("mysqld")
	if !ok || mysqld.CPUMillis != 230 || mysqld.MemoryMB != 512 {
		t.Fatalf("mysqld = %+v %v", mysqld, ok)
	}
	if _, ok := got[0].Container("postgres"); ok {
		t.Fatal("a container the pod does not run has no reading")
	}
}

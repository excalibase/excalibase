package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodLogTailReadsThePodsLog(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p-postgres-1-full-recovery-x", Namespace: "ns"},
	})}
	out, err := client.PodLogTail(context.Background(), "ns", "p-postgres-1-full-recovery-x", "full-recovery", 200)
	if err != nil {
		t.Fatalf("PodLogTail: %v", err)
	}
	if out == "" {
		t.Error("want the log the API returned")
	}
}

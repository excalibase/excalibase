package main

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func buildClusterClient(handler string) *k8s.Client {
	objects := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "builds"}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: handler},
	}
	return k8s.NewClientFromInterfaces(fake.NewSimpleClientset(objects...), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
}

// A class merely named gvisor that runs plain runc would put tenant builds on the host kernel.
func TestVerifyBuildCluster_HandlerMustBeGVisor(t *testing.T) {
	if err := verifyBuildCluster(buildClusterClient("runsc"), "builds", "is missing", "gvisor"); err != nil {
		t.Fatalf("runsc: %v", err)
	}
	err := verifyBuildCluster(buildClusterClient("runc"), "builds", "is missing", "gvisor")
	if err == nil || !strings.Contains(err.Error(), "runsc") {
		t.Fatalf("a non-gVisor handler must be refused, got %v", err)
	}
}

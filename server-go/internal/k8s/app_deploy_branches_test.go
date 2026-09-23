package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func failReactor(errMsg string) ktesting.ReactionFunc {
	return func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(errMsg)
	}
}

func TestApplyAppDeployment_GetErrorPropagates(t *testing.T) {
	c := newFakeClient()
	workload := mustRender(t, minimalApp(), newResolver())
	c.clientset.(*fake.Clientset).PrependReactor("get", "deployments", failReactor("boom"))

	err := c.ApplyAppWorkload(context.Background(), testNamespace, workload)
	if err == nil || !strings.Contains(err.Error(), "read app deployment") {
		t.Fatalf("expected a wrapped read error, got %v", err)
	}
}

func TestApplyAppDeployment_UpdateErrorPropagates(t *testing.T) {
	c := newFakeClient()
	workload := mustRender(t, minimalApp(), newResolver())
	if err := c.ApplyAppWorkload(context.Background(), testNamespace, workload); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	c.clientset.(*fake.Clientset).PrependReactor("update", "deployments", failReactor("boom"))

	err := c.ApplyAppWorkload(context.Background(), testNamespace, workload)
	if err == nil || !strings.Contains(err.Error(), "update app deployment") {
		t.Fatalf("expected a wrapped update error, got %v", err)
	}
}

func TestApplyAppEgressPolicy_GetErrorPropagates(t *testing.T) {
	c := newFakeClient()
	workload := mustRender(t, minimalApp(), newResolver())
	c.clientset.(*fake.Clientset).PrependReactor("get", "networkpolicies", failReactor("boom"))

	err := c.ApplyAppWorkload(context.Background(), testNamespace, workload)
	if err == nil || !strings.Contains(err.Error(), "read app egress policy") {
		t.Fatalf("expected a wrapped read error, got %v", err)
	}
}

func TestApplyAppEgressPolicy_UpdateErrorPropagates(t *testing.T) {
	c := newFakeClient()
	workload := mustRender(t, minimalApp(), newResolver())
	if err := c.ApplyAppWorkload(context.Background(), testNamespace, workload); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	c.clientset.(*fake.Clientset).PrependReactor("update", "networkpolicies", failReactor("boom"))

	err := c.ApplyAppWorkload(context.Background(), testNamespace, workload)
	if err == nil || !strings.Contains(err.Error(), "update app egress policy") {
		t.Fatalf("expected a wrapped update error, got %v", err)
	}
}

func TestWaitForAppRollout_PropagatesGetError(t *testing.T) {
	c := newFakeClient()
	err := c.WaitForAppRollout(context.Background(), testNamespace, "does-not-exist", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "read app deployment") {
		t.Fatalf("expected a wrapped read error, got %v", err)
	}
}

func TestConverged_NilReplicasIsFalse(t *testing.T) {
	if converged(&appsv1.Deployment{}) {
		t.Fatal("a deployment with no replica count cannot be converged")
	}
}

func TestCurrentReplicaSet_NoAnnotationIsNil(t *testing.T) {
	c := newFakeClient()
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app-web", Namespace: testNamespace}}
	if rs := c.currentReplicaSet(context.Background(), testNamespace, dep); rs != nil {
		t.Fatalf("expected nil with no revision annotation, got %+v", rs)
	}
}

func TestCurrentReplicaSet_NotFoundIsNil(t *testing.T) {
	c := newFakeClient()
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "app-web", Namespace: testNamespace, UID: "dep-1",
		Annotations: map[string]string{revisionAnnotation: "1"},
	}}
	if rs := c.currentReplicaSet(context.Background(), testNamespace, dep); rs != nil {
		t.Fatalf("expected nil with no matching replicaset, got %+v", rs)
	}
}

func TestBadPod_NoCurrentReplicaSetIsNotBad(t *testing.T) {
	c := newFakeClient()
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "app-web", Namespace: testNamespace, UID: "dep-1",
		Annotations: map[string]string{revisionAnnotation: "1"},
	}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}

	if _, _, bad := c.badPod(context.Background(), testNamespace, dep); bad {
		t.Fatal("with no current replicaset resolved, badPod must not report one")
	}
}

func TestWaitingReason_UnknownReasonIsNotBad(t *testing.T) {
	status := corev1.ContainerStatus{
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
	}
	if _, ok := waitingReason(status); ok {
		t.Fatal("an unrecognized waiting reason must not be reported as bad")
	}
}

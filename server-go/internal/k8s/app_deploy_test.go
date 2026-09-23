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
	"k8s.io/apimachinery/pkg/types"
)

func TestApplyAppWorkload_CreatesThenUpdates(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	app := fullApp()
	workload := mustRender(t, app, newResolver())

	if err := c.ApplyAppWorkload(ctx, testNamespace, workload); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	dep, err := c.clientset.AppsV1().Deployments(testNamespace).Get(ctx, AppObjectName(app.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("deployment should exist: %v", err)
	}
	if *dep.Spec.Replicas != int32(app.Replicas) {
		t.Errorf("replicas: got %d want %d", *dep.Spec.Replicas, app.Replicas)
	}
	if _, err := c.clientset.NetworkingV1().NetworkPolicies(testNamespace).Get(ctx, AppEgressPolicyName(app.Name), metav1.GetOptions{}); err != nil {
		t.Fatalf("network policy should exist: %v", err)
	}

	app.Replicas = 2
	updated := mustRender(t, app, newResolver())
	if err := c.ApplyAppWorkload(ctx, testNamespace, updated); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	dep, err = c.clientset.AppsV1().Deployments(testNamespace).Get(ctx, AppObjectName(app.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("deployment should still exist: %v", err)
	}
	if *dep.Spec.Replicas != 2 {
		t.Errorf("replicas after update: got %d want 2", *dep.Spec.Replicas)
	}
}

func TestApplyAppWorkload_RefusesNilWorkload(t *testing.T) {
	c := newFakeClient()
	if err := c.ApplyAppWorkload(context.Background(), testNamespace, nil); err == nil {
		t.Fatal("expected an error for a nil workload")
	}
}

func TestWaitForAppRollout_SucceedsWhenConverged(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	name := "app-web"
	one := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 1, Replicas: 1,
		},
	}
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	if err := c.WaitForAppRollout(ctx, testNamespace, name, 5*time.Second); err != nil {
		t.Fatalf("expected rollout success, got %v", err)
	}
}

func TestWaitForAppRollout_NotConvergedWhileOldPodsRemain(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	name := "app-web"
	want := int32(2)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &want},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, UpdatedReplicas: 2, AvailableReplicas: 2, Replicas: 3,
		},
	}
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	err := c.WaitForAppRollout(ctx, testNamespace, name, 300*time.Millisecond)
	if err == nil || !errors.Is(err, ErrAppRollout) {
		t.Fatalf("expected an ErrAppRollout timeout, got %v", err)
	}
}

func TestWaitForAppRollout_TimesOutWhenNeverReady(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	name := "app-web"
	one := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 0, AvailableReplicas: 0},
	}
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	err := c.WaitForAppRollout(ctx, testNamespace, name, 300*time.Millisecond)
	if err == nil || !errors.Is(err, ErrAppRollout) {
		t.Fatalf("expected an ErrAppRollout timeout, got %v", err)
	}
}

func seedCurrentReplicaSet(t *testing.T, c *Client, dep *appsv1.Deployment, revision string) types.UID {
	t.Helper()
	dep.Annotations = map[string]string{revisionAnnotation: revision}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: dep.Name + "-" + revision, Namespace: dep.Namespace, UID: types.UID(dep.Name + "-rs-" + revision),
		Annotations:     map[string]string{revisionAnnotation: revision},
		OwnerReferences: []metav1.OwnerReference{{UID: dep.UID}},
	}}
	if _, err := c.clientset.AppsV1().ReplicaSets(dep.Namespace).Create(context.Background(), rs, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed replicaset: %v", err)
	}
	return rs.UID
}

func TestWaitForAppRollout_FailsFastOnStuckContainer(t *testing.T) {
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff", "CreateContainerConfigError", "InvalidImageName"} {
		t.Run(reason, func(t *testing.T) { assertFailsFastOn(t, reason) })
	}
}

func assertFailsFastOn(t *testing.T, reason string) {
	c := newFakeClient()
	ctx := context.Background()
	name := "app-web"
	one := int32(1)
	selector := map[string]string{"excalibase.io/app": "app-01H"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: 1, UID: "dep-1"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 0, AvailableReplicas: 0},
	}
	rsUID := seedCurrentReplicaSet(t, c, dep, "1")
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-web-abc", Namespace: testNamespace, Labels: selector,
			OwnerReferences: []metav1.OwnerReference{{UID: rsUID}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "web",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: reason, Message: "stuck",
				}},
			}},
		},
	}
	if _, err := c.clientset.CoreV1().Pods(testNamespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pod: %v", err)
	}

	err := c.WaitForAppRollout(ctx, testNamespace, name, 5*time.Second)
	if err == nil || !errors.Is(err, ErrAppRollout) {
		t.Fatalf("expected an ErrAppRollout, got %v", err)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("error should name the reason: %v", err)
	}
}

func TestBadPod_IgnoresPreviousReplicaSetPods(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	selector := map[string]string{"excalibase.io/app": "app-01H"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app-web", Namespace: testNamespace, UID: "dep-1"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: selector}},
	}
	oldRSUID := seedCurrentReplicaSet(t, c, dep.DeepCopy(), "1")
	newRSUID := seedCurrentReplicaSet(t, c, dep, "2")

	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-web-old", Namespace: testNamespace, Labels: selector,
			OwnerReferences: []metav1.OwnerReference{{UID: oldRSUID}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "web",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason: "CrashLoopBackOff", Message: "back-off restarting failed container",
			}},
		}}},
	}
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-web-new", Namespace: testNamespace, Labels: selector,
			OwnerReferences: []metav1.OwnerReference{{UID: newRSUID}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	if _, err := c.clientset.CoreV1().Pods(testNamespace).Create(ctx, oldPod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed old pod: %v", err)
	}
	if _, err := c.clientset.CoreV1().Pods(testNamespace).Create(ctx, newPod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed new pod: %v", err)
	}

	if _, _, bad := c.badPod(ctx, testNamespace, dep); bad {
		t.Fatal("badPod must ignore the previous ReplicaSet's crashing pod")
	}
}

func TestWaitForAppRollout_IgnoresOldPodsBeforeTheControllerSeesTheNewSpec(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	one := int32(1)
	selector := map[string]string{"excalibase.io/app": "app-01H"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app-web", Namespace: testNamespace, Generation: 2, UID: "dep-1"},
		Spec:       appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: selector}},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
	}
	oldRS := seedCurrentReplicaSet(t, c, dep, "1")
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-web-old", Namespace: testNamespace, Labels: selector,
			OwnerReferences: []metav1.OwnerReference{{UID: oldRS}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "web",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}}}}},
	}
	if _, err := c.clientset.CoreV1().Pods(testNamespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pod: %v", err)
	}

	err := c.WaitForAppRollout(ctx, testNamespace, "app-web", 3*time.Second)
	if err == nil || strings.Contains(err.Error(), "ErrImagePull") {
		t.Fatalf("want a timeout, not the previous revision's pull error: %v", err)
	}
}

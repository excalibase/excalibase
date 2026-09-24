package k8s

import (
	"context"
	"errors"
	"reflect"
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
	if _, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, AppEgressPolicyName(app.Name)); err != nil {
		t.Fatalf("egress policy should exist: %v", err)
	}

	app.Replicas = 2
	updated, err := RenderAppWorkload(testNamespace, app, newResolver(), AppRenderOptions{
		RuntimeClass: testRuntimeClass, ExtraDenyCIDRs: []string{"203.0.113.9/32"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
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
	policy, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, AppEgressPolicyName(app.Name))
	if err != nil {
		t.Fatalf("egress policy should still exist: %v", err)
	}
	stored := egressSpec(t, &AppWorkload{EgressPolicy: policy})
	if !reflect.DeepEqual(stored, egressSpec(t, updated)) {
		t.Errorf("egress policy was not updated to the new spec: %+v", stored)
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
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "InvalidImageName"} {
		t.Run(reason, func(t *testing.T) {
			c := seedRollingPod(t, waitingStatus(reason, 0))
			assertRolloutFails(t, c, reason)
		})
	}
}

func TestWaitForAppRollout_ToleratesEarlyCrashLoops(t *testing.T) {
	c := seedRollingPod(t, waitingStatus("CrashLoopBackOff", crashLoopRestartLimit-1))
	err := c.WaitForAppRollout(context.Background(), testNamespace, "app-web", 300*time.Millisecond)
	if err == nil || strings.Contains(err.Error(), "CrashLoopBackOff") {
		t.Fatalf("want a timeout while restarts are below the limit, got %v", err)
	}
}

func TestWaitForAppRollout_FailsOnRepeatedCrashLoop(t *testing.T) {
	c := seedRollingPod(t, waitingStatus("CrashLoopBackOff", crashLoopRestartLimit))
	assertRolloutFails(t, c, "CrashLoopBackOff")
}

func TestWaitForAppRollout_FailsOnPersistentlyUnschedulablePod(t *testing.T) {
	c := seedRollingPod(t, unschedulableStatus(time.Now().Add(-unschedulableGrace-time.Second)))
	err := assertRolloutFails(t, c, "Unschedulable")
	if !strings.Contains(err.Error(), `"`+testRuntimeClass+`"`) {
		t.Errorf("error must name the runtime class: %v", err)
	}
}

func TestWaitForAppRollout_ToleratesBriefSchedulingDelay(t *testing.T) {
	c := seedRollingPod(t, unschedulableStatus(time.Now()))
	err := c.WaitForAppRollout(context.Background(), testNamespace, "app-web", 300*time.Millisecond)
	if err == nil || strings.Contains(err.Error(), "Unschedulable") {
		t.Fatalf("want a timeout, not an unschedulable failure, got %v", err)
	}
}

func waitingStatus(reason string, restarts int32) corev1.PodStatus {
	return corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:         "web",
		RestartCount: restarts,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "stuck"}},
	}}}
}

func unschedulableStatus(since time.Time) corev1.PodStatus {
	return corev1.PodStatus{Conditions: []corev1.PodCondition{{
		Type:               corev1.PodScheduled,
		Status:             corev1.ConditionFalse,
		Reason:             corev1.PodReasonUnschedulable,
		Message:            "0/1 nodes are available: 1 node(s) didn't match Pod's node affinity/selector.",
		LastTransitionTime: metav1.NewTime(since),
	}}}
}

func assertRolloutFails(t *testing.T, c *Client, reason string) error {
	t.Helper()
	err := c.WaitForAppRollout(context.Background(), testNamespace, "app-web", 5*time.Second)
	if !errors.Is(err, ErrAppRollout) {
		t.Fatalf("expected an ErrAppRollout, got %v", err)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("error should name %s: %v", reason, err)
	}
	return err
}

// seedRollingPod seeds a deployment mid-rollout whose one current pod has the given status.
func seedRollingPod(t *testing.T, status corev1.PodStatus) *Client {
	t.Helper()
	c := newFakeClient()
	ctx := context.Background()
	one := int32(1)
	runtimeClass := testRuntimeClass
	selector := map[string]string{"excalibase.io/app": "app-01H"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app-web", Namespace: testNamespace, Generation: 1, UID: "dep-1"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RuntimeClassName: &runtimeClass}},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1},
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
		Status: status,
	}
	if _, err := c.clientset.CoreV1().Pods(testNamespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed pod: %v", err)
	}
	return c
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

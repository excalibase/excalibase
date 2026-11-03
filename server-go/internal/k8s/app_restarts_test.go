package k8s

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func appPodWithStatus(name, appID string, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: map[string]string{
			"excalibase.io/app": appID, "app.kubernetes.io/managed-by": appManagedByValue}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: statuses},
	}
}

func restartedStatus(restarts int32, reason string, exitCode int32, finished time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: "web", RestartCount: restarts, Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(finished.Add(time.Second))}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: reason, ExitCode: exitCode, FinishedAt: metav1.NewTime(finished)}},
	}
}

// EXC-651: an app that was OOMKilled and restarted read ACTIVE everywhere.
// The restarts of its pods and the newest termination's reason are reported.
func TestAppRestarts_SumsThePodsAndNamesTheNewestTermination(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	c := newFakeClient(
		appPodWithStatus("web-a", "app-1", restartedStatus(2, "OOMKilled", 137, now.Add(-time.Minute))),
		appPodWithStatus("web-b", "app-1", restartedStatus(1, "Error", 1, now.Add(-time.Hour))),
		appPodWithStatus("other", "app-2", restartedStatus(5, "Error", 2, now)),
	)
	restarts, err := c.AppRestarts(context.Background(), testNamespace, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Count != 3 || restarts.LastReason != "OOMKilled" || restarts.LastExitCode == nil || *restarts.LastExitCode != 137 {
		t.Fatalf("restarts = %+v, want 3 with the newest OOMKilled (137)", restarts)
	}
	if restarts.LastAt == nil || !restarts.LastAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("lastAt = %v", restarts.LastAt)
	}
}

func TestAppRestarts_ACleanAppReportsNone(t *testing.T) {
	c := newFakeClient(appPodWithStatus("web-a", "app-1", corev1.ContainerStatus{Name: "web", Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}))
	restarts, err := c.AppRestarts(context.Background(), testNamespace, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Count != 0 || restarts.LastReason != "" || restarts.LastAt != nil {
		t.Fatalf("restarts = %+v, want none", restarts)
	}
}

// A container that has just died and not been restarted yet is the newest termination.
func TestAppRestarts_ReadsAContainerThatIsDownNow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	c := newFakeClient(appPodWithStatus("web-a", "app-1", corev1.ContainerStatus{Name: "web", RestartCount: 4,
		State:                corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now)}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(-time.Minute))}},
	}))
	restarts, err := c.AppRestarts(context.Background(), testNamespace, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Count != 4 || restarts.LastReason != "OOMKilled" {
		t.Fatalf("restarts = %+v, want the current OOMKilled", restarts)
	}
}

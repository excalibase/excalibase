package k8s

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func crashStatus(name string, restarts int32, reason string, exitCode int32, finished time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: name, RestartCount: restarts,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: reason, ExitCode: exitCode, FinishedAt: metav1.NewTime(finished)}},
	}
}

func podOf(name string, labels map[string]string, uid string, deleting bool, statuses ...corev1.ContainerStatus) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels, UID: types.UID(uid)},
		Status:     corev1.PodStatus{ContainerStatuses: statuses},
	}
	if deleting {
		now := metav1.Now()
		pod.DeletionTimestamp = &now
	}
	return pod
}

func crashAppLabels(appID string) map[string]string {
	return map[string]string{"excalibase.io/app": appID, "app.kubernetes.io/managed-by": appManagedByValue}
}

func TestAppContainerStates_ReportsEachContainerOfTheAppsPodsOnly(t *testing.T) {
	finished := time.Now().UTC().Truncate(time.Second)
	c := newFakeClient(
		podOf("web-a", crashAppLabels("app-1"), "uid-a", false, crashStatus("web", 4, "Error", 1, finished)),
		podOf("web-b", crashAppLabels("app-1"), "uid-b", true, crashStatus("web", 2, "Error", 143, finished)),
		podOf("other", crashAppLabels("app-2"), "uid-c", false, crashStatus("web", 9, "Error", 1, finished)),
	)
	states, err := c.AppContainerStates(context.Background(), testNamespace, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("states = %+v, want the two containers of app-1", states)
	}
	byUID := map[string]ContainerCrash{}
	for _, state := range states {
		byUID[state.PodUID] = state
	}
	a := byUID["uid-a"]
	if a.RestartCount != 4 || a.Container != "web" || a.Deleting || a.LastExit == nil ||
		a.LastExit.Reason != "Error" || a.LastExit.ExitCode != 1 || !a.LastExit.FinishedAt.Equal(finished) {
		t.Errorf("uid-a = %+v", a)
	}
	if !byUID["uid-b"].Deleting {
		t.Error("a pod being deleted must be marked, its exits are a rollout's")
	}
}

func TestAppContainerStates_ANeverRestartedContainerHasNoExit(t *testing.T) {
	c := newFakeClient(podOf("web-a", crashAppLabels("app-1"), "uid-a", false, corev1.ContainerStatus{Name: "web"}))
	states, err := c.AppContainerStates(context.Background(), testNamespace, "app-1")
	if err != nil || len(states) != 1 || states[0].LastExit != nil {
		t.Fatalf("states = %+v err = %v", states, err)
	}
}

func TestDatabaseContainerStates_ReadsCNPGAndMOCOPodsOnly(t *testing.T) {
	finished := time.Now().UTC()
	c := newFakeClient(
		podOf("pg-1", map[string]string{"cnpg.io/cluster": "proj-x-pg"}, "uid-pg", false, crashStatus("postgres", 3, "Error", 1, finished)),
		podOf("my-0", map[string]string{"app.kubernetes.io/created-by": "moco"}, "uid-my", false, crashStatus("mysqld", 5, "Error", 1, finished)),
		podOf("web", crashAppLabels("app-1"), "uid-app", false, crashStatus("web", 7, "Error", 1, finished)),
	)
	states, err := c.DatabaseContainerStates(context.Background(), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int32{}
	for _, state := range states {
		got[state.PodUID] = state.RestartCount
	}
	if len(got) != 2 || got["uid-pg"] != 3 || got["uid-my"] != 5 {
		t.Fatalf("got %v, want the Postgres and MySQL pods and not the app's", got)
	}
}

func TestSuspendAppWorkload_AnnotatesTheDeploymentWithWhyAndWhen(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	if err := c.SuspendAppWorkload(context.Background(), testNamespace, app.ID, "crash-loop", "11 crashes in the last hour", at); err != nil {
		t.Fatal(err)
	}
	annotations := readDeployment(t, c, app).Annotations
	if annotations[AppSuspendedAnnotation] != "crash-loop" ||
		annotations[AppSuspendedReasonAnnotation] != "11 crashes in the last hour" ||
		annotations[AppSuspendedAtAnnotation] != "2026-11-23T10:00:00Z" {
		t.Errorf("annotations = %v", annotations)
	}
}

func TestResumeAppWorkload_ClearsTheSuspension(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	convergeOnUpdate(clientset)
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.SuspendAppWorkload(context.Background(), testNamespace, app.ID, "crash-loop", "why", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.ResumeAppWorkload(context.Background(), testNamespace, app.ID, app.Name, app.Tier, time.Second); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{AppSuspendedAnnotation, AppSuspendedReasonAnnotation, AppSuspendedAtAnnotation} {
		if _, left := readDeployment(t, c, app).Annotations[key]; left {
			t.Errorf("%s survived a resume", key)
		}
	}
}

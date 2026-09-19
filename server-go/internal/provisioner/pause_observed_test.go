package provisioner

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// pauseObservationPoller drives the pause wait on a fake clock, calling tick
// on each round so a test can say when the operator finished shutting the
// pods down.
func pauseObservationPoller(tick func(round int)) Poller {
	now := time.Unix(0, 0)
	round := 0
	return Poller{
		Interval: time.Second,
		Timeout:  5 * time.Second,
		Now:      func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			now = now.Add(d)
			round++
			if tick != nil {
				tick(round)
			}
			ch := make(chan time.Time, 1)
			ch <- now
			return ch
		},
	}
}

func databasePod(name string, terminating bool) corev1.Pod {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: pauseTestNS,
		Labels:    map[string]string{"cnpg.io/podRole": "instance"},
	}}
	if terminating {
		deleted := metav1.Now()
		pod.DeletionTimestamp = &deleted
	}
	return pod
}

func TestPauseWaitsUntilNoDatabasePodIsRunning(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	// A pod that is Terminating is still a running pod as far as cluster
	// capacity is concerned, so the wait must not end on it.
	mock.Pods[pauseTestNS] = []corev1.Pod{databasePod(pauseTestCluster+"-1", true)}
	prov.SetPausePoller(pauseObservationPoller(func(round int) {
		if round == 2 {
			mock.Pods[pauseTestNS] = nil
		}
	}))

	if err := prov.Pause(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	assertAnnotation(t, storedCluster(t, mock), "cnpg.io/hibernation", "on")
}

func TestPauseFailsWhileADatabasePodIsStillTerminating(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	mock.Pods[pauseTestNS] = []corev1.Pod{databasePod(pauseTestCluster+"-1", true)}
	prov.SetPausePoller(pauseObservationPoller(nil))

	err := prov.Pause(context.Background(), pauseTestNS, pauseTestProject)
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err: got %v, want ErrWaitTimeout — a Terminating pod still holds its resources", err)
	}
}

func TestPauseReportsAPodListingItCannotRead(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	mock.GetPodsError = errors.New("apiserver unreachable")
	prov.SetPausePoller(pauseObservationPoller(nil))

	if err := prov.Pause(context.Background(), pauseTestNS, pauseTestProject); err == nil {
		t.Fatal("a pause that cannot see the pods must not claim they are gone")
	}
}

func TestStopReplicationRemovesTheTenantWatcher(t *testing.T) {
	mock, prov := setupHibernationTest(t)

	if err := prov.StopReplication(context.Background(), pauseTestNS, pauseTestProject); err != nil {
		t.Fatalf("StopReplication: %v", err)
	}
	want := "UninstallHelmChart:" + pauseTestNS + "/excalibase-watcher"
	if !slices.Contains(mock.Calls, want) {
		t.Errorf("the watcher release must be uninstalled, calls=%v", mock.Calls)
	}
}

func TestStopReplicationReportsAFailedUninstall(t *testing.T) {
	mock, prov := setupHibernationTest(t)
	mock.UninstallHelmError = errors.New("release lock held")

	if err := prov.StopReplication(context.Background(), pauseTestNS, pauseTestProject); err == nil {
		t.Fatal("a watcher that could not be stopped must fail the pause, not be ignored")
	}
}

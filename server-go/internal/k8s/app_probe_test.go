package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func httpProbe(path string) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt(8080)}},
		InitialDelaySeconds: 5, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 3,
	}
}

// probingPod is an app pod whose container started at started and is running but not ready.
func probingPod(started time.Time, probe *corev1.Probe) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-web-abc", Namespace: testNamespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", ReadinessProbe: probe}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "web", Ready: false,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}},
		}}},
	}
}

func unhealthyEvent(message string, count int32, last time.Time) corev1.Event {
	return corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "app-web-abc.1", Namespace: testNamespace},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "app-web-abc", Namespace: testNamespace, FieldPath: "spec.containers{web}"},
		Reason:         "Unhealthy", Message: message, Count: count,
		FirstTimestamp: metav1.NewTime(last.Add(-time.Duration(count) * 10 * time.Second)), LastTimestamp: metav1.NewTime(last),
		Type: corev1.EventTypeWarning,
	}
}

const notFound404 = "Readiness probe failed: HTTP probe failed with statuscode: 404"

// EXC-651: a health check path the app does not serve left the deploy
// "rolling out" for the whole progress deadline. An app that answers the
// path with a 4xx for the window is up and refusing it: fail with that reason.
func TestProbeRejection_FailsAPathTheAppAnswersWith4xxForTheWindow(t *testing.T) {
	now := time.Now()
	pod := probingPod(now.Add(-probeRejectionWindow-10*time.Second), httpProbe("/healthz"))
	events := []corev1.Event{unhealthyEvent(notFound404, 12, now.Add(-5*time.Second))}

	reason, message, bad := probeRejection(pod, events, now)
	if !bad || reason != probeRejectedReason {
		t.Fatalf("probeRejection = %q %q %v, want a %s", reason, message, bad, probeRejectedReason)
	}
	for _, want := range []string{"/healthz", "404", "healthCheckPath"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not name %s", message, want)
		}
	}
}

// A slow starter is never failed early: the window starts after the probe's
// initial delay, counted from the container's own start.
func TestProbeRejection_WaitsOutTheStartAndTheWindow(t *testing.T) {
	now := time.Now()
	young := probingPod(now.Add(-probeRejectionWindow+20*time.Second), httpProbe("/healthz"))
	events := []corev1.Event{unhealthyEvent(notFound404, 12, now)}
	if _, _, bad := probeRejection(young, events, now); bad {
		t.Fatal("a container younger than its initial delay plus the window was failed")
	}
	slowProbe := httpProbe("/healthz")
	slowProbe.InitialDelaySeconds = 300
	delayed := probingPod(now.Add(-4*time.Minute), slowProbe)
	if _, _, bad := probeRejection(delayed, events, now); bad {
		t.Fatal("a container still inside its initial delay was failed")
	}
}

// Only a sustained, current 4xx counts: too few checks, a stale event, a
// warming 503, a refused connection, or a container already ready are left
// to the deadline.
func TestProbeRejection_LeavesEverythingElseToTheDeadline(t *testing.T) {
	now := time.Now()
	old := now.Add(-probeRejectionWindow - time.Minute)
	ready := probingPod(old, httpProbe("/healthz"))
	ready.Status.ContainerStatuses[0].Ready = true
	cases := map[string]struct {
		pod    corev1.Pod
		events []corev1.Event
	}{
		"too few checks":          {probingPod(old, httpProbe("/healthz")), []corev1.Event{unhealthyEvent(notFound404, 3, now)}},
		"stale event":             {probingPod(old, httpProbe("/healthz")), []corev1.Event{unhealthyEvent(notFound404, 12, now.Add(-2*time.Minute))}},
		"503 while warming":       {probingPod(old, httpProbe("/healthz")), []corev1.Event{unhealthyEvent("Readiness probe failed: HTTP probe failed with statuscode: 503", 20, now)}},
		"connection refused":      {probingPod(old, httpProbe("/healthz")), []corev1.Event{unhealthyEvent(`Readiness probe failed: Get "http://10.0.0.1:8080/healthz": dial tcp 10.0.0.1:8080: connect: connection refused`, 20, now)}},
		"liveness, not readiness": {probingPod(old, httpProbe("/healthz")), []corev1.Event{unhealthyEvent("Liveness probe failed: HTTP probe failed with statuscode: 404", 20, now)}},
		"ready container":         {ready, []corev1.Event{unhealthyEvent(notFound404, 20, now)}},
		"no events":               {probingPod(old, httpProbe("/healthz")), nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if reason, message, bad := probeRejection(tc.pod, tc.events, now); bad {
				t.Fatalf("failed early: %s %s", reason, message)
			}
		})
	}
}

// The rollout reads the pod's probe events from the cluster and fails with them.
func TestWaitForAppRollout_FailsOnARejectedHealthCheckPath(t *testing.T) {
	now := time.Now()
	pod := probingPod(now.Add(-probeRejectionWindow-30*time.Second), httpProbe("/nope"))
	c := seedRollingPod(t, pod.Status)
	ctx := context.Background()
	seeded, err := c.clientset.CoreV1().Pods(testNamespace).Get(ctx, "app-web-abc", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seeded.Spec = pod.Spec
	if _, err := c.clientset.CoreV1().Pods(testNamespace).Update(ctx, seeded, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	event := unhealthyEvent(notFound404, 14, now)
	if _, err := c.clientset.CoreV1().Events(testNamespace).Create(ctx, &event, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	err = assertRolloutFails(t, c, probeRejectedReason)
	if !strings.Contains(err.Error(), "/nope") || !strings.Contains(err.Error(), "404") {
		t.Errorf("error must name the path and the answer: %v", err)
	}
}

// kubelet merges a pod's repeated probe events across container runs; only
// failures of this run, one per period, count (review of EXC-651).
func TestProbeRejection_CountsOnlyThisRunsDenseFailures(t *testing.T) {
	now := time.Now()
	started := now.Add(-probeRejectionWindow - 10*time.Second)
	pod := probingPod(started, httpProbe("/healthz"))

	carried := unhealthyEvent(notFound404, 14, now)
	carried.FirstTimestamp = metav1.NewTime(started.Add(-time.Minute))
	if _, _, bad := probeRejection(pod, []corev1.Event{carried}, now); bad {
		t.Error("an event that began in an earlier run failed this one")
	}

	sparse := unhealthyEvent(notFound404, 12, now)
	sparse.FirstTimestamp = metav1.NewTime(started.Add(time.Second))
	sparse.LastTimestamp = metav1.NewTime(now)
	if span := sparse.LastTimestamp.Sub(sparse.FirstTimestamp.Time); span <= 12*10*time.Second {
		t.Fatalf("setup: span %s is not sparse", span)
	}
	if _, _, bad := probeRejection(pod, []corev1.Event{sparse}, now); bad {
		t.Error("4xx answers mixed with other failures failed the run")
	}
}

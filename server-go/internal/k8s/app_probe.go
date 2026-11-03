package k8s

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// probeRejectionWindow is how long an app must keep refusing its health check
// path with a 4xx, after its probe's initial delay, before the deploy fails
// instead of waiting out the whole progress deadline (EXC-651). A 4xx means the
// app is up and answering, so the path itself is wrong; an app that is still
// starting (connection refused, 503) is left to the deadline.
const probeRejectionWindow = 2 * time.Minute

const (
	probeRejectedReason = "HealthCheckRejected"
	readinessFailed     = "Readiness probe failed: "
	httpStatusFailure   = "HTTP probe failed with statuscode: "
	// minEventFreshness keeps a probe event current even on a long period.
	minEventFreshness = 30 * time.Second
)

// probeEvents lists the Unhealthy events of one pod. A cluster that refuses
// the read leaves the rollout to its deadline rather than failing it.
func (c *Client) probeEvents(ctx context.Context, namespace, pod string) []corev1.Event {
	list, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Pod,involvedObject.name=" + pod + ",reason=Unhealthy",
	})
	if err != nil {
		log.Printf("app rollout: probe events of %s/%s: %v", namespace, pod, err)
		return nil
	}
	return list.Items
}

// awaitingProbe is true when some container has run past its probe's
// initial delay plus the window without becoming ready; only then is it worth
// reading the pod's events.
func awaitingProbe(pod corev1.Pod, now time.Time) bool {
	_, _, ok := rejectedContainer(pod, now)
	return ok
}

// probedRun is a container's current run and how long its probe has run.
type probedRun struct {
	container corev1.Container
	started   time.Time
}

// rejectedContainer is the first running, unready container with an HTTP
// readiness probe whose initial delay and window have both passed.
func rejectedContainer(pod corev1.Pod, now time.Time) (probedRun, time.Duration, bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Ready || status.State.Running == nil {
			continue
		}
		spec, found := containerSpec(pod, status.Name)
		probe := spec.ReadinessProbe
		if !found || probe == nil || probe.HTTPGet == nil {
			continue
		}
		started := status.State.Running.StartedAt.Time
		since := now.Sub(started) - time.Duration(probe.InitialDelaySeconds)*time.Second
		if since >= probeRejectionWindow {
			return probedRun{container: spec, started: started}, since, true
		}
	}
	return probedRun{}, 0, false
}

func containerSpec(pod corev1.Pod, name string) (corev1.Container, bool) {
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return container, true
		}
	}
	return corev1.Container{}, false
}

// probeRejection fails a pod whose app has answered its readiness path with a
// 4xx on every check of the window, and is still doing so.
func probeRejection(pod corev1.Pod, events []corev1.Event, now time.Time) (reason, message string, bad bool) {
	run, since, ok := rejectedContainer(pod, now)
	if !ok {
		return "", "", false
	}
	container := run.container
	probe := container.ReadinessProbe
	period := time.Duration(max(probe.PeriodSeconds, 1)) * time.Second
	needed := int32(math.Ceil(probeRejectionWindow.Seconds() / period.Seconds()))
	fresh := max(2*period, minEventFreshness)
	for _, event := range events {
		status, rejected := clientErrorStatus(event.Message)
		if !rejected || !aboutContainer(event, pod.Name, container.Name) {
			continue
		}
		count, first, last := eventSpan(event)
		// Every check of this run: the event began in it, spans the window,
		// is current, and has one 4xx per period (kubelet merges a pod's
		// repeats into one event across runs, so an older one says nothing).
		if first.Before(run.started) || count < needed || now.Sub(last) > fresh ||
			now.Sub(first) < probeRejectionWindow || last.Sub(first) > time.Duration(count)*period {
			continue
		}
		return probeRejectedReason, fmt.Sprintf(
			"the health check GET %s on port %s answered HTTP %s on every check for %s (%d checks); "+
				"the app is running but does not serve that path: fix healthCheckPath or serve it",
			probe.HTTPGet.Path, probe.HTTPGet.Port.String(), status, since.Round(time.Second), count), true
	}
	return "", "", false
}

// clientErrorStatus reads a readiness failure that is an HTTP 4xx answer.
func clientErrorStatus(message string) (string, bool) {
	rest, ok := strings.CutPrefix(message, readinessFailed+httpStatusFailure)
	if !ok {
		return "", false
	}
	status := strings.TrimSpace(rest)
	if len(status) < 3 || status[0] != '4' {
		return "", false
	}
	return status[:3], true
}

func aboutContainer(event corev1.Event, pod, container string) bool {
	return event.InvolvedObject.Name == pod &&
		(event.InvolvedObject.FieldPath == "" || event.InvolvedObject.FieldPath == "spec.containers{"+container+"}")
}

// eventSpan is how often and over what time an event was seen, in either the
// deduplicated (count) or the series form.
func eventSpan(event corev1.Event) (count int32, first, last time.Time) {
	count, first, last = event.Count, event.FirstTimestamp.Time, event.LastTimestamp.Time
	if event.Series != nil {
		count = max(count, event.Series.Count)
		last = event.Series.LastObservedTime.Time
	}
	if first.IsZero() {
		first = event.EventTime.Time
	}
	if last.IsZero() {
		last = event.EventTime.Time
	}
	return count, first, last
}

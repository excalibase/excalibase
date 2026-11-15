package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	// podUsageRaise is the share of a node's pod limit at which it is nearly full,
	// and podUsageResolve the share it must fall below to stop being so: the gap
	// keeps churn around one line from raising and resolving every minute.
	podUsageRaise   = 0.85
	podUsageResolve = 0.80
	// pendingAfter is how long a pod may wait before it is a problem.
	pendingAfter = 10 * time.Minute
)

// PodPressureSource is the cluster reads the check needs; only a Kubernetes client has them.
type PodPressureSource interface {
	GetClusterCapacity(ctx context.Context) (k8s.ClusterCapacity, error)
	PendingPods(ctx context.Context) ([]k8s.PendingPod, error)
}

// PodPressureMonitor raises a platform alert when a node nears its pod limit
// or a pod stays Pending (EXC-747). It runs on the metrics loop: the platform
// has no Prometheus (EXC-633), so its rules cannot live there.
type PodPressureMonitor struct {
	source   PodPressureSource
	alerts   *AlertingService
	now      func() time.Time
	notifier AlertNotifier
}

// AlertNotifier tells the people who run the platform that an alert changed.
type AlertNotifier interface {
	Notify(ctx context.Context, change AlertChange)
}

// SetNotifier sends every raised alert on; without one it is only recorded.
func (m *PodPressureMonitor) SetNotifier(notifier AlertNotifier) { m.notifier = notifier }

func (m *PodPressureMonitor) notify(ctx context.Context, change AlertChange) {
	if m.notifier != nil && (change.Raised != nil || change.Resolved != nil) {
		m.notifier.Notify(ctx, change)
	}
}

const nodePodsMetricPrefix = "cluster.pods:"

func NewPodPressureMonitor(source PodPressureSource, alerts *AlertingService, now func() time.Time) *PodPressureMonitor {
	return &PodPressureMonitor{source: source, alerts: alerts, now: now}
}

// Sample checks once. A read that fails raises and resolves nothing.
func (m *PodPressureMonitor) Sample(ctx context.Context) {
	m.checkNodes(ctx)
	m.checkPending(ctx)
}

func (m *PodPressureMonitor) checkNodes(ctx context.Context) {
	capacity, err := m.source.GetClusterCapacity(ctx)
	if err != nil {
		log.Printf("pod pressure: node capacity: %v", err)
		return
	}
	now := m.now()
	listed := map[string]bool{}
	for _, node := range capacity.Nodes {
		listed[nodePodsMetricPrefix+node.Name] = true
		if node.AllocatablePods == 0 {
			continue
		}
		ratio := float64(node.Pods) / float64(node.AllocatablePods)
		metric := nodePodsMetricPrefix + node.Name
		breached := ratio >= podUsageRaise || (ratio >= podUsageResolve && m.isOpen(metric))
		change := m.alerts.Track("", metric, breached, func() domain.Alert {
			percent, threshold := ratio*100, podUsageRaise*100
			return domain.Alert{
				ID: fmt.Sprintf("cluster.pods-%s-%d", node.Name, now.UnixNano()), Severity: "WARNING",
				Message: fmt.Sprintf("Node %s runs %d of %d pods; at the limit new pods stay Pending with \"Too many pods\"",
					node.Name, node.Pods, node.AllocatablePods),
				Value: &percent, Threshold: &threshold, Timestamp: &domain.FlexTime{Time: now},
			}
		})
		m.notify(ctx, change)
	}
	// A node that was cordoned or removed is no longer listed; its alert is over.
	for _, open := range m.alerts.GetActiveAlerts() {
		if open.ProjectID == "" && strings.HasPrefix(open.Metric, nodePodsMetricPrefix) && !listed[open.Metric] {
			m.alerts.Track("", open.Metric, false, nil)
		}
	}
}

func (m *PodPressureMonitor) isOpen(metric string) bool {
	for _, open := range m.alerts.GetActiveAlerts() {
		if open.ProjectID == "" && open.Metric == metric {
			return true
		}
	}
	return false
}

func (m *PodPressureMonitor) checkPending(ctx context.Context) {
	pods, err := m.source.PendingPods(ctx)
	if err != nil {
		log.Printf("pod pressure: pending pods: %v", err)
		return
	}
	now := m.now()
	var stuck []k8s.PendingPod
	for _, pod := range pods {
		if now.Sub(pod.Since) > pendingAfter {
			stuck = append(stuck, pod)
		}
	}
	change := m.alerts.Track("", "cluster.pending", len(stuck) > 0, func() domain.Alert {
		oldest := stuck[0]
		for _, pod := range stuck[1:] {
			if pod.Since.Before(oldest.Since) {
				oldest = pod
			}
		}
		return domain.Alert{
			ID: fmt.Sprintf("cluster.pending-%d", now.UnixNano()), Severity: "WARNING",
			Message: fmt.Sprintf("%d pod(s) unschedulable for more than %d minutes; oldest %s/%s for %s",
				len(stuck), int(pendingAfter.Minutes()), oldest.Namespace, oldest.Name, now.Sub(oldest.Since).Round(time.Minute)),
			Timestamp: &domain.FlexTime{Time: now},
		}
	})
	m.notify(ctx, change)
}

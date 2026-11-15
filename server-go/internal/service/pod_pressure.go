package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	// podUsageThreshold is the share of a node's pod limit above which it is nearly full.
	podUsageThreshold = 0.85
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
	source PodPressureSource
	alerts *AlertingService
	now    func() time.Time
}

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
	for _, node := range capacity.Nodes {
		if node.AllocatablePods == 0 {
			continue
		}
		ratio := float64(node.Pods) / float64(node.AllocatablePods)
		m.alerts.Track("", "cluster.pods:"+node.Name, ratio > podUsageThreshold, func() domain.Alert {
			percent, threshold := ratio*100, podUsageThreshold*100
			return domain.Alert{
				ID: fmt.Sprintf("cluster.pods-%s-%d", node.Name, now.UnixNano()), Severity: "WARNING",
				Message: fmt.Sprintf("Node %s runs %d of %d pods; at the limit new pods stay Pending with \"Too many pods\"",
					node.Name, node.Pods, node.AllocatablePods),
				Value: &percent, Threshold: &threshold, Timestamp: &domain.FlexTime{Time: now},
			}
		})
	}
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
	m.alerts.Track("", "cluster.pending", len(stuck) > 0, func() domain.Alert {
		oldest := stuck[0]
		for _, pod := range stuck[1:] {
			if pod.Since.Before(oldest.Since) {
				oldest = pod
			}
		}
		return domain.Alert{
			ID: fmt.Sprintf("cluster.pending-%d", now.UnixNano()), Severity: "WARNING",
			Message: fmt.Sprintf("%d pod(s) Pending for more than %d minutes; oldest %s/%s for %s",
				len(stuck), int(pendingAfter.Minutes()), oldest.Namespace, oldest.Name, now.Sub(oldest.Since).Round(time.Minute)),
			Timestamp: &domain.FlexTime{Time: now},
		}
	})
}

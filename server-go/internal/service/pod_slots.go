package service

import (
	"errors"
	"log"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ErrNoPodSlots refuses work the nodes' pod limit cannot take. Without the
// check the pods sit Pending with "Too many pods" and nobody is told (EXC-747).
// The numbers go to the log only.
var ErrNoPodSlots = errors.New("the platform has no free pod slots right now; try again later or contact support")

// projectSidecarPods are the pods every project runs beside its database
// instances: the CDC watcher and the function runtime.
const projectSidecarPods = 2

// requirePodSlots refuses when the nodes report a pod limit and fewer than
// count slots are free. A cluster that reports none is not refused.
func requirePodSlots(capacity k8s.ClusterCapacity, count int64, who string) error {
	if count <= 0 || !capacity.PodSlotsKnown() || capacity.FreePods() >= count {
		return nil
	}
	log.Printf("INFO: %s refused (pod slots): need=%d free=%d pods=%d allocatable=%d",
		who, count, capacity.FreePods(), capacity.Pods, capacity.AllocatablePods)
	return ErrNoPodSlots
}

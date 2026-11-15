package service

import (
	"context"
	"errors"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ErrNoPodSlots refuses work the nodes' pod limit cannot take. Without the
// check the pods sit Pending with "Too many pods" and nobody is told (EXC-747).
// The numbers go to the log only.
var ErrNoPodSlots = errors.New("the platform has no free pod slots right now; try again later or contact support")

// projectSidecarPods are the pods every project runs beside its database
// instances: the CDC watcher and the function runtime.
const projectSidecarPods = 2

// PodSlotSource reads the nodes' pod slots.
type PodSlotSource interface {
	GetClusterCapacity(ctx context.Context) (k8s.ClusterCapacity, error)
}

// SetPodSlots makes a resume check that the database's pods have slots to start in.
func (s *PauseService) SetPodSlots(source PodSlotSource) { s.podSlots = source }

// requirePodSlotsToResume refuses a resume with no slot for the database's
// instances and sidecars. A capacity read that fails does not refuse: the
// project is paused already, and the resume itself reports what then happens.
func (s *PauseService) requirePodSlotsToResume(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.podSlots == nil || inst.DeploymentMode != domain.ModeK8s {
		return nil
	}
	instances := 1
	if tier, err := config.GetTierConfig(inst.Tier); err == nil {
		instances = max(tier.Instances, 1)
	}
	capacity, err := s.podSlots.GetClusterCapacity(ctx)
	if err != nil {
		log.Printf("resume %s: pod slots unknown: %v", inst.ProjectID, err)
		return nil
	}
	return requirePodSlots(capacity, int64(instances+projectSidecarPods), "resume of "+inst.ProjectID)
}

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

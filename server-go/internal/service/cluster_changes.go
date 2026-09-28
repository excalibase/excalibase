package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Cluster changes an admin makes to a running database (EXC-492).
const (
	OperationResize     ProjectOperation = "disk resize"
	OperationTierChange ProjectOperation = "tier change"
	OperationTune       ProjectOperation = "parameter change"
)

var (
	// ErrKubernetesOnly refuses a cluster change on a project with no CNPG cluster.
	ErrKubernetesOnly = errors.New("this change is available on Kubernetes projects only")
	// ErrInvalidStorageSize refuses a size that is not a whole number of GiB.
	ErrInvalidStorageSize = errors.New("the disk size must be a whole number of gibibytes, such as 10Gi")
	// ErrStorageShrink refuses a size at or below the current disk: a volume
	// can only grow, never shrink.
	ErrStorageShrink = errors.New("a database disk can only grow; it cannot be shrunk")
	// ErrStorageAbovePlan refuses a disk larger than the project's plan gives.
	ErrStorageAbovePlan = errors.New("the disk size is larger than the project's plan allows")
	// ErrTierNotOrgPlan refuses a tier other than the organization's plan: a
	// project's tier follows its organization (EXC-470).
	ErrTierNotOrgPlan = errors.New("a project's tier follows its organization's plan")
	// ErrDiskAbovePlanMax refuses a plan whose maximum disk is below the
	// project's current one, since the disk cannot shrink.
	ErrDiskAbovePlanMax = errors.New("the project's disk is larger than the plan allows, and a disk cannot shrink")
	// ErrTierParametersOutOfBounds refuses a plan the project's Postgres
	// settings do not fit; they have to be lowered first.
	ErrTierParametersOutOfBounds = errors.New("the project's Postgres settings are outside the plan's bounds; lower them first")
	// ErrPlanDoesNotFit refuses a plan whose instances would not all find a
	// node with room: a restarted instance left Pending is a database down.
	ErrPlanDoesNotFit = errors.New("the platform has no room for the plan's size right now")
)

// ClusterSettings is what an admin sees before resizing, re-tiering or tuning.
type ClusterSettings struct {
	ProjectID   string          `json:"projectId"`
	Tier        domain.TierType `json:"tier"`
	OrgTier     domain.TierType `json:"orgTier"`
	StorageSize string          `json:"storageSize"`
	// StorageStart is the disk the plan starts with, StorageLimit the most it
	// may grow to; StorageUsedBytes is what the databases take, absent when
	// it could not be read.
	StorageStart      string            `json:"storageStart"`
	StorageLimit      string            `json:"storageLimit"`
	StorageUsedBytes  *int64            `json:"storageUsedBytes"`
	Instances         int64             `json:"instances"`
	CPU               string            `json:"cpu"`
	Memory            string            `json:"memory"`
	Parameters        map[string]string `json:"parameters"`
	TunableParameters []string          `json:"tunableParameters"`
}

// wholeGibibytes is the only disk size format accepted: what every tier uses.
var wholeGibibytes = regexp.MustCompile(`^[1-9][0-9]{0,5}Gi$`)

// ClusterSettings reads the project's cluster as it is, and its plan.
func (s *ProvisioningService) ClusterSettings(ctx context.Context, projectID string) (*ClusterSettings, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}
	if err := s.requireKubernetes(inst); err != nil {
		return nil, err
	}
	tier, err := s.tierConfig(ctx, inst.Tier)
	if err != nil {
		return nil, err
	}
	orgTier, err := s.orgTier(ctx, inst.OrgID)
	if err != nil {
		return nil, err
	}
	cluster, err := s.projectCluster(ctx, inst)
	if err != nil {
		return nil, err
	}
	size, err := k8s.ClusterStorageSize(cluster)
	if err != nil {
		return nil, err
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	cpu, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "limits", "cpu")
	memory, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "limits", "memory")
	return &ClusterSettings{
		ProjectID: projectID, Tier: inst.Tier, OrgTier: orgTier,
		StorageSize: size.String(), StorageStart: tier.StorageSize, StorageLimit: tier.MaxStorageSize,
		StorageUsedBytes: s.databasesSize(ctx, inst, cluster),
		Instances:        instances, CPU: cpu, Memory: memory,
		Parameters:        k8s.TenantParameters(cluster),
		TunableParameters: config.TenantTunableParameterNames(),
	}, nil
}

// ResizeStorage grows the project's disk to size, up to its plan's disk.
func (s *ProvisioningService) ResizeStorage(ctx context.Context, projectID, size string) error {
	if !wholeGibibytes.MatchString(size) {
		return ErrInvalidStorageSize
	}
	requested := resource.MustParse(size)

	inst, release, err := s.holdProject(ctx, projectID, OperationResize, requireActive)
	if err != nil {
		return err
	}
	defer release()
	if err := s.requireKubernetes(inst); err != nil {
		return err
	}
	tier, err := s.tierConfig(ctx, inst.Tier)
	if err != nil {
		return err
	}
	limit, err := resource.ParseQuantity(tier.MaxStorageSize)
	if err != nil {
		return fmt.Errorf("tier %s maximum disk %q: %w", inst.Tier, tier.MaxStorageSize, err)
	}
	cluster, err := s.projectCluster(ctx, inst)
	if err != nil {
		return err
	}
	current, err := k8s.ClusterStorageSize(cluster)
	if err != nil {
		return err
	}
	if requested.Cmp(current) <= 0 {
		return fmt.Errorf("%w: the disk is %s and %s is not larger", ErrStorageShrink, current.String(), size)
	}
	if requested.Cmp(limit) > 0 {
		return fmt.Errorf("%w: the %s plan allows up to %s", ErrStorageAbovePlan, inst.Tier, tier.MaxStorageSize)
	}
	if err := s.k8sClient.ClusterVolumesExpandable(ctx, inst.Namespace, cluster.GetName()); err != nil {
		return err
	}
	return s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, k8s.WithStorageSize(cluster, size))
}

// ApplyOrgTier moves the project's cluster onto its organization's plan: the
// plan's instances (one per node), CPU, memory, disk and statement guard.
// tier is the plan the caller expects the organization to be on.
func (s *ProvisioningService) ApplyOrgTier(ctx context.Context, projectID string, tier domain.TierType) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationTierChange, requireActive)
	if err != nil {
		return err
	}
	defer release()
	if err := s.requireKubernetes(inst); err != nil {
		return err
	}
	orgTier, err := s.orgTier(ctx, inst.OrgID)
	if err != nil {
		return err
	}
	if tier != orgTier {
		return fmt.Errorf("%w: the organization is on %s, not %s", ErrTierNotOrgPlan, orgTier, tier)
	}
	plan, err := s.tierConfig(ctx, orgTier)
	if err != nil {
		return err
	}
	if err := k8s.ValidateTierSizing(plan); err != nil {
		return err
	}
	cluster, err := s.projectCluster(ctx, inst)
	if err != nil {
		return err
	}
	if err := config.ValidateTenantParameters(k8s.TenantParameters(cluster), plan); err != nil {
		return fmt.Errorf("%w: %v", ErrTierParametersOutOfBounds, err)
	}
	disk, err := s.planDisk(ctx, inst, cluster, plan)
	if err != nil {
		return err
	}
	if err := s.RequireNodeCount(ctx, orgTier, plan.Instances); err != nil {
		return err
	}
	if err := s.requireRoomForPlan(ctx, inst, cluster, plan); err != nil {
		return err
	}
	if err := s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, k8s.WithTier(cluster, plan, disk)); err != nil {
		return fmt.Errorf("apply the %s plan to the cluster: %w", orgTier, err)
	}
	previousTier := inst.Tier
	inst.Tier = orgTier
	if err := s.store.UpdateIfStatus(inst, inst.Status); err != nil {
		inst.Tier = previousTier
		return errors.Join(fmt.Errorf("record the %s plan: %w", orgTier, err), s.restoreSizing(ctx, inst, cluster))
	}
	return nil
}

// restoreSizing puts back the size a tier change replaced when the change
// could not be recorded, so tuning and resizing, which the recorded tier
// bounds, match the cluster. It outlives a caller that gave up.
func (s *ProvisioningService) restoreSizing(ctx context.Context, inst *domain.DatabaseInstance, previous *unstructured.Unstructured) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revertTimeout)
	defer cancel()
	current, err := s.projectCluster(ctx, inst)
	if err != nil {
		return fmt.Errorf("put the previous size back: %w", err)
	}
	if err := s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, k8s.WithSizingOf(current, previous)); err != nil {
		return fmt.Errorf("put the previous size back: %w", err)
	}
	return nil
}

// revertTimeout bounds putting a cluster back after a change failed to record.
const revertTimeout = 30 * time.Second

// planDisk is the disk the cluster gets on the plan: its current one when that
// is within the plan, grown to the plan's start when below it. A disk above the
// plan's maximum is refused, as is growth the volumes cannot take.
func (s *ProvisioningService) planDisk(ctx context.Context, inst *domain.DatabaseInstance,
	cluster *unstructured.Unstructured, plan config.TierConfig) (string, error) {
	current, err := k8s.ClusterStorageSize(cluster)
	if err != nil {
		return "", err
	}
	start, err := resource.ParseQuantity(plan.StorageSize)
	if err != nil {
		return "", fmt.Errorf("plan disk %q: %w", plan.StorageSize, err)
	}
	limit, err := resource.ParseQuantity(plan.MaxStorageSize)
	if err != nil {
		return "", fmt.Errorf("plan maximum disk %q: %w", plan.MaxStorageSize, err)
	}
	if current.Cmp(limit) > 0 {
		return "", fmt.Errorf("%w: the plan allows up to %s and the disk is %s", ErrDiskAbovePlanMax, plan.MaxStorageSize, current.String())
	}
	if current.Cmp(start) >= 0 {
		return current.String(), nil
	}
	if err := s.k8sClient.ClusterVolumesExpandable(ctx, inst.Namespace, cluster.GetName()); err != nil {
		return "", err
	}
	return plan.StorageSize, nil
}

// databasesSize is what the project's databases take on disk, read on the
// primary; nil when it cannot be read, which is shown as unknown.
func (s *ProvisioningService) databasesSize(ctx context.Context, inst *domain.DatabaseInstance, cluster *unstructured.Unstructured) *int64 {
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if primary == "" {
		return nil
	}
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, primary, "postgres",
		[]string{"psql", "-U", "postgres", "-tAc", "SELECT sum(pg_database_size(datname)) FROM pg_database"})
	if err != nil {
		log.Printf("WARN: read database size for %s: %v", inst.ProjectID, err)
		return nil
	}
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return nil
	}
	return &size
}

// requireRoomForPlan refuses a plan unless enough untainted nodes have room
// for an instance of its size. A node already running one of the cluster's
// instances needs room only for the growth; its current requests are counted.
func (s *ProvisioningService) requireRoomForPlan(ctx context.Context, inst *domain.DatabaseInstance,
	cluster *unstructured.Unstructured, plan config.TierConfig) error {
	wantCPU, wantMemory, err := quantities(plan.CPU, plan.Memory)
	if err != nil {
		return fmt.Errorf("plan size: %w", err)
	}
	cpu, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "requests", "cpu")
	memory, _, _ := unstructured.NestedString(cluster.Object, "spec", "resources", "requests", "memory")
	hasCPU, hasMemory, err := quantities(cpu, memory)
	if err != nil {
		hasCPU, hasMemory = 0, 0
	}
	hosts, err := s.instanceNodes(ctx, inst.Namespace, cluster.GetName())
	if err != nil {
		return err
	}
	capacity, err := s.k8sClient.GetClusterCapacity(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNodePlacementUnknown, err)
	}
	roomy := 0
	for _, node := range capacity.Nodes {
		needCPU, needMemory := wantCPU, wantMemory
		if hosts[node.Name] {
			needCPU, needMemory = max(0, wantCPU-hasCPU), max(0, wantMemory-hasMemory)
		}
		fits := needCPU == 0 && needMemory == 0 && hosts[node.Name]
		if !node.Tainted && (fits || node.Fits(needCPU, needMemory, s.capacityHeadroomPercent)) {
			roomy++
		}
	}
	if roomy < plan.Instances {
		return fmt.Errorf("%w: %d of the %d instances would find a node", ErrPlanDoesNotFit, roomy, plan.Instances)
	}
	return nil
}

// instanceNodes names the nodes the cluster's instances run on now.
func (s *ProvisioningService) instanceNodes(ctx context.Context, namespace, clusterName string) (map[string]bool, error) {
	pods, err := s.k8sClient.GetPods(ctx, namespace, "cnpg.io/cluster="+clusterName+",cnpg.io/podRole=instance")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodePlacementUnknown, err)
	}
	nodes := map[string]bool{}
	for _, pod := range pods {
		if pod.Spec.NodeName != "" {
			nodes[pod.Spec.NodeName] = true
		}
	}
	return nodes, nil
}

// quantities reads a CPU and a memory quantity as millicores and bytes.
func quantities(cpu, memory string) (int64, int64, error) {
	cpuQuantity, err := resource.ParseQuantity(cpu)
	if err != nil {
		return 0, 0, fmt.Errorf("cpu %q: %w", cpu, err)
	}
	memoryQuantity, err := resource.ParseQuantity(memory)
	if err != nil {
		return 0, 0, fmt.Errorf("memory %q: %w", memory, err)
	}
	return cpuQuantity.MilliValue(), memoryQuantity.Value(), nil
}

// TuneParameters sets the project's tenant Postgres settings to exactly
// params, each within the bounds its plan allows. An empty set restores the
// defaults.
func (s *ProvisioningService) TuneParameters(ctx context.Context, projectID string, params map[string]string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationTune, requireActive)
	if err != nil {
		return err
	}
	defer release()
	if err := s.requireKubernetes(inst); err != nil {
		return err
	}
	recorder, ok := s.store.(storage.ProjectParametersStore)
	if !ok {
		return errors.New("the project store cannot record Postgres settings")
	}
	tier, err := s.tierConfig(ctx, inst.Tier)
	if err != nil {
		return err
	}
	if err := config.ValidateTenantParameters(params, tier); err != nil {
		return err
	}
	cluster, err := s.projectCluster(ctx, inst)
	if err != nil {
		return err
	}
	previous := k8s.TenantParameters(cluster)
	if err := s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, k8s.WithTenantParameters(cluster, params)); err != nil {
		return fmt.Errorf("apply the settings to the cluster: %w", err)
	}
	if err := recorder.UpdateParametersIfStatus(projectID, params, inst.Status); err != nil {
		return errors.Join(fmt.Errorf("record the settings: %w", err), s.restoreTenantParameters(ctx, inst, previous))
	}
	return nil
}

// restoreTenantParameters puts back the settings a tuning replaced when the
// tuning could not be recorded, so the record and the cluster agree.
func (s *ProvisioningService) restoreTenantParameters(ctx context.Context, inst *domain.DatabaseInstance, previous map[string]string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revertTimeout)
	defer cancel()
	cluster, err := s.projectCluster(ctx, inst)
	if err != nil {
		return fmt.Errorf("put the previous settings back: %w", err)
	}
	if err := s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, k8s.WithTenantParameters(cluster, previous)); err != nil {
		return fmt.Errorf("put the previous settings back: %w", err)
	}
	return nil
}

func (s *ProvisioningService) requireKubernetes(inst *domain.DatabaseInstance) error {
	if s.k8sClient == nil || (inst.DeploymentMode != "" && inst.DeploymentMode != domain.ModeK8s) {
		return ErrKubernetesOnly
	}
	return nil
}

func (s *ProvisioningService) projectCluster(ctx context.Context, inst *domain.DatabaseInstance) (*unstructured.Unstructured, error) {
	cluster, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, inst.ProjectID+postgresSuffix)
	if err != nil {
		return nil, fmt.Errorf(errGetCRDFmt, err)
	}
	return cluster.DeepCopy(), nil
}

package k8s

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The platform's storage budget (owner, 2026-09-29): every volume on the node,
// tenant and platform, is counted at what the storage layer reserves for it.

// LVMNodeGVR is OpenEBS LVM LocalPV's per-node report of its volume groups.
var LVMNodeGVR = schema.GroupVersionResource{Group: "local.openebs.io", Version: "v1alpha1", Resource: "lvmnodes"}

var (
	// ErrStorageNotSized refuses a tenant class whose volumes are not held to their size.
	ErrStorageNotSized = errors.New("the tenant storage class does not enforce each volume's size")
	// ErrStorageCapacityUnknown: the storage layer did not report the storage to budget.
	ErrStorageCapacityUnknown = errors.New("the storage layer does not report its capacity")
)

const cnpgClusterLabel = "cnpg.io/cluster"

// StorageAllocation is what the platform's volumes reserve, in bytes.
type StorageAllocation struct {
	// TenantBytes are the claims in project namespaces.
	TenantBytes int64 `json:"tenantBytes"`
	// PlatformBytes are every other claim: the platform database, NATS, provisioning.
	PlatformBytes int64 `json:"platformBytes"`
	// PendingBytes are what created clusters will still claim.
	PendingBytes int64 `json:"pendingBytes"`
}

// Total is everything allocated.
func (a StorageAllocation) Total() int64 { return a.TenantBytes + a.PlatformBytes + a.PendingBytes }

// RequireSizedStorageClass refuses a class that is missing, cannot grow, or
// does not come from a provisioner known to make volumes of exactly their size.
func (c *Client) RequireSizedStorageClass(ctx context.Context, name string, provisioners []string) error {
	class, err := c.clientset.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: StorageClass %q does not exist", ErrStorageNotSized, name)
	}
	if err != nil {
		return fmt.Errorf("read StorageClass %q: %w", name, err)
	}
	if !slices.Contains(provisioners, class.Provisioner) {
		return fmt.Errorf("%w: StorageClass %q is made by %q, not one of %v", ErrStorageNotSized, name, class.Provisioner, provisioners)
	}
	if class.AllowVolumeExpansion == nil || !*class.AllowVolumeExpansion {
		return fmt.Errorf("%w: StorageClass %q does not allow volume expansion", ErrStorageNotSized, name)
	}
	return nil
}

// LVMVolumeGroupBytes is the size of the volume group on every node together.
func (c *Client) LVMVolumeGroupBytes(ctx context.Context, namespace, volumeGroup string) (int64, error) {
	nodes, err := c.dynamicClient.Resource(LVMNodeGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("%w: list LVM nodes: %v", ErrStorageCapacityUnknown, err)
	}
	var total int64
	for _, node := range nodes.Items {
		groups, _, _ := unstructured.NestedSlice(node.Object, "volumeGroups")
		for _, raw := range groups {
			group, ok := raw.(map[string]interface{})
			if !ok || group["name"] != volumeGroup {
				continue
			}
			size, err := resource.ParseQuantity(fmt.Sprint(group["size"]))
			if err != nil {
				return 0, fmt.Errorf("%w: node %s reports %q for %s", ErrStorageCapacityUnknown, node.GetName(), group["size"], volumeGroup)
			}
			total += size.Value()
		}
	}
	if total <= 0 {
		return 0, fmt.Errorf("%w: no node reports the volume group %s", ErrStorageCapacityUnknown, volumeGroup)
	}
	return total, nil
}

// StorageAllocated counts every claim in the cluster, and what each project's
// clusters will still claim, so an allocation in flight is never free space.
func (c *Client) StorageAllocated(ctx context.Context) (StorageAllocation, error) {
	claims, err := c.clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return StorageAllocation{}, fmt.Errorf("list volume claims: %w", err)
	}
	projects, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: "excalibase.io/type=project"})
	if err != nil {
		return StorageAllocation{}, fmt.Errorf("list project namespaces: %w", err)
	}
	isProject := map[string]bool{}
	for _, ns := range projects.Items {
		isProject[ns.Name] = true
	}
	var allocation StorageAllocation
	claimed := map[string]int64{} // namespace/cluster -> bytes its claims hold
	for i := range claims.Items {
		pvc := &claims.Items[i]
		bytes := claimBytes(pvc)
		if isProject[pvc.Namespace] {
			allocation.TenantBytes += bytes
		} else {
			allocation.PlatformBytes += bytes
		}
		if cluster := pvc.Labels[cnpgClusterLabel]; cluster != "" {
			claimed[pvc.Namespace+"/"+cluster] += bytes
		}
	}
	for namespace := range isProject {
		pending, err := c.clusterBytesStillToClaim(ctx, namespace, claimed)
		if err != nil {
			return StorageAllocation{}, err
		}
		allocation.PendingBytes += pending
	}
	return allocation, nil
}

func (c *Client) clusterBytesStillToClaim(ctx context.Context, namespace string, claimed map[string]int64) (int64, error) {
	clusters, err := c.dynamicClient.Resource(CNPGClusterGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list database clusters in %s: %w", namespace, err)
	}
	var pending int64
	for _, cluster := range clusters.Items {
		bytes, err := ClusterStorageBytes(&cluster)
		if err != nil {
			return 0, err
		}
		if want := bytes - claimed[namespace+"/"+cluster.GetName()]; want > 0 {
			pending += want
		}
	}
	return pending, nil
}

// claimBytes is what a claim reserves: its capacity once bound, its request
// until then, whichever is larger (a grow in progress asks for more).
func claimBytes(pvc *corev1.PersistentVolumeClaim) int64 {
	bytes := pvc.Spec.Resources.Requests.Storage().Value()
	if capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok && capacity.Value() > bytes {
		bytes = capacity.Value()
	}
	return bytes
}

// StorageReserver admits an allocation of add bytes and runs it while holding
// the budget (storagebudget.Budget).
type StorageReserver interface {
	Reserve(ctx context.Context, add int64, what string, allocate func() error) error
}

// SetStorageReserver holds every cluster, app disk and growth this client
// makes to the platform's storage budget.
func (c *Client) SetStorageReserver(reserver StorageReserver) { c.storage = reserver }

// reserve runs allocate within the budget; without one it just runs it.
func (c *Client) reserve(ctx context.Context, add int64, what string, allocate func() error) error {
	if c.storage == nil {
		return allocate()
	}
	return c.storage.Reserve(ctx, add, what, allocate)
}

// applyClusterWithinBudget reserves what a database cluster adds: all of it
// when new, its growth when it exists.
func (c *Client) applyClusterWithinBudget(ctx context.Context, namespace string, obj *unstructured.Unstructured) error {
	want, err := ClusterStorageBytes(obj)
	if err != nil {
		return err
	}
	var have int64
	existing, err := c.dynamicClient.Resource(CNPGClusterGVR).Namespace(namespace).Get(ctx, obj.GetName(), metav1.GetOptions{})
	switch {
	case err == nil:
		if have, err = ClusterStorageBytes(existing); err != nil {
			return err
		}
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("read cluster %s/%s: %w", namespace, obj.GetName(), err)
	}
	what := fmt.Sprintf("the database cluster %s/%s", namespace, obj.GetName())
	return c.reserve(ctx, want-have, what, func() error { return c.applyCRD(ctx, CNPGClusterGVR, namespace, obj) })
}

// ClusterStorageBytes is what a database cluster's volumes reserve: instances times its disk.
func ClusterStorageBytes(cluster *unstructured.Unstructured) (int64, error) {
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if instances < 1 {
		instances = 1
	}
	size, _, _ := unstructured.NestedString(cluster.Object, "spec", "storage", "size")
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return 0, fmt.Errorf("cluster %s asks for an unreadable disk %q", cluster.GetName(), size)
	}
	return instances * quantity.Value(), nil
}

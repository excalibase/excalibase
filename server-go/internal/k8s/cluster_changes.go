package k8s

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"

	"github.com/excalibase/provisioning-poc/internal/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrVolumeExpansionUnsupported refuses a disk resize the cluster's storage
// cannot take. CloudNativePG cannot shrink a volume back, so a resize is only
// started once every claim is known to be growable.
var ErrVolumeExpansionUnsupported = errors.New("the database's storage cannot be expanded")

// tierPlatformParameters are the Postgres settings a tier imposes. They are
// the platform's, written after the tenant's, so a new cluster and a change of
// tier render them the same way.
func tierPlatformParameters(tier config.TierConfig) map[string]interface{} {
	params := tierQueryGuard(tier)
	// An unparseable storage size fails the cluster's own storage spec anyway.
	if walCap, err := tier.SlotWALKeepSize(); err == nil {
		params["max_slot_wal_keep_size"] = walCap
	}
	return params
}

// ClusterStorageSize is the volume size a CNPG Cluster asks for.
func ClusterStorageSize(cluster *unstructured.Unstructured) (resource.Quantity, error) {
	size, found, err := unstructured.NestedString(cluster.Object, "spec", "storage", "size")
	if err != nil || !found || size == "" {
		return resource.Quantity{}, fmt.Errorf("cluster %s names no storage size", cluster.GetName())
	}
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("cluster %s storage size %q: %w", cluster.GetName(), size, err)
	}
	return quantity, nil
}

// WithStorageSize is a copy of the cluster asking for a volume of size.
func WithStorageSize(cluster *unstructured.Unstructured, size string) *unstructured.Unstructured {
	changed := cluster.DeepCopy()
	_ = unstructured.SetNestedField(changed.Object, size, "spec", "storage", "size")
	return changed
}

// WithTier is a copy of the cluster sized, spread and guarded for tier, the
// way a new cluster of that tier is. Its storage class and tenant settings
// are kept. The caller has checked the tier's disk is not below the current.
func WithTier(cluster *unstructured.Unstructured, tier config.TierConfig) *unstructured.Unstructured {
	changed := cluster.DeepCopy()
	spec, _, _ := unstructured.NestedMap(changed.Object, "spec")
	if spec == nil {
		spec = map[string]interface{}{}
	}
	storageClass, _, _ := unstructured.NestedString(spec, "storage", "storageClass")
	delete(spec, "affinity")
	applyTierSizing(spec, tier, storageClass)
	params := postgresParameters(spec)
	maps.Copy(params, tierPlatformParameters(tier))
	setPostgresParameters(spec, params)
	_ = unstructured.SetNestedMap(changed.Object, spec, "spec")
	return changed
}

// TenantParameters are the tenant-tunable settings the cluster carries.
func TenantParameters(cluster *unstructured.Unstructured) map[string]string {
	params, _, _ := unstructured.NestedMap(cluster.Object, "spec", "postgresql", "parameters")
	tenant := map[string]string{}
	for name, value := range params {
		if text, ok := value.(string); ok && config.TenantTunableParameter(name) {
			tenant[name] = text
		}
	}
	return tenant
}

// WithTenantParameters is a copy of the cluster whose tenant-tunable settings
// are exactly params. Platform settings are untouched, and a name that is not
// the tenant's never reaches the cluster.
func WithTenantParameters(cluster *unstructured.Unstructured, params map[string]string) *unstructured.Unstructured {
	changed := cluster.DeepCopy()
	spec, _, _ := unstructured.NestedMap(changed.Object, "spec")
	if spec == nil {
		spec = map[string]interface{}{}
	}
	current := postgresParameters(spec)
	for name := range current {
		if config.TenantTunableParameter(name) {
			delete(current, name)
		}
	}
	for name, value := range params {
		if config.TenantTunableParameter(name) {
			current[name] = value
		}
	}
	setPostgresParameters(spec, current)
	_ = unstructured.SetNestedMap(changed.Object, spec, "spec")
	return changed
}

func postgresParameters(spec map[string]interface{}) map[string]interface{} {
	params, _, _ := unstructured.NestedMap(spec, "postgresql", "parameters")
	if params == nil {
		params = map[string]interface{}{}
	}
	return params
}

func setPostgresParameters(spec map[string]interface{}, params map[string]interface{}) {
	_ = unstructured.SetNestedMap(spec, params, "postgresql", "parameters")
}

// ClusterVolumesExpandable returns nil only when the cluster has volumes and
// every one of them is on a storage class that allows expansion.
func (c *Client) ClusterVolumesExpandable(ctx context.Context, namespace, clusterName string) error {
	claims, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx,
		metav1.ListOptions{LabelSelector: "cnpg.io/cluster=" + clusterName})
	if err != nil {
		return fmt.Errorf("list the database's volumes: %w", err)
	}
	if len(claims.Items) == 0 {
		return fmt.Errorf("%w: no volumes found for %s", ErrVolumeExpansionUnsupported, clusterName)
	}
	classes := map[string]bool{}
	for _, claim := range claims.Items {
		if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName == "" {
			return fmt.Errorf("%w: volume %s has no storage class", ErrVolumeExpansionUnsupported, claim.Name)
		}
		classes[*claim.Spec.StorageClassName] = true
	}
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := c.storageClassExpandable(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) storageClassExpandable(ctx context.Context, name string) error {
	class, err := c.clientset.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: storage class %q no longer exists", ErrVolumeExpansionUnsupported, name)
	}
	if err != nil {
		return fmt.Errorf("read storage class %q: %w", name, err)
	}
	if class.AllowVolumeExpansion == nil || !*class.AllowVolumeExpansion {
		return fmt.Errorf("%w: storage class %q does not allow volume expansion", ErrVolumeExpansionUnsupported, name)
	}
	return nil
}

// ValidateTierSizing refuses a tier that does not say how large a cluster is
// or what it may use.
func ValidateTierSizing(tier config.TierConfig) error { return validateTierSizing(tier) }

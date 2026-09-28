package k8s

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const (
	appDiskPrefix     = "app-disk-"
	appDiskVolumeName = "disk"
)

var (
	// ErrAppDiskNotExpandable refuses a grow the disk's storage class cannot take.
	ErrAppDiskNotExpandable = errors.New("the app's disk is on storage that cannot be expanded")
	// ErrAppDiskNotCreated: the disk is only on the record; the next deploy creates it at the recorded size.
	ErrAppDiskNotCreated = errors.New("the app's disk has not been created yet")
)

// AppDiskClaimName is keyed by the app id, so a rename keeps the same disk,
// and by the disk's generation: a disk moved onto a smaller volume lives in a
// new claim, since a claim cannot shrink.
func AppDiskClaimName(appID string, generation int) string {
	if generation == 0 {
		return appDiskPrefix + appID
	}
	return appDiskPrefix + appID + "-g" + strconv.Itoa(generation)
}

// appDiskLabels leave out the app's name: the claim outlives any one name.
func appDiskLabels(app *apphost.App) map[string]string {
	return map[string]string{
		"app.kubernetes.io/component":  appComponentLabel,
		"app.kubernetes.io/managed-by": appManagedByValue,
		"excalibase.io/app":            app.ID,
		"excalibase.io/project":        app.ProjectID,
	}
}

func buildAppDisk(namespace string, app *apphost.App, storageClass string) (*corev1.PersistentVolumeClaim, error) {
	if app.Disk == nil {
		return nil, nil
	}
	return buildAppDiskClaim(namespace, app, *app.Disk, storageClass)
}

func buildAppDiskClaim(namespace string, app *apphost.App, disk apphost.AppDisk, storageClass string) (*corev1.PersistentVolumeClaim, error) {
	size, err := resource.ParseQuantity(disk.Size)
	if err != nil {
		return nil, fmt.Errorf("%w: disk size %q: %w", ErrRenderApp, disk.Size, err)
	}
	name := AppDiskClaimName(app.ID, disk.Generation)
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return nil, fmt.Errorf("%w: app id %q cannot name a disk: %s", ErrRenderApp, app.ID, strings.Join(problems, "; "))
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: appDiskLabels(app)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}
	if storageClass != "" {
		claim.Spec.StorageClassName = &storageClass
	}
	return claim, nil
}

// mountAppDisk stops the old pod before the new one starts: a volume that
// admits one writer cannot serve two copies, so a redeploy is a short outage.
func mountAppDisk(deployment *appsv1.Deployment, claim *corev1.PersistentVolumeClaim, mountPath string) {
	deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	pod := &deployment.Spec.Template.Spec
	pod.Volumes = []corev1.Volume{{
		Name: appDiskVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name},
		},
	}}
	pod.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: appDiskVolumeName, MountPath: mountPath}}
}

// ensureAppDisk creates the claim once and never changes it: growing is
// GrowAppDisk's job and a volume cannot shrink.
func (c *Client) ensureAppDisk(ctx context.Context, namespace string, desired *corev1.PersistentVolumeClaim) error {
	return c.ensureAppDiskReplacing(ctx, namespace, desired, 0)
}

// ensureAppDiskReplacing reserves only what the new claim adds beyond
// replacedBytes, the volume it replaces.
func (c *Client) ensureAppDiskReplacing(ctx context.Context, namespace string, desired *corev1.PersistentVolumeClaim, replacedBytes int64) error {
	claims := c.clientset.CoreV1().PersistentVolumeClaims(namespace)
	_, err := claims.Get(ctx, desired.Name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("read app disk: %w", err)
	}
	size := desired.Spec.Resources.Requests[corev1.ResourceStorage]
	return c.reserve(ctx, size.Value()-replacedBytes, "the app disk "+desired.Name+" ("+size.String()+")", func() error {
		if _, err := claims.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create app disk: %w", err)
		}
		return nil
	})
}

// GrowAppDisk asks the app's claim for size, once its storage class is known
// to allow expansion; the caller has checked size is larger and within the plan.
func (c *Client) GrowAppDisk(ctx context.Context, namespace, appID string, generation int, size string) error {
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("disk size %q: %w", size, err)
	}
	claims := c.clientset.CoreV1().PersistentVolumeClaims(namespace)
	name := AppDiskClaimName(appID, generation)
	claim, err := claims.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ErrAppDiskNotCreated
	}
	if err != nil {
		return fmt.Errorf("read app disk: %w", err)
	}
	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName == "" {
		return fmt.Errorf("%w: the disk names no storage class", ErrAppDiskNotExpandable)
	}
	if err := c.storageClassExpandable(ctx, *claim.Spec.StorageClassName); err != nil {
		if errors.Is(err, ErrVolumeExpansionUnsupported) {
			return fmt.Errorf("%w: storage class %q does not allow volume expansion", ErrAppDiskNotExpandable, *claim.Spec.StorageClassName)
		}
		return err
	}
	held := claimBytes(claim)
	return c.reserve(ctx, quantity.Value()-held, "growing the app disk "+name+" to "+size, func() error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, err := claims.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("read app disk: %w", err)
			}
			current.Spec.Resources.Requests[corev1.ResourceStorage] = quantity
			if _, err := claims.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("grow app disk: %w", err)
			}
			return nil
		})
	})
}

// deleteAppDisks runs once the app's pods are gone, so nothing still writes to
// the disk, and waits until the claim is gone.
func (c *Client) deleteAppDisks(ctx context.Context, namespace, selector string, timeout time.Duration) error {
	claims := c.clientset.CoreV1().PersistentVolumeClaims(namespace)
	opts := metav1.ListOptions{LabelSelector: selector}
	list := func(ctx context.Context) ([]string, error) {
		found, err := claims.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list app disks: %w", err)
		}
		return namesOf(found.Items), nil
	}
	err := deleteOwned(ctx, []ownedKind{{"disk", list, func(ctx context.Context, name string) error {
		return claims.Delete(ctx, name, metav1.DeleteOptions{})
	}}})
	if err != nil {
		return err
	}
	return waitUntilNoneLeft(ctx, timeout, list)
}

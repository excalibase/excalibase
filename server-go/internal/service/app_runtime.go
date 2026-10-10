package service

import (
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// AppRuntime is what the app services ask of whatever runs apps (EXC-575):
// the Kubernetes client, or a single Docker or Podman host. "namespace" is the
// project's scope there, inst.Namespace; both take the same rendered workload.
type AppRuntime interface {
	ApplyAppWorkload(ctx context.Context, namespace string, workload *k8s.AppWorkload) error
	WaitForAppRollout(ctx context.Context, namespace, name, deployID string, timeout time.Duration) error
	AppAvailableReplicas(ctx context.Context, namespace, name string) (int32, error)
	PauseAppWorkload(ctx context.Context, namespace, appID string) error
	ResumeAppWorkload(ctx context.Context, namespace, appID, appName string, tier domain.TierType, timeout time.Duration) error
	PausedAppReplicas(ctx context.Context, namespace, appID, appName string) (int, error)
	WaitForAppPodsGone(ctx context.Context, namespace, appID string, timeout time.Duration) error
	DeleteAppWorkload(ctx context.Context, namespace, appID string, timeout time.Duration) error
	PruneAppWorkload(ctx context.Context, namespace, appID, keepName string, timeout time.Duration) error
	WithdrawProjectWorkloads(ctx context.Context, namespace string, opts k8s.WithdrawOptions) error
	RestartFunctionRuntime(ctx context.Context, namespace string) error
	RestoreAppRoute(ctx context.Context, namespace string, app *apphost.App, opts k8s.AppRouteOptions) error

	CreateAppDisk(ctx context.Context, namespace string, app *apphost.App, storageClass string, opts k8s.DiskJobOptions) error
	GrowAppDisk(ctx context.Context, namespace, appID string, generation int, size string) error
	AppDiskUsage(ctx context.Context, namespace, appID string, disk apphost.AppDisk, opts k8s.DiskJobOptions) (k8s.AppDiskUsage, error)
	CopyAppDisk(ctx context.Context, namespace string, app *apphost.App, to apphost.AppDisk, storageClass string, opts k8s.DiskJobOptions) error
	DeleteOtherAppDisks(ctx context.Context, namespace, appID string, keep int, timeout time.Duration) error
	RepointAppDisk(ctx context.Context, namespace, appID, claim string) error

	AppLogs(ctx context.Context, namespace, appID string, opts k8s.AppLogOptions) (k8s.AppLogPage, error)
	DeleteRegistryPullSecrets(ctx context.Context, namespace, registry string) error

	SyncAppDomains(ctx context.Context, namespace string, app *apphost.App, hosts []string, opts k8s.AppDomainOptions) error
	AppDomainCertificate(ctx context.Context, namespace, appName, host string) (k8s.CertificateState, error)
	AppHostCertificate(ctx context.Context, namespace, appName string) (k8s.CertificateState, error)
	AttachIssuedAppHostCertificates(ctx context.Context) error

	SetAppPrivateNetwork(ctx context.Context, namespace string, open bool) error
	AppPrivateNetworkOpen(ctx context.Context, namespace string) (bool, error)
	EnsureNamespaceQuota(ctx context.Context, namespace string, quota k8s.NamespaceQuota) error

	GetClusterCapacity(ctx context.Context) (k8s.ClusterCapacity, error)
	LiveAppPods(ctx context.Context, namespace, appID string) (k8s.AppPods, error)
	RuntimeClassPlacement(ctx context.Context, name string) (k8s.RuntimePlacement, error)
}

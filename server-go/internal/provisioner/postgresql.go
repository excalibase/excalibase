package provisioner

import (
	"context"
	"fmt"
	"log"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/natsauth"
)

const primaryPodSuffix = "-postgres-1"

const clusterNameSuffix = "-postgres"

// watcherReleaseName is the Helm release of the per-project CDC watcher.
const watcherReleaseName = "excalibase-watcher"

// CNPG declarative hibernation (operator >= 1.20): the annotation drives
// a clean shutdown that deletes the pods but keeps the PVCs; the
// condition is what the operator reports back while hibernated.
const (
	hibernationAnnotation = "cnpg.io/hibernation"
	hibernationCondition  = hibernationAnnotation
	hibernationOn         = "on"
	hibernationOff        = "off"

	defaultResumeTimeout = 5 * time.Minute
	defaultResumePoll    = 5 * time.Second

	// Teardown budget. CNPG finalizers plus PVC release routinely take a
	// couple of minutes on a busy node, so the wait is generous; it is
	// bounded so a wedged namespace surfaces as a failure, not a hang.
	defaultDeletionTimeout = 5 * time.Minute
	defaultDeletionPoll    = 2 * time.Second
	// cnpgPodSelector matches the pods CNPG runs for a cluster. Scoping the
	// pause wait to them leaves unrelated workloads in the namespace — the
	// tenant's Deno runtime, for instance — out of the question.
	cnpgPodSelector = "cnpg.io/podRole=instance"
)

// PostgreSQLProvisioner provisions PostgreSQL via CloudNativePG operator.
type PostgreSQLProvisioner struct {
	client           k8s.KubeClient
	watcherChartPath string
	resumeTimeout    time.Duration
	resumePoll       time.Duration
	deletionPoller   Poller
	// pausePoller bounds the wait for the hibernated cluster's pods to go
	// away. CNPG shuts postgres down cleanly, so this is a wait on a real
	// shutdown, not on an API call.
	pausePoller Poller
}

func NewPostgreSQLProvisioner(client k8s.KubeClient, watcherChartPath string) *PostgreSQLProvisioner {
	return &PostgreSQLProvisioner{
		client:           client,
		watcherChartPath: watcherChartPath,
		resumeTimeout:    defaultResumeTimeout,
		resumePoll:       defaultResumePoll,
		deletionPoller:   NewPoller(defaultDeletionPoll, defaultDeletionTimeout),
		pausePoller:      NewPoller(defaultDeletionPoll, defaultDeletionTimeout),
	}
}

// SetDeletionPoller overrides how long teardown waits for the cluster and
// the namespace to actually disappear.
func (p *PostgreSQLProvisioner) SetDeletionPoller(poller Poller) {
	p.deletionPoller = poller
}

// SetPausePoller overrides how long a pause waits for the hibernated
// cluster's pods to stop running.
func (p *PostgreSQLProvisioner) SetPausePoller(poller Poller) {
	p.pausePoller = poller
}

// StopReplication removes the tenant watcher. Its logical-replication
// session keeps a connection open on the primary, and CNPG's smart shutdown
// waits for open connections to close before it stops postgres — so a
// hibernation requested with the watcher still streaming takes minutes to
// take effect (EXC-363). The watcher is reinstalled on resume, by the
// control plane, which holds its credentials.
func (p *PostgreSQLProvisioner) StopReplication(ctx context.Context, namespace, _ string) error {
	if err := p.client.UninstallHelmChart(ctx, namespace, watcherReleaseName); err != nil {
		return fmt.Errorf("stop tenant watcher: %w", err)
	}
	return nil
}

func (p *PostgreSQLProvisioner) SupportedType() domain.DatabaseType {
	return domain.PostgreSQL
}

func (p *PostgreSQLProvisioner) provisionNamespace(ctx context.Context, req domain.ProvisioningRequest, namespace string) error {
	labels := map[string]string{
		"excalibase.io/type": "project",
		"excalibase.io/org":  req.OrgID,
	}
	if err := p.client.CreateNamespaceWithLabels(ctx, namespace, labels); err != nil {
		return fmt.Errorf("create namespace: %w", err)
	}
	if req.Backup != nil && req.Backup.Enabled && req.Backup.S3 != nil {
		if err := p.client.CreateSecret(ctx, namespace, "backup-s3-creds", map[string][]byte{
			"ACCESS_KEY_ID":     []byte(req.Backup.S3.AccessKeyID),
			"ACCESS_SECRET_KEY": []byte(req.Backup.S3.SecretAccessKey),
		}); err != nil {
			return fmt.Errorf("create backup secret: %w", err)
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) provisionCRD(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, projectID, namespace string) error {
	opts := k8s.PostgreSQLClusterOpts{
		ProjectID:       projectID,
		Namespace:       namespace,
		Tier:            tier,
		StorageClass:    req.StorageClass,
		PostgresVersion: req.PostgresVersion,
		DatabaseName:    req.DatabaseName,
		MasterUsername:  req.MasterUsername,
		Parameters:      req.Parameters,
		Tags:            req.Tags,
	}
	if req.Backup != nil && req.Backup.Enabled {
		opts.Backup = &k8s.BackupOpts{
			Schedule:      req.Backup.Schedule,
			RetentionDays: req.Backup.Retention,
		}
		if req.Backup.S3 != nil {
			opts.Backup.EndpointURL = req.Backup.S3.Endpoint
			opts.Backup.Bucket = req.Backup.S3.Bucket
		}
	}
	cluster := k8s.BuildPostgreSQLCluster(opts)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return fmt.Errorf("deploy cluster CRD: %w", err)
	}
	return nil
}

func (p *PostgreSQLProvisioner) waitForAllPods(ctx context.Context, tier config.TierConfig, projectID, namespace string) error {
	primaryPod := projectID + primaryPodSuffix
	if err := p.waitForPodReady(ctx, namespace, primaryPod, 5*time.Minute); err != nil {
		return fmt.Errorf("waiting for ready: %w", err)
	}
	for i := 2; i <= tier.Instances; i++ {
		pod := fmt.Sprintf("%s-postgres-%d", projectID, i)
		if err := p.waitForPodReady(ctx, namespace, pod, 3*time.Minute); err != nil {
			return fmt.Errorf("waiting for replica %d: %w", i, err)
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) provisionScheduledBackup(ctx context.Context, req domain.ProvisioningRequest, projectID, namespace string) error {
	schedule := req.Backup.Schedule
	if schedule == "" {
		schedule = "0 0 * * *"
	}
	backup := k8s.BuildScheduledBackup(projectID, namespace, schedule)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, namespace, backup); err != nil {
		return fmt.Errorf("configure backup: %w", err)
	}
	return nil
}

func (p *PostgreSQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	projectID := req.ProjectName
	namespace := fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)

	cb(domain.StageValidating)

	// Stage 2: Namespace + optional backup secret
	cb(domain.StageNamespaceCreation)
	if err := p.provisionNamespace(ctx, req, namespace); err != nil {
		return nil, err
	}

	// Stage 3: CRD
	cb(domain.StageCRDDeployment)
	if err := p.provisionCRD(ctx, req, tier, projectID, namespace); err != nil {
		return nil, err
	}

	// Stage 4: Wait for pods
	cb(domain.StageWaitingForReady)
	if err := p.waitForAllPods(ctx, tier, projectID, namespace); err != nil {
		return nil, err
	}

	// Stage 5: Credentials
	cb(domain.StageCredentialGeneration)
	creds, err := p.extractCredentials(ctx, namespace, projectID+"-postgres-app", projectID)
	if err != nil {
		return nil, fmt.Errorf("extract credentials: %w", err)
	}

	// Stage 6: Backup
	if req.Backup != nil && req.Backup.Enabled {
		cb(domain.StageBackupConfiguration)
		if err := p.provisionScheduledBackup(ctx, req, projectID, namespace); err != nil {
			return nil, err
		}
	}

	cb(domain.StageMetricsSetup)
	// Stage 8: Watcher deployment deferred to service layer (after cdc_watcher role).
	cb(domain.StageWatcherDeployment)
	cb(domain.StageCompleted)

	creds.Namespace = namespace
	return creds, nil
}

// ProvisionWithRollback mirrors Provision but uses ProvisionContext for stage/step
// tracking and registers compensation actions so the service layer can roll back
// on failure. Namespace delete is the single atomic cleanup that covers stages 2-8
// (CNPG cluster CRD, pods, PVCs, backup secret, scheduled backup, helm watcher are
// all inside the namespace).
func (p *PostgreSQLProvisioner) ProvisionWithRollback(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, pc *ProvisionContext) (*ProvisioningResult, error) {
	projectID := req.ProjectName
	namespace := fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)

	// Stage 1: Validate
	pc.SetStage(domain.StageValidating)

	// Stage 2: Namespace + backup secret
	if err := p.stageNamespace(ctx, req, namespace, pc); err != nil {
		return nil, err
	}

	// Stage 3: CRD (inside namespace — covered by namespace cleanup)
	if err := p.stageCRD(ctx, req, tier, projectID, namespace, pc); err != nil {
		return nil, err
	}

	// Stage 4: Wait for pods
	if err := p.stageWaitForPods(ctx, tier, projectID, namespace, pc); err != nil {
		return nil, err
	}

	// Stage 5: Credentials
	pc.SetStage(domain.StageCredentialGeneration)
	pc.SetStep("read postgres-app secret")
	secretName := projectID + "-postgres-app"
	creds, err := p.extractCredentials(ctx, namespace, secretName, projectID)
	if err != nil {
		return nil, pc.Fail(fmt.Errorf("extract credentials: %w", err))
	}

	// Stage 6: Backup
	if req.Backup != nil && req.Backup.Enabled {
		if err := p.stageBackup(ctx, req, projectID, namespace, pc); err != nil {
			return nil, err
		}
	}

	// Stage 7: Metrics (no-op)
	pc.SetStage(domain.StageMetricsSetup)

	// Stage 8: Watcher deployment is deferred to the service layer (see
	// PostgreSQLProvisioner.DeployWatcher). The cdc_watcher role does not
	// exist until createProjectRoles runs in finalizeProvisioning, so
	// deploying here races the role SQL and crashloops the watcher.
	pc.SetStage(domain.StageWatcherDeployment)

	// Stage 9: Done
	pc.SetStage(domain.StageCompleted)
	creds.Namespace = namespace
	return creds, nil
}

func (p *PostgreSQLProvisioner) stageNamespace(ctx context.Context, req domain.ProvisioningRequest, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageNamespaceCreation)
	pc.SetStep("create namespace")
	labels := map[string]string{
		"excalibase.io/type": "project",
		"excalibase.io/org":  req.OrgID,
	}
	if err := p.client.CreateNamespaceWithLabels(ctx, namespace, labels); err != nil {
		return pc.Fail(fmt.Errorf("create namespace: %w", err))
	}
	pc.RegisterCleanup("delete namespace "+namespace, func(ctx context.Context) error {
		return p.client.DeleteNamespace(ctx, namespace)
	})
	if req.Backup != nil && req.Backup.Enabled && req.Backup.S3 != nil {
		pc.SetStep("create backup secret")
		if err := p.client.CreateSecret(ctx, namespace, "backup-s3-creds", map[string][]byte{
			"ACCESS_KEY_ID":     []byte(req.Backup.S3.AccessKeyID),
			"ACCESS_SECRET_KEY": []byte(req.Backup.S3.SecretAccessKey),
		}); err != nil {
			return pc.Fail(fmt.Errorf("create backup secret: %w", err))
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) stageCRD(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, projectID, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageCRDDeployment)
	pc.SetStep("apply CNPG cluster")
	opts := k8s.PostgreSQLClusterOpts{
		ProjectID:       projectID,
		Namespace:       namespace,
		Tier:            tier,
		StorageClass:    req.StorageClass,
		PostgresVersion: req.PostgresVersion,
		DatabaseName:    req.DatabaseName,
		MasterUsername:  req.MasterUsername,
		Parameters:      req.Parameters,
		Tags:            req.Tags,
	}
	if req.Backup != nil && req.Backup.Enabled {
		opts.Backup = &k8s.BackupOpts{
			Schedule:      req.Backup.Schedule,
			RetentionDays: req.Backup.Retention,
		}
		if req.Backup.S3 != nil {
			opts.Backup.EndpointURL = req.Backup.S3.Endpoint
			opts.Backup.Bucket = req.Backup.S3.Bucket
		}
	}
	cluster := k8s.BuildPostgreSQLCluster(opts)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return pc.Fail(fmt.Errorf("deploy cluster CRD: %w", err))
	}
	return nil
}

func (p *PostgreSQLProvisioner) stageWaitForPods(ctx context.Context, tier config.TierConfig, projectID, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageWaitingForReady)
	pc.SetStep("primary pod")
	primaryPod := projectID + primaryPodSuffix
	if err := p.waitForPodReady(ctx, namespace, primaryPod, 5*time.Minute); err != nil {
		return pc.Fail(fmt.Errorf("primary pod: %w", err))
	}
	for i := 2; i <= tier.Instances; i++ {
		pc.SetStep(fmt.Sprintf("replica %d", i))
		pod := fmt.Sprintf("%s-postgres-%d", projectID, i)
		if err := p.waitForPodReady(ctx, namespace, pod, 3*time.Minute); err != nil {
			return pc.Fail(fmt.Errorf("replica %d: %w", i, err))
		}
	}
	return nil
}

func (p *PostgreSQLProvisioner) stageBackup(ctx context.Context, req domain.ProvisioningRequest, projectID, namespace string, pc *ProvisionContext) error {
	pc.SetStage(domain.StageBackupConfiguration)
	pc.SetStep("apply scheduled backup")
	schedule := req.Backup.Schedule
	if schedule == "" {
		schedule = "0 0 * * *"
	}
	backup := k8s.BuildScheduledBackup(projectID, namespace, schedule)
	if err := p.client.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, namespace, backup); err != nil {
		return pc.Fail(fmt.Errorf("configure backup: %w", err))
	}
	return nil
}

// Pause hibernates the project's CNPG cluster declaratively
// (cnpg.io/hibernation: "on"). The operator performs a clean shutdown,
// deletes the pods and keeps the PVCs, so spec.instances (and the
// excalibase.io/tier-instances annotation) stay untouched and the
// cluster comes back at its tier size on Resume.
// Pause requests hibernation and then waits until the namespace holds no
// running database pod. Setting the annotation only asks the operator to
// shut down; a project recorded PAUSED while its pods are still Terminating
// is still consuming the CPU capacity admission plans against.
func (p *PostgreSQLProvisioner) Pause(ctx context.Context, namespace, projectID string) error {
	if err := p.setHibernation(ctx, namespace, projectID, hibernationOn); err != nil {
		return err
	}
	return p.pausePoller.WaitUntilClear(ctx, "database pods in "+namespace,
		func(ctx context.Context) ([]string, error) { return p.runningDatabasePods(ctx, namespace) })
}

// WorkloadStopped reports whether the namespace still runs a database pod.
// A Terminating pod counts as running: it still holds its resource requests
// and the database may still be shutting down.
func (p *PostgreSQLProvisioner) WorkloadStopped(ctx context.Context, namespace, _ string) (bool, error) {
	pods, err := p.runningDatabasePods(ctx, namespace)
	if err != nil {
		return false, err
	}
	return len(pods) == 0, nil
}

// runningDatabasePods lists the database pods the namespace still carries.
// A pod in Terminating is returned like any other: it exists, so it still
// holds its resource requests.
func (p *PostgreSQLProvisioner) runningDatabasePods(ctx context.Context, namespace string) ([]string, error) {
	pods, err := p.client.GetPods(ctx, namespace, cnpgPodSelector)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names, nil
}

// Resume clears the hibernation annotation and blocks until the
// operator reports a ready primary, bounded by resumeTimeout. A
// timeout returns an error but leaves the annotation off: the
// operator keeps recovering the cluster while the project stays in
// RESUMING for the operator to inspect.
func (p *PostgreSQLProvisioner) Resume(ctx context.Context, namespace, projectID string) error {
	if err := p.setHibernation(ctx, namespace, projectID, hibernationOff); err != nil {
		return err
	}
	return p.waitForClusterReady(ctx, namespace, projectID+clusterNameSuffix)
}

// setHibernation flips the hibernation annotation, re-reading the Cluster and
// trying again when the API server refuses the write on a stale
// resourceVersion. The operator writes the same object throughout a pause and
// a resume, so losing that race is ordinary rather than exceptional — a
// conflict says nothing about the cluster, only that our copy was old. A
// conflict that never clears is still a failure: the annotation was not set,
// so neither a pause nor a resume may be reported as started.
func (p *PostgreSQLProvisioner) setHibernation(ctx context.Context, namespace, projectID, value string) error {
	clusterName := projectID + clusterNameSuffix
	var err error
	for attempt := 0; attempt < hibernationFlipAttempts; attempt++ {
		if err = p.flipHibernation(ctx, namespace, clusterName, value); err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}

// hibernationFlipAttempts bounds the read-modify-write retries. Each attempt
// reads the object afresh, so the only way to keep conflicting is an operator
// writing faster than we can submit; a handful of tries separates that from
// the ordinary single lost race.
const hibernationFlipAttempts = 5

func (p *PostgreSQLProvisioner) flipHibernation(ctx context.Context, namespace, clusterName, value string) error {
	cluster, err := p.client.GetCRD(ctx, k8s.CNPGClusterGVR, namespace, clusterName)
	if err != nil {
		return fmt.Errorf("get cluster for hibernation=%s: %w", value, err)
	}
	annotations := cluster.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[hibernationAnnotation] = value
	cluster.SetAnnotations(annotations)
	if err := p.client.UpdateCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return fmt.Errorf("update cluster hibernation=%s: %w", value, err)
	}
	return nil
}

// waitForClusterReady polls the Cluster status until the primary is
// ready. CNPG leaves status.phase at "Cluster in healthy state" while
// hibernated, so the phase is not a usable signal: readiness is
// status.readyInstances >= 1 with the hibernation condition cleared.
func (p *PostgreSQLProvisioner) waitForClusterReady(ctx context.Context, namespace, clusterName string) error {
	deadline := time.Now().Add(p.resumeTimeout)
	for time.Now().Before(deadline) {
		cluster, err := p.client.GetCRD(ctx, k8s.CNPGClusterGVR, namespace, clusterName)
		if err == nil && clusterPrimaryReady(cluster) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.resumePoll):
		}
	}
	return fmt.Errorf("cluster %s did not become ready within %v after resume", clusterName, p.resumeTimeout)
}

func clusterPrimaryReady(cluster *unstructured.Unstructured) bool {
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	return ready >= 1 && !clusterHibernated(cluster)
}

func clusterHibernated(cluster *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if condition["type"] == hibernationCondition && condition["status"] == "True" {
			return true
		}
	}
	return false
}

// Deprovision tears the project's Kubernetes resources down and returns only
// once their removal is observed. Order matters: the CNPG Cluster goes first
// and is waited out, because deleting the namespace under a live Cluster
// leaves the operator's finalizers wedged on objects that are already gone.
// Every step reports its own error — a submitted delete request is not proof
// that anything was removed.
func (p *PostgreSQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	// Stop the tenant watcher first so replication ends cleanly and no
	// surviving pod keeps publishing while the database is torn down.
	if err := p.client.UninstallHelmChart(ctx, namespace, watcherReleaseName); err != nil {
		return fmt.Errorf("stop tenant watcher: %w", err)
	}
	if err := p.deleteClusterAndWait(ctx, namespace, projectID+clusterNameSuffix); err != nil {
		return err
	}
	return p.deleteNamespaceAndWait(ctx, namespace)
}

func (p *PostgreSQLProvisioner) deleteClusterAndWait(ctx context.Context, namespace, cluster string) error {
	if err := p.client.DeleteCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return fmt.Errorf("delete database cluster %s: %w", cluster, err)
	}
	return p.deletionPoller.WaitUntilClear(ctx, "database cluster "+cluster,
		func(ctx context.Context) ([]string, error) {
			exists, err := p.client.CRDExists(ctx, k8s.CNPGClusterGVR, namespace, cluster)
			if err != nil || !exists {
				return nil, err
			}
			return []string{cluster}, nil
		})
}

func (p *PostgreSQLProvisioner) deleteNamespaceAndWait(ctx context.Context, namespace string) error {
	if err := p.client.DeleteNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("delete namespace %s: %w", namespace, err)
	}
	return p.deletionPoller.WaitUntilClear(ctx, "namespace "+namespace,
		func(ctx context.Context) ([]string, error) { return p.namespaceRemnants(ctx, namespace) })
}

// namespaceRemnants lists what the namespace still carries. Pods and PVCs are
// checked alongside the namespace object itself: a Terminating pod still
// holds its CPU request, which is the number capacity admission plans new
// projects against, and a surviving claim still holds its volume.
func (p *PostgreSQLProvisioner) namespaceRemnants(ctx context.Context, namespace string) ([]string, error) {
	var left []string
	exists, err := p.client.NamespaceExists(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if exists {
		left = append(left, "namespace/"+namespace)
	}
	pods, err := p.client.GetPods(ctx, namespace, "")
	if err != nil {
		return nil, err
	}
	for _, pod := range pods {
		left = append(left, "pod/"+pod.Name)
	}
	pvcs, err := p.client.ListPVCs(ctx, namespace)
	if err != nil {
		return nil, err
	}
	for _, pvc := range pvcs {
		left = append(left, "pvc/"+pvc)
	}
	return left, nil
}

func (p *PostgreSQLProvisioner) GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error) {
	ready, err := p.client.IsPodReady(ctx, namespace, projectID+primaryPodSuffix)
	if err != nil {
		return &ProvisioningStatus{Phase: "Unknown", Message: err.Error()}, nil
	}
	if ready {
		return &ProvisioningStatus{Phase: "Running", Ready: true}, nil
	}
	return &ProvisioningStatus{Phase: "Pending", Ready: false}, nil
}

func (p *PostgreSQLProvisioner) ConfigureBackup(ctx context.Context, namespace, projectID, schedule string, retention int) error {
	backup := k8s.BuildScheduledBackup(projectID, namespace, schedule)
	return p.client.ApplyCRD(ctx, k8s.CNPGScheduledBackupGVR, namespace, backup)
}

// WatcherSpec carries everything the per-project CDC watcher needs: its
// replication role on the tenant database and its own NATS bus identity.
type WatcherSpec struct {
	Namespace string
	ProjectID string
	DBName    string
	// Username/Password are the cdc_watcher Postgres role.
	Username string
	Password string
	// NatsUser/NatsPassword are the project-scoped bus credential minted by
	// the control plane (EXC-324). Blank leaves the watcher unauthenticated,
	// which only works on a NATS server without auth_callout.
	NatsUser     string
	NatsPassword string
}

// DeployWatcher installs the per-project CDC watcher Helm chart with inline
// cdc_watcher credentials. Must be called AFTER the role exists (after
// createProjectRoles). Soft-fails: logs WARN if the chart install errors so
// provisioning still completes.
func (p *PostgreSQLProvisioner) DeployWatcher(ctx context.Context, spec WatcherSpec) error {
	if p.watcherChartPath == "" {
		return nil
	}
	namespace, projectID, dbName := spec.Namespace, spec.ProjectID, spec.DBName
	username, password := spec.Username, spec.Password
	values := map[string]interface{}{
		"postgres": map[string]interface{}{
			"enabled":           true,
			"url":               fmt.Sprintf("postgres://%s-postgres-rw.%s.svc.cluster.local:5432/%s?replication=database", projectID, namespace, dbName),
			"username":          username,
			"password":          password,
			"slotName":          "cdc_watcher",
			"publicationName":   "cdc_watcher_pub",
			"createSlot":        true,
			"createPublication": false,
			"captureDdl":        false,
		},
		"nats": map[string]interface{}{
			"url":           "nats://nats.excalibase-platform.svc.cluster.local:4222",
			"streamName":    "CDC",
			"subjectPrefix": fmt.Sprintf("cdc.%s", projectID),
			"enabled":       true,
			// Project-scoped bus credential. The chart puts these in a
			// Secret in the tenant namespace; they never appear in the
			// rendered Deployment manifest.
			"username": spec.NatsUser,
			"password": spec.NatsPassword,
			// Replies to this watcher's JetStream publishes land under a
			// prefix only it may subscribe to (natsauth.PermissionsFor).
			"inboxPrefix": natsauth.InboxPrefixFor(spec.NatsUser),
		},
		"resources": map[string]interface{}{
			"limits":   map[string]interface{}{"cpu": "200m", "memory": "256Mi"},
			"requests": map[string]interface{}{"cpu": "50m", "memory": "128Mi"},
		},
		"image": map[string]interface{}{
			"repository": "excalibase/excalibase-watcher-go",
			"tag":        "latest",
			"pullPolicy": "IfNotPresent",
		},
	}
	if err := p.client.InstallHelmChart(ctx, namespace, watcherReleaseName, p.watcherChartPath, values); err != nil {
		log.Printf("WARN: watcher deployment failed for %s: %v", projectID, err)
		return err
	}
	return nil
}

func (p *PostgreSQLProvisioner) waitForPodReady(ctx context.Context, namespace, pod string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := p.client.IsPodReady(ctx, namespace, pod)
		if err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("pod %s did not become ready within %v", pod, timeout)
}

func (p *PostgreSQLProvisioner) extractCredentials(ctx context.Context, namespace, secretName, projectID string) (*ProvisioningResult, error) {
	// Poll for secret with retries, respecting context cancellation.
	var secretData map[string][]byte
	var err error
	for i := 0; i < 30; i++ {
		secretData, err = p.client.GetSecret(ctx, namespace, secretName)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("secret %s not found (context cancelled): %w", secretName, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("secret %s not found: %w", secretName, err)
	}

	dbName := string(secretData["dbname"])
	if dbName == "" {
		dbName = "app"
	}

	return &ProvisioningResult{
		Host:         fmt.Sprintf("%s-postgres-rw.%s.svc.cluster.local", projectID, namespace),
		ReadOnlyHost: fmt.Sprintf("%s-postgres-r.%s.svc.cluster.local", projectID, namespace),
		Port:         5432,
		DatabaseName: dbName,
		Username:     string(secretData["username"]),
		Password:     string(secretData["password"]),
		SSLMode:      "require",
	}, nil
}

package provisioner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// MySQL projects run on the MOCO operator (ADR 0041): one MySQLCluster per
// project in its namespace. Every lifecycle step waits on observed state —
// the instance pods and a real query on the primary — never on the CR alone.

// MOCOMySQLContainer is the mysqld container of a MOCO pod, and
// MOCOAdminDefaultsFile MOCO's own admin login file inside it.
const (
	MOCOMySQLContainer    = "mysqld"
	MOCOAdminDefaultsFile = "/mysql-credentials/moco-admin-my.cnf"
)

// ErrMySQLUnsupported refuses what a MySQL project does not offer (yet).
var ErrMySQLUnsupported = errors.New("not available for MySQL projects")

const (
	mysqlPort            = 3306
	mysqlDefaultDatabase = "app"
	// mysqlAdminCnf is MOCO's own admin login file inside the mysqld
	// container; the platform runs its setup SQL through it over the socket.
	mysqlAdminCnf      = MOCOAdminDefaultsFile
	mysqlContainer     = MOCOMySQLContainer
	mysqlBackupJobName = "moco-backup-"

	defaultMySQLReadyTimeout = 10 * time.Minute
	defaultMySQLPoll         = 5 * time.Second
)

// mysqlDatabaseName is a plain identifier, so it is safe to quote into SQL.
var mysqlDatabaseName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)

// MySQLProvisioner provisions MySQL projects through MOCO.
type MySQLProvisioner struct {
	client k8s.KubeClient
	// readyPoller bounds a wait for the cluster to serve; stopPoller a wait
	// for pods or the namespace to go.
	readyPoller Poller
	stopPoller  Poller
	// backupPathStyle addresses the backup store path-style.
	backupPathStyle bool
	// publicDomainSuffix is the suffix public endpoint names hang off; empty
	// when the platform offers no public endpoints.
	publicDomainSuffix string
}

func NewMySQLProvisioner(client k8s.KubeClient) *MySQLProvisioner {
	return &MySQLProvisioner{
		client:      client,
		readyPoller: NewPoller(defaultMySQLPoll, defaultMySQLReadyTimeout),
		stopPoller:  NewPoller(defaultDeletionPoll, defaultDeletionTimeout),
	}
}

// SetPollers overrides how long the provisioner waits for a cluster to serve
// and for its pods or namespace to go.
func (p *MySQLProvisioner) SetPollers(ready, stop Poller) {
	p.readyPoller, p.stopPoller = ready, stop
}

func (p *MySQLProvisioner) SupportedType() domain.DatabaseType { return domain.MySQL }

func mysqlNamespace(req domain.ProvisioningRequest) string {
	return fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)
}

// validateMySQLRequest refuses, before anything exists, what MySQL projects
// do not offer and a database name that is not a plain identifier.
func validateMySQLRequest(req domain.ProvisioningRequest) (database, image string, err error) {
	if err := validateMySQLBackup(req.Backup); err != nil {
		return "", "", err
	}
	if req.DocumentDB {
		return "", "", fmt.Errorf("DocumentDB is %w", ErrMySQLUnsupported)
	}
	version := req.MySQLVersion
	if version == "" {
		version = config.DefaultMySQLVersion()
	}
	if image, err = config.MySQLImage(version); err != nil {
		return "", "", err
	}
	database = req.DatabaseName
	if database == "" {
		database = mysqlDefaultDatabase
	}
	if !mysqlDatabaseName.MatchString(database) {
		return "", "", fmt.Errorf("database name %q must be lowercase letters, digits and underscores", database)
	}
	return database, image, nil
}

// Provision builds the project without stage rollback.
func (p *MySQLProvisioner) Provision(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	pc := NewProvisionContext(cb, func(string) {
		// No step reporting outside a rollback-tracked provision.
	})
	return p.ProvisionWithRollback(ctx, req, tier, pc)
}

// ProvisionWithRollback builds the namespace, settings and MySQLCluster, waits
// for it to serve and creates the database; deleting the namespace undoes it.
func (p *MySQLProvisioner) ProvisionWithRollback(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, pc *ProvisionContext) (*ProvisioningResult, error) {
	namespace := mysqlNamespace(req)
	projectID := req.ProjectName

	pc.SetStage(domain.StageValidating)
	database, image, err := validateMySQLRequest(req)
	if err != nil {
		return nil, pc.Fail(err)
	}
	pc.SetStage(domain.StageNamespaceCreation)
	if err := p.ensureNamespace(ctx, req, namespace, pc); err != nil {
		return nil, err
	}
	if req.Backup != nil && req.Backup.Enabled {
		pc.SetStep("write the backup credentials")
		if err := p.WriteBackupAccess(ctx, namespace, k8s.MySQLBackupAccessName, req.Backup.S3); err != nil {
			return nil, pc.Fail(err)
		}
	}
	pc.SetStage(domain.StageCRDDeployment)
	opts := k8s.MySQLClusterOpts{ProjectID: projectID, Namespace: namespace, Tier: tier, StorageClass: req.StorageClass, Image: image}
	if err := p.applyCluster(ctx, opts, pc); err != nil {
		return nil, pc.Fail(err)
	}
	pc.SetStage(domain.StageWaitingForReady)
	pc.SetStep("wait for every instance and a query")
	if err := p.waitServing(ctx, namespace, projectID, tier.Instances); err != nil {
		return nil, pc.Fail(err)
	}
	result, err := p.createDatabase(ctx, namespace, projectID, database, pc)
	if err != nil {
		return nil, err
	}
	if err := p.attachProvisionedBackups(ctx, req, opts, pc); err != nil {
		return nil, pc.Fail(err)
	}
	return result, nil
}

// createDatabase creates the project's database on the serving cluster and
// answers where it is reached.
func (p *MySQLProvisioner) createDatabase(ctx context.Context, namespace, projectID, database string, pc *ProvisionContext) (*ProvisioningResult, error) {
	pc.SetStage(domain.StageCredentialGeneration)
	pc.SetStep("create database " + database)
	if _, err := p.query(ctx, namespace, projectID, "CREATE DATABASE IF NOT EXISTS `"+database+"`"); err != nil {
		return nil, pc.Fail(fmt.Errorf("create database %s: %w", database, err))
	}
	pc.SetStage(domain.StageCompleted)
	return &ProvisioningResult{
		Host:         k8s.MySQLPrimaryHost(projectID, namespace),
		ReadOnlyHost: k8s.MySQLReplicaHost(projectID, namespace),
		Port:         mysqlPort,
		DatabaseName: database,
		Username:     MySQLUserAdmin,
		SSLMode:      domain.MySQLSSLMode,
		Namespace:    namespace,
	}, nil
}

// ensureNamespace creates the project's namespace, or for a database added
// to an existing project checks it is there and never deletes it.
func (p *MySQLProvisioner) ensureNamespace(ctx context.Context, req domain.ProvisioningRequest, namespace string, pc *ProvisionContext) error {
	if req.IntoExistingNamespace {
		pc.SetStep("find namespace")
		exists, err := p.client.NamespaceExists(ctx, namespace)
		if err != nil {
			return pc.Fail(fmt.Errorf("find namespace: %w", err))
		}
		if !exists {
			return pc.Fail(fmt.Errorf("the project's namespace %s does not exist", namespace))
		}
		pc.RegisterCleanup("remove database from namespace "+namespace, func(ctx context.Context) error {
			return p.RemoveDatabase(ctx, namespace, req.ProjectName)
		})
		return nil
	}
	pc.SetStep("create namespace")
	if err := p.client.CreateProjectNamespace(ctx, namespace, req.OrgID); err != nil {
		return pc.Fail(fmt.Errorf("create namespace: %w", err))
	}
	pc.RegisterCleanup("delete namespace "+namespace, func(ctx context.Context) error {
		return p.client.DeleteNamespace(ctx, namespace)
	})
	return nil
}

func (p *MySQLProvisioner) applyCluster(ctx context.Context, opts k8s.MySQLClusterOpts, pc *ProvisionContext) error {
	pc.SetStep("issue the server certificate")
	if err := p.applyTLS(ctx, opts); err != nil {
		return err
	}
	pc.SetStep("apply mysqld settings")
	settings, err := k8s.BuildMySQLConfig(opts)
	if err != nil {
		return err
	}
	if err := p.client.ApplyCRD(ctx, k8s.ConfigMapGVR, opts.Namespace, settings); err != nil {
		return fmt.Errorf("apply mysqld settings: %w", err)
	}
	pc.SetStep("apply MySQL cluster")
	cluster, err := k8s.BuildMySQLCluster(opts)
	if err != nil {
		return err
	}
	if err := p.client.ApplyCRD(ctx, k8s.MOCOClusterGVR, opts.Namespace, cluster); err != nil {
		return fmt.Errorf("deploy MySQL cluster (is MOCO installed?): %w", err)
	}
	return nil
}

// query runs one statement on the primary as MOCO's admin over the socket.
func (p *MySQLProvisioner) query(ctx context.Context, namespace, projectID, statement string) (string, error) {
	cluster, err := p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
	if err != nil {
		return "", fmt.Errorf("read MySQL cluster: %w", err)
	}
	cmd := []string{"mysql", "--defaults-extra-file=" + mysqlAdminCnf, "--batch", "--skip-column-names", "-e", statement}
	return p.client.ExecInPod(ctx, namespace, k8s.MySQLPrimaryPod(cluster), mysqlContainer, cmd)
}

// EnsurePlatformUsers creates the platform's databases and users on the
// primary, or resets the passwords of users a restored cluster brought.
func (p *MySQLProvisioner) EnsurePlatformUsers(ctx context.Context, namespace, projectID, database string, passwords MySQLPasswords) error {
	err := p.execSecret(ctx, namespace, projectID, MySQLPlatformUsersSQL(database, passwords),
		passwords.Admin, passwords.App, passwords.AuthAdmin, passwords.Watcher)
	if err != nil {
		return fmt.Errorf("create the platform users: %w", err)
	}
	return nil
}

// SetUserPassword sets one platform user's password.
func (p *MySQLProvisioner) SetUserPassword(ctx context.Context, namespace, projectID, user, password string) error {
	if err := p.execSecret(ctx, namespace, projectID, MySQLAlterUserSQL(user, password), password); err != nil {
		return fmt.Errorf("set the %s password: %w", user, err)
	}
	return nil
}

// SetUserPasswords sets several platform users' passwords in one batch.
func (p *MySQLProvisioner) SetUserPasswords(ctx context.Context, namespace, projectID string, passwords map[string]string) error {
	users := make([]string, 0, len(passwords))
	for user := range passwords {
		users = append(users, user)
	}
	sort.Strings(users)
	var statements strings.Builder
	secrets := make([]string, 0, len(users))
	for _, user := range users {
		statements.WriteString(MySQLAlterUserSQL(user, passwords[user]) + "\n")
		secrets = append(secrets, passwords[user])
	}
	if err := p.execSecret(ctx, namespace, projectID, statements.String(), secrets...); err != nil {
		return fmt.Errorf("set the platform users' passwords: %w", err)
	}
	return nil
}

// execSecret runs statements that carry passwords on the primary. They go on
// stdin, never as exec arguments, which the API server's audit log records;
// mysql echoes a failed statement, so the error is logged with the secrets
// scrubbed and reported only as a failure.
func (p *MySQLProvisioner) execSecret(ctx context.Context, namespace, projectID, statements string, secrets ...string) error {
	cluster, err := p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
	if err != nil {
		return fmt.Errorf("read MySQL cluster: %w", err)
	}
	cmd := []string{"mysql", "--defaults-extra-file=" + mysqlAdminCnf, "--batch"}
	if _, err := p.client.ExecInPodStdin(ctx, namespace, k8s.MySQLPrimaryPod(cluster), mysqlContainer, cmd, statements); err != nil {
		reported := err.Error()
		for _, secret := range secrets {
			if secret != "" {
				reported = strings.ReplaceAll(reported, secret, "[redacted]")
			}
		}
		log.Printf("WARN: mysql on %s/%s: %s", namespace, k8s.MySQLPrimaryPod(cluster), reported)
		return errors.New("the statements were refused or could not be run on the primary")
	}
	return nil
}

// serving reports whether every instance pod is ready, MOCO reports a
// writable primary (and, with replicas, every instance in sync), and a real
// query answers on the primary.
func (p *MySQLProvisioner) serving(ctx context.Context, namespace, projectID string, instances int) (bool, error) {
	for i := 0; i < instances; i++ {
		ready, err := p.client.IsPodReady(ctx, namespace, k8s.MySQLPodName(projectID, int64(i)))
		if err != nil || !ready {
			return false, nil
		}
	}
	cluster, err := p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
	if err != nil {
		return false, nil
	}
	if !k8s.MySQLClusterAvailable(cluster) || !k8s.MySQLClusterRolledOut(cluster) ||
		(instances > 1 && !k8s.MySQLClusterHealthy(cluster)) {
		return false, nil
	}
	out, err := p.query(ctx, namespace, projectID, "SELECT 1")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(out) == "1", nil
}

func (p *MySQLProvisioner) waitServing(ctx context.Context, namespace, projectID string, instances int) error {
	return p.readyPoller.WaitUntilReady(ctx, "MySQL cluster "+k8s.MySQLClusterName(projectID),
		func(ctx context.Context) (bool, error) { return p.serving(ctx, namespace, projectID, instances) })
}

func (p *MySQLProvisioner) clusterInstances(ctx context.Context, namespace, projectID string) (int, error) {
	cluster, err := p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
	if err != nil {
		return 0, fmt.Errorf("read MySQL cluster: %w", err)
	}
	replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	if replicas < 1 {
		replicas = 1
	}
	return int(replicas), nil
}

// GetStatus is the observed state: pods, MOCO's report and a real query.
func (p *MySQLProvisioner) GetStatus(ctx context.Context, namespace, projectID string) (*ProvisioningStatus, error) {
	instances, err := p.clusterInstances(ctx, namespace, projectID)
	if err != nil {
		return &ProvisioningStatus{Phase: "Unknown", Message: err.Error()}, nil
	}
	ready, err := p.serving(ctx, namespace, projectID, instances)
	if err != nil || !ready {
		return &ProvisioningStatus{Phase: "Pending"}, nil
	}
	return &ProvisioningStatus{Phase: "Running", Ready: true}, nil
}

// StopReplication has nothing to stop: no CDC watcher runs for MySQL yet.
func (p *MySQLProvisioner) StopReplication(context.Context, string, string) error { return nil }

// Pause takes the cluster offline (MOCO scales it to zero and keeps the
// volumes), suspends MOCO's backup CronJob, which offline leaves running,
// and returns once no instance pod is left.
func (p *MySQLProvisioner) Pause(ctx context.Context, namespace, projectID string) error {
	if err := p.setOffline(ctx, namespace, projectID, true); err != nil {
		return err
	}
	if err := p.suspendBackups(ctx, namespace, projectID, true); err != nil {
		return err
	}
	return p.stopPoller.WaitUntilClear(ctx, "MySQL pods in "+namespace,
		func(ctx context.Context) ([]string, error) { return p.runningPods(ctx, namespace, projectID) })
}

// Resume brings the cluster back online, resumes its backups and returns
// once it serves.
func (p *MySQLProvisioner) Resume(ctx context.Context, namespace, projectID string) error {
	if err := p.setOffline(ctx, namespace, projectID, false); err != nil {
		return err
	}
	if err := p.suspendBackups(ctx, namespace, projectID, false); err != nil {
		return err
	}
	instances, err := p.clusterInstances(ctx, namespace, projectID)
	if err != nil {
		return err
	}
	return p.waitServing(ctx, namespace, projectID, instances)
}

// WorkloadStopped reports whether no instance pod is left.
func (p *MySQLProvisioner) WorkloadStopped(ctx context.Context, namespace, projectID string) (bool, error) {
	pods, err := p.runningPods(ctx, namespace, projectID)
	if err != nil {
		return false, err
	}
	return len(pods) == 0, nil
}

func (p *MySQLProvisioner) runningPods(ctx context.Context, namespace, projectID string) ([]string, error) {
	pods, err := p.client.GetPods(ctx, namespace, k8s.MySQLPodSelector(projectID))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names, nil
}

// updateCluster re-reads the cluster and writes change(cluster), trying
// again when the operator's own write made our copy stale.
func (p *MySQLProvisioner) updateCluster(ctx context.Context, namespace, projectID string,
	change func(*unstructured.Unstructured) (*unstructured.Unstructured, error)) error {
	var err error
	for attempt := 0; attempt < hibernationFlipAttempts; attempt++ {
		var cluster, changed *unstructured.Unstructured
		cluster, err = p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
		if err != nil {
			return fmt.Errorf("read MySQL cluster: %w", err)
		}
		if changed, err = change(cluster); err != nil {
			return err
		}
		if err = p.client.UpdateCRD(ctx, k8s.MOCOClusterGVR, namespace, changed); err == nil || !apierrors.IsConflict(err) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("update MySQL cluster: %w", err)
	}
	return nil
}

func (p *MySQLProvisioner) setOffline(ctx context.Context, namespace, projectID string, offline bool) error {
	return p.updateCluster(ctx, namespace, projectID, func(cluster *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		return k8s.WithMySQLOffline(cluster, offline), nil
	})
}

// suspendBackups flips MOCO's backup CronJob for the cluster, when one exists.
func (p *MySQLProvisioner) suspendBackups(ctx context.Context, namespace, projectID string, suspend bool) error {
	jobs, err := p.client.ListCRDs(ctx, k8s.CronJobGVR, namespace)
	if err != nil {
		return fmt.Errorf("list backup CronJobs: %w", err)
	}
	name := mysqlBackupJobName + k8s.MySQLClusterName(projectID)
	for _, job := range jobs {
		if job.GetName() != name {
			continue
		}
		flipped := job.DeepCopy()
		if err := unstructured.SetNestedField(flipped.Object, suspend, "spec", "suspend"); err != nil {
			return err
		}
		if err := p.client.UpdateCRD(ctx, k8s.CronJobGVR, namespace, flipped); err != nil {
			return fmt.Errorf("set backup CronJob suspend=%v: %w", suspend, err)
		}
	}
	return nil
}

// UpgradeMinor re-pins the cluster onto image and returns once every
// instance runs it and the database answers. MOCO rolls replicas first.
func (p *MySQLProvisioner) UpgradeMinor(ctx context.Context, namespace, projectID, image string) error {
	if err := p.PinImage(ctx, namespace, projectID, image); err != nil {
		return err
	}
	return p.AwaitImage(ctx, namespace, projectID, image)
}

// PinImage points the cluster's pod template at image.
func (p *MySQLProvisioner) PinImage(ctx context.Context, namespace, projectID, image string) error {
	return p.updateCluster(ctx, namespace, projectID, func(cluster *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		return k8s.WithMySQLImage(cluster, image)
	})
}

// AwaitImage returns once every instance runs image and the database answers.
func (p *MySQLProvisioner) AwaitImage(ctx context.Context, namespace, projectID, image string) error {
	instances, err := p.clusterInstances(ctx, namespace, projectID)
	if err != nil {
		return err
	}
	return p.readyPoller.WaitUntilReady(ctx, "MySQL upgrade of "+projectID, func(ctx context.Context) (bool, error) {
		onImage, err := p.allOnImage(ctx, namespace, projectID, image, instances)
		if err != nil || !onImage {
			return false, err
		}
		return p.serving(ctx, namespace, projectID, instances)
	})
}

// AwaitServing returns once the cluster serves with every one of instances.
func (p *MySQLProvisioner) AwaitServing(ctx context.Context, namespace, projectID string, instances int) error {
	return p.waitServing(ctx, namespace, projectID, instances)
}

func (p *MySQLProvisioner) allOnImage(ctx context.Context, namespace, projectID, image string, instances int) (bool, error) {
	pods, err := p.client.GetPods(ctx, namespace, k8s.MySQLPodSelector(projectID))
	if err != nil {
		return false, err
	}
	running := 0
	for _, pod := range pods {
		for _, container := range pod.Spec.Containers {
			if container.Name == mysqlContainer {
				if container.Image != image {
					return false, nil
				}
				running++
			}
		}
	}
	return running == instances, nil
}

// ApplyPlan moves the cluster onto a plan — more instances, resources, disk,
// settings — and returns once every instance serves. Fewer instances are
// refused (k8s.ErrMySQLScaleDown).
func (p *MySQLProvisioner) ApplyPlan(ctx context.Context, namespace, projectID string, plan config.TierConfig, disk string) error {
	if err := p.SetPlan(ctx, namespace, projectID, plan, disk); err != nil {
		return err
	}
	return p.waitServing(ctx, namespace, projectID, plan.Instances)
}

// SetPlan writes the plan's settings and cluster spec without waiting.
func (p *MySQLProvisioner) SetPlan(ctx context.Context, namespace, projectID string, plan config.TierConfig, disk string) error {
	cluster, err := p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
	if err != nil {
		return fmt.Errorf("read MySQL cluster: %w", err)
	}
	if _, err := k8s.WithMySQLTier(cluster, plan, disk); err != nil {
		return err
	}
	settings, err := k8s.BuildMySQLConfig(k8s.MySQLClusterOpts{ProjectID: projectID, Namespace: namespace, Tier: plan})
	if err != nil {
		return err
	}
	if err := p.client.ApplyCRD(ctx, k8s.ConfigMapGVR, namespace, settings); err != nil {
		return fmt.Errorf("apply mysqld settings: %w", err)
	}
	return p.updateCluster(ctx, namespace, projectID, func(cluster *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		return k8s.WithMySQLTier(cluster, plan, disk)
	})
}

// Deprovision removes the cluster (waited out, so MOCO's finalizer releases
// its volumes) and then the namespace, and returns once both are observed gone.
func (p *MySQLProvisioner) Deprovision(ctx context.Context, namespace, projectID string) error {
	deleting, err := p.client.NamespaceDeleting(ctx, namespace)
	if err != nil {
		return err
	}
	if !deleting {
		if err := p.deleteClusterAndWait(ctx, namespace, projectID); err != nil {
			return err
		}
		if err := p.client.DeleteNamespace(ctx, namespace); err != nil {
			return fmt.Errorf("delete namespace %s: %w", namespace, err)
		}
	}
	return p.stopPoller.WaitUntilClear(ctx, "namespace "+namespace,
		func(ctx context.Context) ([]string, error) { return namespaceRemnants(ctx, p.client, namespace) })
}

// RemoveDatabase deletes the database from a namespace it shares with the
// project's apps: the cluster (waited out) and its settings.
func (p *MySQLProvisioner) RemoveDatabase(ctx context.Context, namespace, projectID string) error {
	if err := p.deleteClusterAndWait(ctx, namespace, projectID); err != nil {
		return err
	}
	if err := p.client.DeleteCRD(ctx, k8s.ConfigMapGVR, namespace, k8s.MySQLConfigName(projectID)); err != nil {
		return fmt.Errorf("delete mysqld settings: %w", err)
	}
	if err := p.removeTLS(ctx, namespace, projectID); err != nil {
		return err
	}
	return p.removeBackupObjects(ctx, namespace, projectID)
}

func (p *MySQLProvisioner) deleteClusterAndWait(ctx context.Context, namespace, projectID string) error {
	name := k8s.MySQLClusterName(projectID)
	if err := p.client.DeleteCRD(ctx, k8s.MOCOClusterGVR, namespace, name); err != nil {
		return fmt.Errorf("delete MySQL cluster %s: %w", name, err)
	}
	return p.stopPoller.WaitUntilClear(ctx, "MySQL cluster "+name, func(ctx context.Context) ([]string, error) {
		exists, err := p.client.CRDExists(ctx, k8s.MOCOClusterGVR, namespace, name)
		if err != nil || !exists {
			return nil, err
		}
		return []string{name}, nil
	})
}

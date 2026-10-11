package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// MySQL projects on MOCO (ADR 0041). The lifecycle a MySQL project shares
// with Postgres (create, status, pause, resume, delete) goes through the
// provisioner factory; what differs lives here.

// ErrNotForMySQL refuses an operation that only a Postgres project has.
var ErrNotForMySQL = errors.New("not available for MySQL projects")

// ErrMySQLBackupScheduleFixed refuses a schedule in a MySQL request: the plan
// sets it, and the interval is how far back a restore can reach.
var ErrMySQLBackupScheduleFixed = errors.New("MySQL backups run on the plan's schedule (hourly on Standard, every 6 hours on Free); remove backup.schedule")

// validateMySQLDatabaseRequest checks the settings a MySQL database is
// created with: a catalogued version (or none, for the newest) and nothing
// that only Postgres has.
func validateMySQLDatabaseRequest(req domain.ProvisioningRequest) error {
	if strings.TrimSpace(req.PostgresVersion) != "" {
		return fmt.Errorf("postgresVersion does not apply to a MySQL project; name mysqlVersion (supported: %s)",
			strings.Join(config.MySQLVersions(), ", "))
	}
	if req.MySQLVersion != "" {
		if _, err := config.MySQLImage(req.MySQLVersion); err != nil {
			return err
		}
	}
	if req.DocumentDB {
		return fmt.Errorf("DocumentDB is %w", ErrNotForMySQL)
	}
	if len(req.Parameters) > 0 {
		return fmt.Errorf("parameters are Postgres settings; they are %w", ErrNotForMySQL)
	}
	if req.Backup != nil && strings.TrimSpace(req.Backup.Schedule) != "" {
		return ErrMySQLBackupScheduleFixed
	}
	return validateOwnerAndTags(req)
}

// mysqlVersionOf is the version a validated MySQL request is created on.
func mysqlVersionOf(req domain.ProvisioningRequest) string {
	if req.MySQLVersion == "" {
		return config.DefaultMySQLVersion()
	}
	return strings.TrimSpace(req.MySQLVersion)
}

func (s *ProvisioningService) mysqlProvisioner() (*provisioner.MySQLProvisioner, error) {
	if s.factory == nil {
		return nil, fmt.Errorf("unsupported database type: %s", domain.MySQL)
	}
	prov, ok := s.factory.Get(domain.MySQL)
	if !ok {
		return nil, fmt.Errorf("unsupported database type: %s", domain.MySQL)
	}
	mysql, ok := prov.(*provisioner.MySQLProvisioner)
	if !ok {
		return nil, fmt.Errorf("unsupported database type: %s", domain.MySQL)
	}
	return mysql, nil
}

// mysqlServer is what the platform runs on a project's mysqld, on MOCO or on
// a single host.
type mysqlServer interface {
	EnsurePlatformUsers(ctx context.Context, namespace, projectID, database string, passwords provisioner.MySQLPasswords) error
	SetUserPassword(ctx context.Context, namespace, projectID, user, password string) error
	SetUserPasswords(ctx context.Context, namespace, projectID string, passwords map[string]string) error
	RemoveDatabase(ctx context.Context, namespace, projectID string) error
}

var (
	_ mysqlServer = (*provisioner.MySQLProvisioner)(nil)
	_ mysqlServer = (*provisioner.DockerMySQLProvisioner)(nil)
)

// mysqlServerOf is the provisioner that runs this installation's MySQL.
func (s *ProvisioningService) mysqlServerOf() (mysqlServer, error) {
	if s.factory == nil {
		return nil, fmt.Errorf("unsupported database type: %s", domain.MySQL)
	}
	prov, ok := s.factory.Get(domain.MySQL)
	if !ok {
		return nil, fmt.Errorf("unsupported database type: %s", domain.MySQL)
	}
	server, ok := prov.(mysqlServer)
	if !ok {
		return nil, fmt.Errorf("unsupported database type: %s", domain.MySQL)
	}
	return server, nil
}

// MySQLProvisioner is the MOCO provisioner, or nil where MySQL is not served.
func (s *ProvisioningService) MySQLProvisioner() *provisioner.MySQLProvisioner {
	mysql, err := s.mysqlProvisioner()
	if err != nil {
		return nil
	}
	return mysql
}

// AllowMySQLCreation turns the creation of new MySQL projects on or off.
func (s *ProvisioningService) AllowMySQLCreation(allowed bool) { s.mysqlCreation = allowed }

// creates reports whether a new database of this engine may be created.
func (s *ProvisioningService) creates(dbType domain.DatabaseType) bool {
	if _, ok := s.factory.Get(dbType); !ok {
		return false
	}
	return dbType != domain.MySQL || s.mysqlCreation
}

// requirePostgres refuses a Postgres-only cluster operation on any other engine.
func requirePostgres(inst *domain.DatabaseInstance) error {
	if inst.DBType == domain.MySQL {
		return ErrNotForMySQL
	}
	return nil
}

// upgradeMySQL moves a MySQL project onto a later catalogue version of its
// series. The new image is pinned and recorded now; the rolling restart is
// followed in the background, like a Postgres minor upgrade is not waited on.
func (s *ProvisioningService) upgradeMySQL(ctx context.Context, projectID, version string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationUpgrade, requireActive)
	if err != nil {
		return err
	}
	defer release()
	if inst.MySQLVersion == version {
		// Already there: the same call is how a client converges.
		return nil
	}
	if err := config.MySQLUpgradeAllowed(inst.MySQLVersion, version); err != nil {
		return err
	}
	image, err := config.MySQLImage(version)
	if err != nil {
		return err
	}
	mysql, err := s.mysqlProvisioner()
	if err != nil {
		return err
	}
	if err := mysql.PinImage(ctx, inst.Namespace, projectID, image); err != nil {
		return err
	}
	previous := inst.MySQLVersion
	inst.MySQLVersion = version
	if err := s.store.UpdateIfStatus(inst, inst.Status); err != nil {
		inst.MySQLVersion = previous
		return fmt.Errorf("record mysql %s: %w", version, err)
	}
	namespace := inst.Namespace
	s.startInBackground(func() {
		if err := mysql.AwaitImage(context.WithoutCancel(ctx), namespace, projectID, image); err != nil {
			log.Printf("WARN: mysql upgrade of %s to %s not observed complete: %v", projectID, version, err)
		}
	})
	return nil
}

// applyMySQLTier moves a MySQL project onto its organisation's plan: more
// instances (never fewer), resources, disk and settings. The change is
// reserved and recorded now; the scale-up is followed in the background.
func (s *ProvisioningService) applyMySQLTier(ctx context.Context, inst *domain.DatabaseInstance, orgTier domain.TierType, tierPlan config.TierConfig) error {
	plan, err := config.MySQLPlan(tierPlan)
	if err != nil {
		return err
	}
	if err := s.RequireNodeCount(ctx, orgTier, plan.Instances); err != nil {
		return err
	}
	mysql, err := s.mysqlProvisioner()
	if err != nil {
		return err
	}
	if err := s.reserveMySQLPlan(ctx, inst, mysql, orgTier, plan); err != nil {
		return err
	}
	previous, previousSchedule := inst.Tier, inst.BackupSchedule
	if err := s.moveMySQLBackupPlan(ctx, inst, mysql, orgTier, plan); err != nil {
		return err
	}
	inst.Tier = orgTier
	if err := s.store.UpdateIfStatus(inst, inst.Status); err != nil {
		inst.Tier, inst.BackupSchedule = previous, previousSchedule
		return fmt.Errorf("record the %s plan: %w", orgTier, err)
	}
	namespace, projectID, instances := inst.Namespace, inst.ProjectID, plan.Instances
	s.startInBackground(func() {
		if err := mysql.AwaitServing(context.WithoutCancel(ctx), namespace, projectID, instances); err != nil {
			log.Printf("WARN: %s on the %s plan not observed serving: %v", projectID, orgTier, err)
		}
	})
	return nil
}

// reserveMySQLPlan writes the plan onto the cluster while the storage it adds
// is held in the platform's budget, recording the disk first.
func (s *ProvisioningService) reserveMySQLPlan(ctx context.Context, inst *domain.DatabaseInstance,
	mysql *provisioner.MySQLProvisioner, orgTier domain.TierType, plan config.TierConfig) error {
	cluster, err := s.k8sClient.GetCRD(ctx, k8s.MOCOClusterGVR, inst.Namespace, k8s.MySQLClusterName(inst.ProjectID))
	if err != nil {
		return fmt.Errorf(errGetCRDFmt, err)
	}
	disk, err := s.planMySQLDisk(ctx, inst, cluster, plan)
	if err != nil {
		return err
	}
	next, err := k8s.WithMySQLTier(cluster, plan, disk)
	if err != nil {
		return err
	}
	before, err := k8s.MySQLClusterStorageBytes(cluster)
	if err != nil {
		return err
	}
	after, err := k8s.MySQLClusterStorageBytes(next)
	if err != nil {
		return err
	}
	apply := func() error {
		return s.growDisk(ctx, inst, disk, func() error {
			return mysql.SetPlan(ctx, inst.Namespace, inst.ProjectID, plan, disk)
		})
	}
	if err := s.storageBudget.Reserve(ctx, after-before, "moving the MySQL database onto the "+string(orgTier)+" plan", apply); err != nil {
		return fmt.Errorf("apply the %s plan to the cluster: %w", orgTier, err)
	}
	return nil
}

// planMySQLDisk is the disk the plan gives: the current one when it is
// already at least the plan's start, else the plan's start, which the
// storage class must be able to grow to.
func (s *ProvisioningService) planMySQLDisk(ctx context.Context, inst *domain.DatabaseInstance, cluster *unstructured.Unstructured,
	plan config.TierConfig) (string, error) {
	current, err := k8s.MySQLClusterDisk(cluster)
	if err != nil {
		return "", err
	}
	start, err := resource.ParseQuantity(plan.StorageSize)
	if err != nil {
		return "", fmt.Errorf("plan disk %q: %w", plan.StorageSize, err)
	}
	if current.Cmp(start) >= 0 {
		return current.String(), nil
	}
	if err := s.k8sClient.ClusterVolumesExpandable(ctx, inst.Namespace, cluster.GetName()); err != nil {
		return "", err
	}
	return plan.StorageSize, nil
}

// mysqlClusterSettings is the plan a MySQL cluster runs: its instances,
// resources and disk. It has no tunable Postgres settings.
func (s *ProvisioningService) mysqlClusterSettings(ctx context.Context, inst *domain.DatabaseInstance) (*ClusterSettings, error) {
	tier, err := s.tierConfig(ctx, inst.Tier)
	if err != nil {
		return nil, err
	}
	orgTier, err := s.orgTier(ctx, inst.OrgID)
	if err != nil {
		return nil, err
	}
	cluster, err := s.k8sClient.GetCRD(ctx, k8s.MOCOClusterGVR, inst.Namespace, k8s.MySQLClusterName(inst.ProjectID))
	if err != nil {
		return nil, fmt.Errorf(errGetCRDFmt, err)
	}
	disk, err := k8s.MySQLClusterDisk(cluster)
	if err != nil {
		return nil, err
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	cpu, memory := k8s.MySQLClusterResources(cluster)
	return &ClusterSettings{
		ProjectID: inst.ProjectID, Tier: inst.Tier, OrgTier: orgTier,
		StorageSize: disk.String(), StorageStart: tier.StorageSize, StorageLimit: tier.MaxStorageSize,
		Instances: instances, CPU: cpu, Memory: memory,
		Parameters: map[string]string{}, TunableParameters: []string{},
	}, nil
}

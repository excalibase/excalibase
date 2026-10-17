package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// OperationRoleReconcile is the lease the startup reconcile holds on a project
// while it re-runs the registration SQL there.
const OperationRoleReconcile ProjectOperation = "platform role reconcile"

// reconcileTimeout bounds one project's reconcile, exec and vault included.
const reconcileTimeout = 2 * time.Minute

// RoleReconcileReport is what one reconcile pass did.
type RoleReconcileReport struct {
	Reconciled int
	Failed     []error
}

// ReconcileProjectRoles re-runs the registration SQL on every running Postgres
// project registered before EXC-623, which it knows by the excalibase_scheduler
// login missing from vault: the excalibase schema is left the superuser's
// alone, whatever the app role planted there is dropped, and the scheduler
// login is created, filed and admitted by certificate. Filing that login is
// the last step, so a project that fails part-way is tried again next pass,
// and one already reconciled is not touched.
func (s *ProvisioningService) ReconcileProjectRoles(ctx context.Context) RoleReconcileReport {
	var report RoleReconcileReport
	if s.vault == nil || s.vault.Sealed() || (s.k8sClient == nil && s.dockerClient == nil) {
		return report
	}
	instances, err := s.store.FindAll()
	if err != nil {
		report.Failed = append(report.Failed, fmt.Errorf("list projects: %w", err))
		return report
	}
	for _, inst := range instances {
		if !reconcilesRoles(inst) {
			continue
		}
		reconciled, err := s.reconcileIfPending(ctx, inst.ProjectID)
		if err != nil {
			report.Failed = append(report.Failed, fmt.Errorf("project %s: %w", inst.ProjectID, err))
			continue
		}
		if reconciled {
			report.Reconciled++
		}
	}
	return report
}

// reconcileIfPending reconciles a project that has no scheduler login yet.
func (s *ProvisioningService) reconcileIfPending(ctx context.Context, projectID string) (bool, error) {
	pending, err := s.missingSchedulerLogin(projectID)
	if err != nil || !pending {
		return false, err
	}
	projectCtx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	if err := s.reconcileProject(projectCtx, projectID); err != nil {
		return false, err
	}
	return true, nil
}

// missingSchedulerLogin reports whether vault holds no excalibase_scheduler
// login for the project, which marks one registered before EXC-623.
func (s *ProvisioningService) missingSchedulerLogin(projectID string) (bool, error) {
	filed, err := s.filedCredentialPaths(projectID)
	if err != nil {
		return false, err
	}
	return !filed[vaultCredentialPath(projectID, roleScheduler)], nil
}

// reconcilesRoles is a running Postgres project with a database.
func reconcilesRoles(inst *domain.DatabaseInstance) bool {
	return inst != nil && inst.DBType == domain.PostgreSQL && !inst.NoDatabase &&
		inst.Status == string(domain.StatusActive)
}

func (s *ProvisioningService) reconcileProject(ctx context.Context, projectID string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationRoleReconcile, requireActive)
	if err != nil {
		return err
	}
	defer release()

	spec := reconcileSpec(inst)
	creds, mintScheduler, err := s.filedRoleCredentials(projectID)
	if err != nil {
		return err
	}
	roleSQL := projectRoleSQL(spec, creds, s.publicationName)
	if mintScheduler {
		roleSQL += BuildProjectRoleResetSQL([]RolePassword{{Role: roleScheduler, Password: creds.schedulerPassword}})
	}
	cmd := []string{"psql", "-U", "postgres", "-d", spec.databaseName, "-c", roleSQL}
	if err := s.execRoleSQL(ctx, spec.namespace, spec.projectID+primaryPodSuffix, cmd); err != nil {
		return fmt.Errorf("re-run registration SQL: %w", err)
	}
	if err := s.ensureClusterCertLogins(ctx, inst); err != nil {
		return err
	}
	if !mintScheduler {
		return nil
	}
	return s.fileSchedulerCredential(ctx, spec, creds.schedulerPassword)
}

func reconcileSpec(inst *domain.DatabaseInstance) projectRoleSpec {
	port := 5432
	if inst.Port != nil {
		port = *inst.Port
	}
	return projectRoleSpec{
		projectID:     inst.ProjectID,
		namespace:     inst.Namespace,
		host:          inst.Host,
		port:          port,
		databaseName:  inst.DatabaseName,
		adminUsername: inst.Username,
		documentDB:    inst.DocumentDB,
	}
}

// filedRoleCredentials reads the passwords vault holds for the platform roles.
// They only matter for a role the SQL would create, which for a registered
// project is excalibase_scheduler alone; mint reports that it must be.
func (s *ProvisioningService) filedRoleCredentials(projectID string) (projectRoleCredentials, bool, error) {
	filed, err := s.filedCredentialPaths(projectID)
	if err != nil {
		return projectRoleCredentials{}, false, err
	}
	password := func(role string) (string, error) {
		if !filed[vaultCredentialPath(projectID, role)] {
			return generatePassword(32), nil
		}
		record, err := s.vault.Get(vaultCredentialPath(projectID, role))
		if err != nil {
			return "", fmt.Errorf("read %s credential: %w", role, err)
		}
		if record["password"] == "" {
			return generatePassword(32), nil
		}
		return record["password"], nil
	}
	var creds projectRoleCredentials
	for role, target := range map[string]*string{
		roleAuthAdmin: &creds.authPassword, roleApp: &creds.appPassword,
		roleWatcher: &creds.watcherPassword, roleScheduler: &creds.schedulerPassword,
	} {
		value, err := password(role)
		if err != nil {
			return projectRoleCredentials{}, false, err
		}
		*target = value
	}
	return creds, !filed[vaultCredentialPath(projectID, roleScheduler)], nil
}

// fileSchedulerCredential files the scheduler login the reconcile just
// created, with its client certificate on Kubernetes.
func (s *ProvisioningService) fileSchedulerCredential(ctx context.Context, spec projectRoleSpec, password string) error {
	record := map[string]string{
		"host": spec.host, "port": strconv.Itoa(spec.port), "database": spec.databaseName,
		"username": roleScheduler, "password": password,
	}
	if s.k8sClient != nil && s.dockerClient == nil {
		ca, err := readClusterCA(ctx, s.k8sClient, spec.namespace, spec.projectID)
		if err != nil {
			return err
		}
		material, err := tenantcert.Issue(ca, roleScheduler, time.Now())
		if err != nil {
			return fmt.Errorf("issue %s certificate: %w", roleScheduler, err)
		}
		record = material.AddTo(record)
	}
	if err := s.vault.Put(vaultCredentialPath(spec.projectID, roleScheduler), record); err != nil {
		return fmt.Errorf("file %s credential: %w", roleScheduler, err)
	}
	return nil
}

// ensureClusterCertLogins adds the certificate login of any platform role the
// cluster's pg_hba predates. Single-host databases have no pg_hba to manage.
func (s *ProvisioningService) ensureClusterCertLogins(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.k8sClient == nil || s.dockerClient != nil || inst.DeploymentMode == domain.ModeDocker {
		return nil
	}
	name := inst.ProjectID + postgresClusterSuffix
	var err error
	for attempt := 0; attempt < clusterUpdateAttempts; attempt++ {
		if err = s.rewriteClusterCertLogins(ctx, inst.Namespace, name); !apierrors.IsConflict(err) {
			break
		}
	}
	return err
}

func (s *ProvisioningService) rewriteClusterCertLogins(ctx context.Context, namespace, name string) error {
	current, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, namespace, name)
	if err != nil {
		return fmt.Errorf("read cluster %s: %w", name, err)
	}
	cluster := current.DeepCopy()
	changed, err := k8s.EnsurePlatformCertLogins(cluster)
	if err != nil || !changed {
		return err
	}
	if err := s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		return fmt.Errorf("update cluster %s: %w", name, err)
	}
	return nil
}

// StartRoleReconcile runs a reconcile pass now and then every interval while
// this replica leads, so a project that was paused, busy or failing at one pass
// is reached at a later one. It returns the function that stops it.
func (s *ProvisioningService) StartRoleReconcile(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		s.RunRoleReconcile(ctx, leader)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RunRoleReconcile(ctx, leader)
			}
		}
	}()
	return cancel
}

// RunRoleReconcile is one leader-gated reconcile pass, logged.
func (s *ProvisioningService) RunRoleReconcile(ctx context.Context, leader LeaderChecker) RoleReconcileReport {
	ok, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("ERROR: platform role reconcile: leadership check: %v", err)
		return RoleReconcileReport{}
	}
	if !ok {
		return RoleReconcileReport{}
	}
	report := s.ReconcileProjectRoles(ctx)
	for _, failure := range report.Failed {
		if errors.Is(failure, ErrProjectOperationRunning) {
			log.Printf("WARN: platform role reconcile: %v (reconciled at the next start)", failure)
			continue
		}
		log.Printf("ERROR: platform role reconcile: %v", failure)
	}
	if report.Reconciled > 0 || len(report.Failed) > 0 {
		log.Printf("platform role reconcile: %d project(s) reconciled, %d failed", report.Reconciled, len(report.Failed))
	}
	return report
}

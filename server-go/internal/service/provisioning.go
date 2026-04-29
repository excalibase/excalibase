package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

type ProvisioningService struct {
	store     storage.InstanceStore
	orgStore  storage.OrgStore      // optional, for org slug lookup
	factory   *provisioner.Factory
	vault     vaultclient.VaultClient // optional
	k8sClient k8s.KubeClient      // optional, for role creation via pod exec
	dockerClient provisioner.DockerClient // optional, for role creation via container exec (Docker mode)
	pgdog          *PgDogNotifier       // optional, for PgDog config registration
	selfHostedMode bool                 // skip tier enforcement
	// publicationName is the CDC publication created during role setup.
	// Must match watcher's publication_name config and graphql's
	// app.realtime.publication-name. Empty defaults to "cdc_watcher_pub".
	publicationName string
}

func NewProvisioningService(store storage.InstanceStore, factory *provisioner.Factory, k8sClient k8s.KubeClient) *ProvisioningService {
	return &ProvisioningService{store: store, factory: factory, k8sClient: k8sClient}
}

func (s *ProvisioningService) SetVault(v vaultclient.VaultClient) {
	s.vault = v
}

func (s *ProvisioningService) SetPgDogNotifier(n *PgDogNotifier) {
	s.pgdog = n
}

func (s *ProvisioningService) SetPublicationName(name string) {
	s.publicationName = name
}

func (s *ProvisioningService) PublicationName() string {
	if s.publicationName == "" {
		return "cdc_watcher_pub"
	}
	return s.publicationName
}

func (s *ProvisioningService) SetOrgStore(os storage.OrgStore) {
	s.orgStore = os
}

func (s *ProvisioningService) SetSelfHostedMode(enabled bool) {
	s.selfHostedMode = enabled
}

func (s *ProvisioningService) SetDockerClient(dc provisioner.DockerClient) {
	s.dockerClient = dc
}

// ProvisionBYOC registers an externally managed database (no provisioning pipeline).
// Validates connectivity, stores credentials in vault, creates instance record.
func (s *ProvisioningService) ProvisionBYOC(ctx context.Context, req domain.BYOCRequest) (*domain.ProvisioningResponse, error) {
	// Generate opaque project ref (display name stays as req.ProjectName)
	var projectRef string
	for i := 0; i < 5; i++ {
		projectRef = generateProjectRef()
		if existing, _ := s.store.FindByProjectID(projectRef); existing == nil {
			break
		}
		projectRef = ""
	}
	if projectRef == "" {
		return nil, fmt.Errorf("failed to generate unique project ref")
	}

	// Resolve org slug for vault path
	orgSlug := "default"
	if s.orgStore != nil && req.OrgID != "" {
		if org, err := s.orgStore.FindOrgByID(ctx, req.OrgID); err == nil && org != nil {
			orgSlug = org.Slug
		}
	}

	// Store credentials in vault under the generated ref
	if s.vault != nil {
		port := strconv.Itoa(req.Port)
		creds := map[string]string{
			"host":     req.Host,
			"port":     port,
			"username": req.Username,
			"password": req.Password,
			"database": req.Database,
		}
		s.vault.Put(fmt.Sprintf("projects/%s/%s/credentials/excalibase_app", orgSlug, projectRef), creds)
		s.vault.Put(fmt.Sprintf("projects/%s/%s/credentials/admin", orgSlug, projectRef), creds)
	}

	// Create instance record
	portInt := req.Port
	inst := &domain.DatabaseInstance{
		ProjectID:      projectRef,
		ProjectName:    req.ProjectName,
		OrgID:          req.OrgID,
		DBType:         domain.PostgreSQL,
		Tier:           domain.Free,
		DeploymentMode: domain.ModeBYOC,
		Host:           req.Host,
		Port:           &portInt,
		DatabaseName:   req.Database,
		Status:         "ACTIVE",
		CurrentStage:   domain.StageCompleted,
	}

	if err := s.store.Save(inst); err != nil {
		return nil, fmt.Errorf("save instance: %w", err)
	}

	log.Printf("BYOC project registered: %s (ref=%s, org=%s, host=%s)", req.ProjectName, projectRef, orgSlug, req.Host)

	return &domain.ProvisioningResponse{
		ProjectID:    projectRef,
		ProjectName:  req.ProjectName,
		Status:       "ACTIVE",
		CurrentStage: domain.StageCompleted,
		Host:         req.Host,
		Port:         &portInt,
		DatabaseName: req.Database,
	}, nil
}

func (s *ProvisioningService) Provision(ctx context.Context, req domain.ProvisioningRequest) (*domain.ProvisioningResponse, error) {
	inst, prov, tier, err := s.prepareProvisioning(req)
	if err != nil {
		return nil, err
	}

	// From this point on, req.ProjectName is replaced with the generated opaque
	// ref so the provisioner and all downstream callers use the K8s-safe ID.
	// The user's display name is preserved on inst.ProjectName.
	req.ProjectName = inst.ProjectID

	if err := s.store.Save(inst); err != nil {
		log.Printf("WARN: failed to persist instance state: %v", err)
	}

	pc := provisioner.NewProvisionContext(
		func(stage domain.ProvisioningStage) {
			inst.CurrentStage = stage
			inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
			if err := s.store.Save(inst); err != nil {
				log.Printf("WARN: failed to persist instance state: %v", err)
			}
		},
		func(step string) {
			inst.CurrentStep = step
			if err := s.store.Save(inst); err != nil {
				log.Printf("WARN: failed to persist instance state: %v", err)
			}
		},
	)

	// Populate S3 credentials from vault if backup enabled but no S3 creds provided
	if req.Backup != nil && req.Backup.Enabled && req.Backup.S3 == nil && s.vault != nil && !s.vault.Sealed() {
		if s3Creds, err := s.vault.Get("backup/s3"); err == nil {
			req.Backup.S3 = &domain.S3Credentials{
				AccessKeyID:     s3Creds["accessKeyId"],
				SecretAccessKey: s3Creds["secretAccessKey"],
				Bucket:          s3Creds["bucket"],
				Region:          s3Creds["region"],
				Endpoint:        s3Creds["endpoint"],
			}
		}
	}

	var result *provisioner.ProvisioningResult
	var provErr error
	if rbProv, ok := prov.(provisioner.RollbackAware); ok {
		result, provErr = rbProv.ProvisionWithRollback(ctx, req, tier, pc)
	} else {
		// Legacy path — no rollback support.
		result, provErr = prov.Provision(ctx, req, tier, pc.SetStage)
	}
	if provErr != nil {
		return s.handleProvisionFailure(ctx, inst, req, provErr, pc), nil
	}

	return s.finalizeProvisioning(ctx, inst, req, result, pc)
}

// prepareProvisioning validates the request and creates the initial instance record.
func (s *ProvisioningService) prepareProvisioning(req domain.ProvisioningRequest) (*domain.DatabaseInstance, provisioner.DatabaseProvisioner, config.TierConfig, error) {
	if err := validateProvisioningRequest(req); err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	// Generate an opaque project ref, retrying in the (cosmically unlikely)
	// event of a collision with an existing project.
	var projectRef string
	for i := 0; i < 5; i++ {
		projectRef = generateProjectRef()
		if existing, _ := s.store.FindByProjectID(projectRef); existing == nil {
			break
		}
		projectRef = ""
	}
	if projectRef == "" {
		return nil, nil, config.TierConfig{}, fmt.Errorf("failed to generate unique project ref after 5 attempts")
	}

	tier, err := config.GetTierConfig(req.Tier)
	if err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	// Enforce max projects per org for this tier (skip in self-hosted mode)
	if tier.MaxProjects > 0 && !s.selfHostedMode {
		allInstances, _ := s.store.FindAll()
		orgCount := 0
		for _, inst := range allInstances {
			if inst.OrgID == req.OrgID {
				orgCount++
			}
		}
		if orgCount >= tier.MaxProjects {
			return nil, nil, config.TierConfig{}, fmt.Errorf("org %s has reached the maximum of %d projects for %s tier", req.OrgID, tier.MaxProjects, req.Tier)
		}
	}

	// Enforce backup availability per tier
	if req.Backup != nil && req.Backup.Enabled && !tier.BackupEnabled && !s.selfHostedMode {
		return nil, nil, config.TierConfig{}, fmt.Errorf("backups are not available on %s tier", req.Tier)
	}

	prov, ok := s.factory.Get(req.DBType)
	if !ok {
		return nil, nil, config.TierConfig{}, fmt.Errorf("unsupported database type: %s", req.DBType)
	}

	now := &domain.FlexTime{Time: time.Now()}
	namespace := fmt.Sprintf("%s-%s", req.OrgID, projectRef)
	inst := &domain.DatabaseInstance{
		ProjectID:    projectRef,
		ProjectName:  req.ProjectName,
		OrgID:        req.OrgID,
		OwnerID:      req.OwnerID,
		DBType:       req.DBType,
		Tier:         req.Tier,
		Namespace:    namespace,
		Status:       "PROVISIONING",
		CurrentStage: domain.StageValidating,
		CreatedAt:    now,
	}

	if req.Backup != nil {
		inst.BackupEnabled = boolPtr(req.Backup.Enabled)
		inst.BackupSchedule = req.Backup.Schedule
		inst.BackupRetentionDays = intPtr(req.Backup.Retention)
	}

	return inst, prov, tier, nil
}

// handleProvisionFailure captures the failing stage/step, runs all registered
// compensation actions in LIFO order, persists the rollback log, and returns a
// failure response. Cleanup errors are captured per-action but never stop the
// rollback or return an error — the DB row surfaces the partial state.
func (s *ProvisioningService) handleProvisionFailure(
	ctx context.Context,
	inst *domain.DatabaseInstance,
	req domain.ProvisioningRequest,
	err error,
	pc *provisioner.ProvisionContext,
) *domain.ProvisioningResponse {
	var se *provisioner.StageError
	if errors.As(err, &se) {
		inst.FailureStage = se.Stage
		inst.FailureStep = se.Step
		if unwrapped := errors.Unwrap(err); unwrapped != nil {
			inst.FailureReason = unwrapped.Error()
		} else {
			inst.FailureReason = err.Error()
		}
	} else {
		inst.FailureStage = pc.Stage()
		inst.FailureStep = pc.Step()
		inst.FailureReason = err.Error()
	}

	log.Printf("Provisioning failed for %s at stage %s (%s): %v — running %d cleanup(s)",
		req.ProjectName, inst.FailureStage, inst.FailureStep, inst.FailureReason, pc.CleanupCount())

	results := pc.Rollback(ctx)
	if logJSON, jerr := json.Marshal(results); jerr == nil {
		inst.RollbackLog = string(logJSON)
	} else {
		log.Printf("WARN: marshal rollback log: %v", jerr)
	}

	inst.Status = "FAILED"
	inst.CurrentStage = domain.StageFailed
	if saveErr := s.store.Save(inst); saveErr != nil {
		log.Printf("WARN: failed to persist instance state: %v", saveErr)
	}

	return &domain.ProvisioningResponse{
		ProjectID:     inst.ProjectID,
		ProjectName:   inst.ProjectName,
		Status:        "FAILED",
		CurrentStage:  domain.StageFailed,
		Namespace:     inst.Namespace,
		FailureReason: inst.FailureReason,
		FailureStage:  inst.FailureStage,
		FailureStep:   inst.FailureStep,
		RollbackLog:   inst.RollbackLog,
		CreatedAt:     inst.CreatedAt,
	}
}

// finalizeProvisioning updates the instance with connection details and creates roles.
func (s *ProvisioningService) finalizeProvisioning(ctx context.Context, inst *domain.DatabaseInstance, req domain.ProvisioningRequest, result *provisioner.ProvisioningResult, pc *provisioner.ProvisionContext) (*domain.ProvisioningResponse, error) {
	port := result.Port
	inst.Host = result.Host
	inst.ReadOnlyHost = result.ReadOnlyHost
	inst.Port = &port
	inst.DatabaseName = result.DatabaseName
	inst.Username = result.Username
	inst.Password = result.Password
	inst.SSLMode = result.SSLMode
	// Docker provisioner stores the container ID in result.Namespace —
	// overwrite the K8s-style namespace so Deprovision and role creation
	// can reach the right target.
	if result.Namespace != "" {
		inst.Namespace = result.Namespace
	}
	inst.MetricsEndpoint = fmt.Sprintf("http://%s-postgres-1.%s.svc.cluster.local:9187/metrics", req.ProjectName, inst.Namespace)

	// Role creation (writes to vault + executes psql in primary pod).
	// This is an atomic step with its own rollback — failures here trigger
	// full rollback via the same ProvisionContext (namespace + vault entries).
	canExecSQL := (s.k8sClient != nil) || (s.dockerClient != nil)
	if s.vault != nil && canExecSQL && !s.vault.Sealed() {
		if err := s.createProjectRoles(ctx, req, result, inst.Namespace, pc); err != nil {
			return s.handleProvisionFailure(ctx, inst, req, err, pc), nil
		}
	}

	inst.Status = "ACTIVE"
	inst.CurrentStage = domain.StageCompleted

	delProtection := false
	inst.DeletionProtection = &delProtection
	poolerEnabled := false
	inst.PoolerEnabled = &poolerEnabled

	finalNow := &domain.FlexTime{Time: time.Now()}
	inst.UpdatedAt = finalNow
	inst.LastHealthCheck = finalNow
	if err := s.store.Save(inst); err != nil {
		log.Printf("WARN: failed to persist instance state: %v", err)
	}

	// Register with PgDog connection pooler
	if s.pgdog != nil {
		if err := s.pgdog.RegisterCluster(ctx, req.ProjectName, inst.Namespace,
			result.DatabaseName, result.Username, result.Password); err != nil {
			log.Printf("WARN: pgdog register: %v", err)
		}
	}

	return &domain.ProvisioningResponse{
		ProjectID:    inst.ProjectID,
		ProjectName:  inst.ProjectName,
		Status:       "ACTIVE",
		CurrentStage: domain.StageCompleted,
		Namespace:    inst.Namespace,
		Host:         result.Host,
		Port:         &port,
		DatabaseName: result.DatabaseName,
		CreatedAt:    inst.CreatedAt,
	}, nil
}

func (s *ProvisioningService) Deprovision(ctx context.Context, projectID string) error {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	if inst.DeletionProtection != nil && *inst.DeletionProtection {
		return fmt.Errorf("deletion protection is enabled for %s", projectID)
	}

	// Deregister from PgDog connection pooler
	if s.pgdog != nil {
		if err := s.pgdog.DeregisterCluster(ctx, projectID, inst.Username); err != nil {
			log.Printf("WARN: pgdog deregister: %v", err)
		}
	}

	prov, ok := s.factory.Get(inst.DBType)
	if ok {
		if err := prov.Deprovision(ctx, inst.Namespace, projectID); err != nil {
			log.Printf("WARN: K8s deprovision failed for %s: %v", projectID, err)
		}
	}

	return s.store.Delete(projectID)
}

func (s *ProvisioningService) GetInstance(projectID string) (*domain.DatabaseInstance, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	return inst, nil
}

func (s *ProvisioningService) GetAllInstances() ([]*domain.DatabaseInstance, error) {
	return s.store.FindAll()
}

func (s *ProvisioningService) GetInstancesByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	return s.store.FindByOwner(ownerID)
}

func (s *ProvisioningService) GetCredentials(projectID string) (*domain.CredentialsResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}

	port := 5432
	if inst.Port != nil {
		port = *inst.Port
	}

	connURL := fmt.Sprintf("postgresql://%s:%s@%s:%d/%s?sslmode=%s",
		inst.Username, inst.Password, inst.Host, port, inst.DatabaseName, inst.SSLMode)

	return &domain.CredentialsResponse{
		ProjectID:     projectID,
		Host:          inst.Host,
		ReadOnlyHost:  inst.ReadOnlyHost,
		Port:          port,
		DatabaseName:  inst.DatabaseName,
		Username:      inst.Username,
		Password:      inst.Password,
		SSLMode:       inst.SSLMode,
		ConnectionURL: connURL,
	}, nil
}

func (s *ProvisioningService) SetDeletionProtection(projectID string, enabled bool) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}
	inst.DeletionProtection = &enabled
	return s.store.Save(inst)
}

func (s *ProvisioningService) createProjectRoles(ctx context.Context, req domain.ProvisioningRequest, result *provisioner.ProvisioningResult, namespace string, pc *provisioner.ProvisionContext) error {
	pc.SetStage(domain.StageRoleCreation)
	projectID := req.ProjectName
	// Pod name = projectID (already DNS-1123 safe since ref uses hyphen)
	primaryPod := projectID + "-postgres-1"
	host := result.Host
	port := strconv.Itoa(result.Port)
	dbName := result.DatabaseName

	orgID := req.OrgID

	// Resolve org slug for vault paths (orgSlug/projectName is the unique key)
	orgSlug := orgID // fallback to orgID if slug lookup fails
	if s.orgStore != nil {
		if org, err := s.orgStore.FindOrgByID(ctx, orgID); err == nil && org != nil {
			orgSlug = org.Slug
		}
	}

	vaultPath := func(role string) string {
		return fmt.Sprintf("projects/%s/%s/credentials/%s", orgSlug, projectID, role)
	}
	registerVaultCleanup := func(path string) {
		pc.RegisterCleanup("delete vault "+path, func(ctx context.Context) error {
			return s.vault.Delete(path)
		})
	}

	// Store admin (superuser) credentials from CNPG
	pc.SetStep("store admin credentials")
	adminPath := vaultPath("admin")
	creds_admin := map[string]string{"host": host, "port": port, "database": dbName, "username": result.Username, "password": result.Password}
	if err := s.vault.Put(adminPath, creds_admin); err != nil {
		return pc.Fail(fmt.Errorf("vault put admin: %w", err))
	}
	registerVaultCleanup(adminPath)

	// Generate passwords for each role
	authPass := generatePassword(32)
	appPass := req.AppPassword
	if appPass == "" {
		appPass = generatePassword(32)
	}
	watcherPass := generatePassword(32)

	roleSQL := BuildProjectRoleSQL(authPass, appPass, watcherPass, dbName, s.publicationName)

	// Execute psql inside the database container (K8s pod or Docker container).
	pc.SetStep("exec CREATE ROLE in database")
	cmd := []string{"psql", "-U", "postgres", "-d", dbName, "-c", roleSQL}
	var execErr error
	if s.dockerClient != nil {
		// Docker mode: the container ID is stored in the instance's Namespace field
		// (updated by finalizeProvisioning from result.Namespace).
		exitCode, err := s.dockerClient.ExecInContainer(ctx, namespace, cmd)
		if err != nil {
			execErr = fmt.Errorf("exec role creation (docker): %w", err)
		} else if exitCode != 0 {
			execErr = fmt.Errorf("exec role creation (docker): psql exit code %d", exitCode)
		}
	} else if s.k8sClient != nil {
		var output string
		output, execErr = s.k8sClient.ExecInPod(ctx, namespace, primaryPod, "postgres", cmd)
		if execErr != nil {
			execErr = fmt.Errorf("exec role creation (k8s): %w (output: %s)", execErr, output)
		}
	}
	if execErr != nil {
		return pc.Fail(execErr)
	}

	// Store credentials in vault at projects/{orgSlug}/{projectName}/credentials/{role}
	pc.SetStep("store auth_admin credentials")
	authPath := vaultPath("auth_admin")
	creds_auth := map[string]string{"host": host, "port": port, "database": dbName, "username": "auth_admin", "password": authPass}
	if err := s.vault.Put(authPath, creds_auth); err != nil {
		return pc.Fail(fmt.Errorf("vault put auth_admin: %w", err))
	}
	registerVaultCleanup(authPath)

	pc.SetStep("store excalibase_app credentials")
	appPath := vaultPath("excalibase_app")
	creds_app := map[string]string{"host": host, "port": port, "database": dbName, "username": "excalibase_app", "password": appPass}
	if err := s.vault.Put(appPath, creds_app); err != nil {
		return pc.Fail(fmt.Errorf("vault put excalibase_app: %w", err))
	}
	registerVaultCleanup(appPath)

	// Watcher daemon reads from this path. Distinct from excalibase_app's
	// credentials because cdc_watcher carries the REPLICATION attribute and
	// must not be reachable from any user-facing service.
	pc.SetStep("store cdc_watcher credentials")
	watcherPath := vaultPath("cdc_watcher")
	creds_watcher := map[string]string{"host": host, "port": port, "database": dbName, "username": "cdc_watcher", "password": watcherPass}
	if err := s.vault.Put(watcherPath, creds_watcher); err != nil {
		return pc.Fail(fmt.Errorf("vault put cdc_watcher: %w", err))
	}
	registerVaultCleanup(watcherPath)

	log.Printf("Created project roles for %s and stored in vault", projectID)
	return nil
}

func boolPtr(b bool) *bool  { return &b }
func intPtr(i int) *int     { return &i }

package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vault"
)

type ProvisioningService struct {
	store     storage.InstanceStore
	orgStore  storage.OrgStore      // optional, for org slug lookup
	factory   *provisioner.Factory
	hooks     *edgefn.HookService  // optional
	vault     *vault.Vault         // optional
	k8sClient k8s.KubeClient      // optional, for role creation via pod exec
	pgdog     *PgDogNotifier       // optional, for PgDog config registration
}

func NewProvisioningService(store storage.InstanceStore, factory *provisioner.Factory, k8sClient k8s.KubeClient) *ProvisioningService {
	return &ProvisioningService{store: store, factory: factory, k8sClient: k8sClient}
}

func (s *ProvisioningService) SetHookService(hooks *edgefn.HookService) {
	s.hooks = hooks
}

func (s *ProvisioningService) SetVault(v *vault.Vault) {
	s.vault = v
}

func (s *ProvisioningService) SetPgDogNotifier(n *PgDogNotifier) {
	s.pgdog = n
}

func (s *ProvisioningService) SetOrgStore(os storage.OrgStore) {
	s.orgStore = os
}

func (s *ProvisioningService) Provision(ctx context.Context, req domain.ProvisioningRequest) (*domain.ProvisioningResponse, error) {
	inst, prov, tier, err := s.prepareProvisioning(req)
	if err != nil {
		return nil, err
	}

	if err := s.store.Save(inst); err != nil {
		log.Printf("WARN: failed to persist instance state: %v", err)
	}

	cb := s.stageCallback(inst)

	if s.hooks != nil {
		s.hooks.ExecuteHooksAsync(ctx, "pre-provision", edgefn.HookContext{
			ProjectID: req.ProjectName, OrgID: req.OrgID,
			DatabaseType: string(req.DBType), Tier: string(req.Tier),
		}, nil)
	}

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

	result, err := prov.Provision(ctx, req, tier, cb)
	if err != nil {
		return s.handleProvisionFailure(inst, req, err), nil
	}

	return s.finalizeProvisioning(ctx, inst, req, result)
}

// prepareProvisioning validates the request and creates the initial instance record.
func (s *ProvisioningService) prepareProvisioning(req domain.ProvisioningRequest) (*domain.DatabaseInstance, provisioner.DatabaseProvisioner, config.TierConfig, error) {
	existing, _ := s.store.FindByProjectID(req.ProjectName)
	if existing != nil {
		return nil, nil, config.TierConfig{}, fmt.Errorf("project ID already exists: %s", req.ProjectName)
	}

	tier, err := config.GetTierConfig(req.Tier)
	if err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	// Enforce max projects per org for this tier
	if tier.MaxProjects > 0 {
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
	if req.Backup != nil && req.Backup.Enabled && !tier.BackupEnabled {
		return nil, nil, config.TierConfig{}, fmt.Errorf("backups are not available on %s tier", req.Tier)
	}

	prov, ok := s.factory.Get(req.DBType)
	if !ok {
		return nil, nil, config.TierConfig{}, fmt.Errorf("unsupported database type: %s", req.DBType)
	}

	now := &domain.FlexTime{Time: time.Now()}
	namespace := fmt.Sprintf("%s-%s", req.OrgID, req.ProjectName)
	inst := &domain.DatabaseInstance{
		ProjectID:    req.ProjectName,
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

// stageCallback returns a callback that updates instance stage in the store.
func (s *ProvisioningService) stageCallback(inst *domain.DatabaseInstance) func(domain.ProvisioningStage) {
	return func(stage domain.ProvisioningStage) {
		inst.CurrentStage = stage
		inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
		if err := s.store.Save(inst); err != nil {
			log.Printf("WARN: failed to persist instance state: %v", err)
		}
	}
}

// handleProvisionFailure persists the failure state and returns an error response.
func (s *ProvisioningService) handleProvisionFailure(inst *domain.DatabaseInstance, req domain.ProvisioningRequest, err error) *domain.ProvisioningResponse {
	inst.Status = "FAILED"
	inst.CurrentStage = domain.StageFailed
	inst.FailureReason = err.Error()
	if saveErr := s.store.Save(inst); saveErr != nil {
		log.Printf("WARN: failed to persist instance state: %v", saveErr)
	}
	return &domain.ProvisioningResponse{
		ProjectID:     req.ProjectName,
		Status:        "FAILED",
		CurrentStage:  domain.StageFailed,
		Namespace:     inst.Namespace,
		FailureReason: err.Error(),
		CreatedAt:     inst.CreatedAt,
	}
}

// finalizeProvisioning updates the instance with connection details, creates roles, and fires hooks.
func (s *ProvisioningService) finalizeProvisioning(ctx context.Context, inst *domain.DatabaseInstance, req domain.ProvisioningRequest, result *provisioner.ProvisioningResult) (*domain.ProvisioningResponse, error) {
	port := result.Port
	inst.Host = result.Host
	inst.ReadOnlyHost = result.ReadOnlyHost
	inst.Port = &port
	inst.DatabaseName = result.DatabaseName
	inst.Username = result.Username
	inst.Password = result.Password
	inst.SSLMode = result.SSLMode
	inst.Status = "ACTIVE"
	inst.CurrentStage = domain.StageCompleted
	inst.MetricsEndpoint = fmt.Sprintf("http://%s-postgres-1.%s.svc.cluster.local:9187/metrics", req.ProjectName, inst.Namespace)

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

	if s.vault != nil && s.k8sClient != nil && !s.vault.Sealed() {
		s.createProjectRoles(ctx, req, result, inst.Namespace)
	}

	// Register with PgDog connection pooler
	if s.pgdog != nil {
		if err := s.pgdog.RegisterCluster(ctx, req.ProjectName, inst.Namespace,
			result.DatabaseName, result.Username, result.Password); err != nil {
			log.Printf("WARN: pgdog register: %v", err)
		}
	}

	if s.hooks != nil {
		s.hooks.ExecuteHooksAsync(ctx, "post-provision", edgefn.HookContext{
			ProjectID: req.ProjectName, OrgID: req.OrgID,
			DatabaseType: string(req.DBType), Tier: string(req.Tier),
			Host: result.Host, Port: result.Port,
			Database: result.DatabaseName, Username: result.Username, Password: result.Password,
		}, nil)
	}

	return &domain.ProvisioningResponse{
		ProjectID:    req.ProjectName,
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

func (s *ProvisioningService) createProjectRoles(ctx context.Context, req domain.ProvisioningRequest, result *provisioner.ProvisioningResult, namespace string) {
	projectID := req.ProjectName
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

	// Store admin (superuser) credentials from CNPG
	// Path: projects/{orgSlug}/{projectName}/credentials/{role}
	creds_admin := map[string]string{"host": host, "port": port, "database": dbName, "username": result.Username, "password": result.Password}
	s.vault.Put(fmt.Sprintf("projects/%s/%s/credentials/admin", orgSlug, projectID), creds_admin)

	// Generate passwords for each role
	authPass := generatePassword(32)
	appPass := req.AppPassword
	if appPass == "" {
		appPass = generatePassword(32)
	}

	// SQL to create roles and schemas
	// Use QuoteIdent for role names in GRANT statements and QuoteLiteral for passwords.
	// Inside DO blocks, we pass the password as a Go-escaped literal to PG's format()
	// with %L, which safely re-quotes it for the EXECUTE'd CREATE ROLE statement.
	authRole := schema.QuoteIdent("auth_admin")
	appRole := schema.QuoteIdent("excalibase_app")
	safeAuthPass := schema.QuoteLiteral(authPass)
	safeAppPass := schema.QuoteLiteral(appPass)

	roleSQL := fmt.Sprintf(`
CREATE SCHEMA IF NOT EXISTS auth;

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'auth_admin') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN PASSWORD %%L', %s::text);
  END IF;
END $$;
GRANT ALL ON SCHEMA auth TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth GRANT ALL ON TABLES TO %s;

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'excalibase_app') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN PASSWORD %%L', %s::text);
  END IF;
END $$;
GRANT ALL ON SCHEMA public TO %s;
GRANT ALL ON ALL TABLES IN SCHEMA public TO %s;
GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO %s;
GRANT USAGE ON SCHEMA auth TO %s;
GRANT SELECT ON ALL TABLES IN SCHEMA auth TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth GRANT SELECT ON TABLES TO %s;
`,
		authRole, safeAuthPass, // DO block: format(%L) safely quotes the password
		authRole, authRole, // GRANT auth
		appRole, safeAppPass, // DO block for excalibase_app
		appRole, appRole, appRole, appRole, appRole, // GRANT public
		appRole, appRole, appRole, // GRANT auth usage
	)

	// Execute via pod exec (psql)
	// Use postgres superuser via local socket (peer auth) to create roles
	cmd := []string{"psql", "-U", "postgres", "-d", dbName, "-c", roleSQL}
	output, err := s.k8sClient.ExecInPod(ctx, namespace, primaryPod, "postgres", cmd)
	if err != nil {
		log.Printf("WARN: role creation failed for %s: %v\nOutput: %s", projectID, err, output)
		return
	}

	// Store credentials in vault at projects/{orgSlug}/{projectName}/credentials/{role}
	creds_auth := map[string]string{"host": host, "port": port, "database": dbName, "username": "auth_admin", "password": authPass}
	creds_app := map[string]string{"host": host, "port": port, "database": dbName, "username": "excalibase_app", "password": appPass}

	s.vault.Put(fmt.Sprintf("projects/%s/%s/credentials/auth_admin", orgSlug, projectID), creds_auth)
	s.vault.Put(fmt.Sprintf("projects/%s/%s/credentials/excalibase_app", orgSlug, projectID), creds_app)

	log.Printf("Created project roles for %s and stored in vault", projectID)
}

func boolPtr(b bool) *bool  { return &b }
func intPtr(i int) *int     { return &i }

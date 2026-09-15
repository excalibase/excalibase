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
	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"k8s.io/apimachinery/pkg/api/resource"
)

const warnPersistFmt = "WARN: failed to persist instance state: %v"

type ProvisioningService struct {
	store          storage.InstanceStore
	orgStore       storage.OrgStore        // optional, for org slug lookup
	tierStore      storage.TierConfigStore // optional; DB-backed tier specs, falls back to config defaults
	factory        *provisioner.Factory
	vault          vaultclient.VaultClient  // optional
	k8sClient      k8s.KubeClient           // optional, for role creation via pod exec
	dockerClient   provisioner.DockerClient // optional, for role creation via container exec (Docker mode)
	pgdog          *PgDogNotifier           // optional, for PgDog config registration
	selfHostedMode bool                     // skip tier enforcement
	// publicationName is the CDC publication created during role setup.
	// Must match watcher's publication_name config and graphql's
	// app.realtime.publication-name. Empty defaults to "cdc_watcher_pub".
	publicationName string
	// capacityHeadroomPercent is the % of node Allocatable held back as a
	// safety buffer when planning provisions. 0 means plan against full
	// Allocatable. Set via SetCapacityHeadroom from main.go config.
	capacityHeadroomPercent int

	// lokiURL is the cluster's Loki HTTP endpoint. When set, GetLogs
	// queries it instead of running kubectl-exec tail (the legacy path
	// hangs on busy clusters and only sees logs since pod start).
	lokiURL string

	// backupDefaults configures the platform-wide backup target. When
	// set, every new project gets backup enabled by default with these
	// credentials, unless the request explicitly overrides via
	// req.Backup. Wired from R2 creds in main.go so backups land in
	// Cloudflare R2 instead of the legacy floci/localstack mock.
	backupDefaults *BackupDefaults

	// backupPurger deletes a project's backup objects on request at
	// deprovision time. nil means confirmDeleteBackups is refused.
	backupPurger *BackupPurger

	// defaultDeploymentMode stamps inst.DeploymentMode at provision time
	// for k8s + docker pipelines (BYOC sets its own). Empty falls back
	// to ModeK8s so legacy callers keep their existing behaviour.
	defaultDeploymentMode domain.DeploymentMode
}

// BackupDefaults — platform-wide CNPG backup target. All four fields are
// required for the defaults to apply; an incomplete config is ignored
// (provisions land without backup, matching pre-v1.1 behaviour).
type BackupDefaults struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string // e.g. https://<account_id>.r2.cloudflarestorage.com
	Bucket          string // e.g. excalibase-backups
	Region          string // R2 ignores; SDK requires non-empty. Default "auto".
	Schedule        string // cron; default "0 0 2 * * *"
	RetentionDays   int    // default 7
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

// SetTierStore wires the DB-backed tier-config source. Optional: when unset
// (or a tier row is missing), tier resolution falls back to config.GetTierConfig.
func (s *ProvisioningService) SetTierStore(ts storage.TierConfigStore) {
	s.tierStore = ts
}

// tierConfig resolves a tier's resource spec, preferring the DB-backed store
// (so admin edits take effect without a redeploy) and falling back to the
// hardcoded config defaults when the store is absent, errors, or has no row.
func (s *ProvisioningService) tierConfig(ctx context.Context, tier domain.TierType) (config.TierConfig, error) {
	if s.tierStore != nil {
		if tc, ok, err := s.tierStore.GetTierConfig(ctx, tier); err == nil && ok {
			return tc, nil
		}
	}
	return config.GetTierConfig(tier)
}

// TierConfig is the exported form of tierConfig for readers outside this
// package (the capacity report), so every consumer of tier specs honours admin
// edits in the tier_configs table exactly as admission does.
func (s *ProvisioningService) TierConfig(ctx context.Context, tier domain.TierType) (config.TierConfig, error) {
	return s.tierConfig(ctx, tier)
}

func (s *ProvisioningService) SetSelfHostedMode(enabled bool) {
	s.selfHostedMode = enabled
}

func (s *ProvisioningService) SetDockerClient(dc provisioner.DockerClient) {
	s.dockerClient = dc
}

// SetDefaultDeploymentMode sets the deployment mode written onto every
// new k8s/docker provision. BYOC ignores this — it always sets ModeBYOC.
func (s *ProvisioningService) SetDefaultDeploymentMode(m domain.DeploymentMode) {
	s.defaultDeploymentMode = m
}

// SetCapacityHeadroom sets the % of node Allocatable held back as a safety
// buffer when planning provisions. Allocatable already excludes
// kube-reserved + system-reserved (kubelet does that); this is the extra
// cushion on top — for burst, monitoring growth, brief restart spikes.
// Pass 0 to plan against full Allocatable.
func (s *ProvisioningService) SetCapacityHeadroom(percent int) {
	s.capacityHeadroomPercent = percent
}

// SetLokiURL configures the Loki endpoint used by GetLogs. Empty string
// keeps the legacy kubectl-exec path (works in dev, hangs under load).
func (s *ProvisioningService) SetLokiURL(url string) {
	s.lokiURL = url
}

// SetBackupDefaults wires the platform-wide CNPG backup target. After
// this is called, every new project provisions with backup enabled
// against the configured S3-compatible store (R2 in production), unless
// the provision request explicitly overrides via req.Backup.
//
// Pass nil (or a struct with empty AccessKeyID/Endpoint/Bucket) to
// disable platform-wide backup defaults — projects then provision
// without backup config (matches pre-v1.1 behaviour).
func (s *ProvisioningService) SetBackupDefaults(d *BackupDefaults) {
	if d == nil || d.AccessKeyID == "" || d.SecretAccessKey == "" || d.Endpoint == "" || d.Bucket == "" {
		s.backupDefaults = nil
		return
	}
	if d.Region == "" {
		d.Region = "auto"
	}
	if d.Schedule == "" {
		d.Schedule = "0 0 2 * * *"
	}
	if d.RetentionDays == 0 {
		d.RetentionDays = 7
	}
	s.backupDefaults = d
}

// CapacityHeadroom returns the configured % held back as a safety buffer.
// Exposed so the /api/capacity handler reports the same number it plans
// against, and so tests can assert the policy applied correctly.
func (s *ProvisioningService) CapacityHeadroom() int {
	return s.capacityHeadroomPercent
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

	// Store credentials in vault under the project ref. Vault paths are
	// project-scoped only — see `projects/{projectId}/...` scheme. Errors are
	// fatal: no point creating an ACTIVE instance row pointing at credentials
	// the platform can't read back.
	if s.vault != nil {
		port := strconv.Itoa(req.Port)
		creds := map[string]string{
			"host":     req.Host,
			"port":     port,
			"username": req.Username,
			"password": req.Password,
			"database": req.Database,
		}
		if err := s.vault.Put(vaultCredentialPath(projectRef, "excalibase_app"), creds); err != nil {
			return nil, fmt.Errorf("vault put excalibase_app: %w", err)
		}
		if err := s.vault.Put(vaultCredentialPath(projectRef, "admin"), creds); err != nil {
			return nil, fmt.Errorf("vault put admin: %w", err)
		}
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

	log.Printf("BYOC project registered: %s (ref=%s, host=%s)", req.ProjectName, projectRef, req.Host)

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
	// Pass req by pointer so prepareProvisioning's defaults (e.g. R2
	// backup config injected when req.Backup is nil) propagate to the
	// downstream prov.Provision call. Otherwise the mutated copy is
	// scoped to the helper and the cluster comes up without backup.
	start := time.Now()
	inst, prov, tier, err := s.prepareProvisioning(ctx, &req)
	if err != nil {
		metrics.ObserveProvision(start, err)
		return nil, err
	}

	// From this point on, req.ProjectName is replaced with the generated opaque
	// ref so the provisioner and all downstream callers use the K8s-safe ID.
	// The user's display name is preserved on inst.ProjectName.
	req.ProjectName = inst.ProjectID

	if err := s.store.Save(inst); err != nil {
		log.Printf(warnPersistFmt, err)
	}

	pc := provisioner.NewProvisionContext(
		func(stage domain.ProvisioningStage) {
			inst.CurrentStage = stage
			inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
			if err := s.store.Save(inst); err != nil {
				log.Printf(warnPersistFmt, err)
			}
		},
		func(step string) {
			inst.CurrentStep = step
			if err := s.store.Save(inst); err != nil {
				log.Printf(warnPersistFmt, err)
			}
		},
	)

	// Populate S3 credentials from vault if backup enabled but no S3 creds provided
	if req.Backup != nil && req.Backup.Enabled && req.Backup.S3 == nil {
		if s3Creds, ok := s.vaultBackupStorage(); ok {
			req.Backup.S3 = s3Creds
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
		metrics.ObserveProvision(start, provErr)
		return s.handleProvisionFailure(ctx, inst, req, provErr, pc), nil
	}

	resp, ferr := s.finalizeProvisioning(ctx, inst, req, result, pc)
	metrics.ObserveProvision(start, ferr)
	return resp, ferr
}

// prepareProvisioning validates the request and creates the initial instance record.
func (s *ProvisioningService) prepareProvisioning(ctx context.Context, req *domain.ProvisioningRequest) (*domain.DatabaseInstance, provisioner.DatabaseProvisioner, config.TierConfig, error) {
	if err := validateProvisioningRequest(*req); err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	projectRef, err := s.generateUniqueProjectRef()
	if err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	tier, err := s.tierConfig(ctx, req.Tier)
	if err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	if err := s.enforceOrgProjectLimit(req.OrgID, tier, req.Tier); err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	s.applyBackupDefaults(req, tier)

	if err := s.enforceBackupTierPolicy(req, tier); err != nil {
		return nil, nil, config.TierConfig{}, err
	}

	if s.k8sClient != nil {
		if err := s.checkClusterCapacity(tier); err != nil {
			return nil, nil, config.TierConfig{}, err
		}
	}

	prov, ok := s.factory.Get(req.DBType)
	if !ok {
		return nil, nil, config.TierConfig{}, fmt.Errorf("unsupported database type: %s", req.DBType)
	}

	now := &domain.FlexTime{Time: time.Now()}
	namespace := fmt.Sprintf("%s-%s", req.OrgID, projectRef)
	mode := s.defaultDeploymentMode
	if mode == "" {
		mode = domain.ModeK8s
	}
	inst := &domain.DatabaseInstance{
		ProjectID:      projectRef,
		ProjectName:    req.ProjectName,
		OrgID:          req.OrgID,
		OwnerID:        req.OwnerID,
		DBType:         req.DBType,
		Tier:           req.Tier,
		DeploymentMode: mode,
		Namespace:      namespace,
		Status:         "PROVISIONING",
		CurrentStage:   domain.StageValidating,
		CreatedAt:      now,
	}

	if req.Backup != nil {
		inst.BackupEnabled = boolPtr(req.Backup.Enabled)
		inst.BackupSchedule = req.Backup.Schedule
		inst.BackupRetentionDays = intPtr(req.Backup.Retention)
	}

	return inst, prov, tier, nil
}

// generateUniqueProjectRef retries up to 5 times to find a collision-free project ref.
func (s *ProvisioningService) generateUniqueProjectRef() (string, error) {
	for i := 0; i < 5; i++ {
		ref := generateProjectRef()
		if existing, _ := s.store.FindByProjectID(ref); existing == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique project ref after 5 attempts")
}

// enforceOrgProjectLimit checks whether the org has capacity for another project under the given tier.
func (s *ProvisioningService) enforceOrgProjectLimit(orgID string, tier config.TierConfig, tierType domain.TierType) error {
	if tier.MaxProjects <= 0 || s.selfHostedMode {
		return nil
	}
	allInstances, _ := s.store.FindAll()
	orgCount := 0
	for _, inst := range allInstances {
		if inst.OrgID == orgID {
			orgCount++
		}
	}
	if orgCount >= tier.MaxProjects {
		return fmt.Errorf("org %s has reached the maximum of %d projects for %s tier", orgID, tier.MaxProjects, tierType)
	}
	return nil
}

// applyBackupDefaults injects platform-wide backup defaults when the request has no backup config.
func (s *ProvisioningService) applyBackupDefaults(req *domain.ProvisioningRequest, tier config.TierConfig) {
	if req.Backup != nil || s.backupDefaults == nil || !tier.BackupEnabled {
		return
	}
	req.Backup = &domain.BackupSettings{
		Enabled:   true,
		Schedule:  s.backupDefaults.Schedule,
		Retention: s.backupDefaults.RetentionDays,
		S3: &domain.S3Credentials{
			AccessKeyID:     s.backupDefaults.AccessKeyID,
			SecretAccessKey: s.backupDefaults.SecretAccessKey,
			Bucket:          s.backupDefaults.Bucket,
			Region:          s.backupDefaults.Region,
			Endpoint:        s.backupDefaults.Endpoint,
		},
	}
}

// BackupStorage resolves the object store new backups are written to, in
// the same order Provision applies it: platform defaults first (they fill
// req.Backup.S3 before the vault lookup runs), then vault backup/s3. The
// restore adapter reads through this so it can never target a different
// store than the one the backup landed in.
func (s *ProvisioningService) BackupStorage() (*domain.S3Credentials, bool) {
	if d := s.backupDefaults; d != nil {
		return &domain.S3Credentials{
			AccessKeyID:     d.AccessKeyID,
			SecretAccessKey: d.SecretAccessKey,
			Bucket:          d.Bucket,
			Region:          d.Region,
			Endpoint:        d.Endpoint,
		}, true
	}
	return s.vaultBackupStorage()
}

// vaultBackupStorage reads vault backup/s3 when the vault is wired and unsealed.
func (s *ProvisioningService) vaultBackupStorage() (*domain.S3Credentials, bool) {
	if s.vault == nil || s.vault.Sealed() {
		return nil, false
	}
	s3Creds, err := s.vault.Get("backup/s3")
	if err != nil || s3Creds == nil {
		return nil, false
	}
	return &domain.S3Credentials{
		AccessKeyID:     s3Creds["accessKeyId"],
		SecretAccessKey: s3Creds["secretAccessKey"],
		Bucket:          s3Creds["bucket"],
		Region:          s3Creds["region"],
		Endpoint:        s3Creds["endpoint"],
	}, true
}

// enforceBackupTierPolicy rejects backup requests on tiers that do not support backup.
func (s *ProvisioningService) enforceBackupTierPolicy(req *domain.ProvisioningRequest, tier config.TierConfig) error {
	if req.Backup != nil && req.Backup.Enabled && !tier.BackupEnabled && !s.selfHostedMode {
		return fmt.Errorf("backups are not available on %s tier", req.Tier)
	}
	return nil
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
		log.Printf(warnPersistFmt, saveErr)
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
	var engineRoles *projectRoleCredentials
	if s.vault != nil && canExecSQL && !s.vault.Sealed() {
		creds := newProjectRoleCredentials(req.AppPassword)
		if err := s.createProjectRoles(ctx, req, result, inst.Namespace, pc, creds); err != nil {
			return s.handleProvisionFailure(ctx, inst, req, err, pc), nil
		}
		engineRoles = &creds
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
		log.Printf(warnPersistFmt, err)
	}

	s.registerWithPgDog(ctx, req.ProjectName, inst.Namespace, result.DatabaseName, engineRoles)

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

var (
	// ErrProjectNotFound is returned when the project row does not exist.
	ErrProjectNotFound = errors.New("project not found")
	// ErrBackupPurgeNotConfigured is returned when a caller asks to delete
	// backups but no purger is wired; nothing is deprovisioned in that case.
	ErrBackupPurgeNotConfigured = errors.New("backup purge is not configured on this platform")
	// ErrBackupsNotPendingDelete guards the retry endpoint: only a row left
	// in BACKUPS_PENDING_DELETE by a deprovision may have its backups purged.
	ErrBackupsNotPendingDelete = errors.New("project is not pending backup deletion; deprovision it with confirmDeleteBackups first")
)

// DeprovisionOptions tunes what Deprovision removes beyond the project.
type DeprovisionOptions struct {
	// DeleteBackups purges the project's backup objects once its resources
	// are gone. Default false: backups outlive the project.
	DeleteBackups bool
}

// SetBackupPurger wires the object-store purge used when a deprovision asks
// for its backups to be deleted.
func (s *ProvisioningService) SetBackupPurger(p *BackupPurger) { s.backupPurger = p }

// Deprovision removes the project and keeps its backups.
func (s *ProvisioningService) Deprovision(ctx context.Context, projectID string) error {
	return s.DeprovisionWithOptions(ctx, projectID, DeprovisionOptions{})
}

// DeprovisionWithOptions tears the project down: pooler, cluster/container,
// vault paths, then (when asked) its backup objects, then the store row. A
// failed purge never undoes the deprovision — the row is kept with status
// BACKUPS_PENDING_DELETE so PurgeBackups can retry it.
func (s *ProvisioningService) DeprovisionWithOptions(ctx context.Context, projectID string, opts DeprovisionOptions) error {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if inst.DeletionProtection != nil && *inst.DeletionProtection {
		return fmt.Errorf("deletion protection is enabled for %s", projectID)
	}
	if opts.DeleteBackups && s.backupPurger == nil {
		return ErrBackupPurgeNotConfigured
	}

	s.releaseProjectResources(ctx, inst, projectID)

	if opts.DeleteBackups && !s.purgeBackupsOrMark(ctx, inst) {
		return nil
	}
	return s.store.Delete(projectID)
}

// releaseProjectResources frees everything the project holds outside the
// store row. Every step is best-effort: an outage logs and proceeds rather
// than stranding the instance row.
func (s *ProvisioningService) releaseProjectResources(ctx context.Context, inst *domain.DatabaseInstance, projectID string) {
	if s.pgdog != nil {
		if err := s.pgdog.DeregisterCluster(ctx, projectID); err != nil {
			log.Printf("WARN: pgdog deregister: %v", err)
		}
	}

	if prov, ok := s.factory.Get(inst.DBType); ok {
		if err := prov.Deprovision(ctx, inst.Namespace, projectID); err != nil {
			log.Printf("WARN: K8s deprovision failed for %s: %v", projectID, err)
		}
	}

	// Delete every vault path scoped to this project. Critical for BYOC where
	// the credentials are live passwords on an externally managed database —
	// without this the platform retains them indefinitely after the project
	// row is gone.
	if s.vault != nil && !s.vault.Sealed() {
		s.deleteProjectVaultPaths(ctx, inst, projectID)
	}
}

// purgeBackupsOrMark deletes the project's backup objects and reports whether
// the store row may now be removed. On failure the row is turned into a
// BACKUPS_PENDING_DELETE marker instead.
func (s *ProvisioningService) purgeBackupsOrMark(ctx context.Context, inst *domain.DatabaseInstance) bool {
	deleted, err := s.backupPurger.Purge(ctx, inst)
	if errors.Is(err, ErrNoBackupsForMode) {
		log.Printf("backup purge skipped for %s: %v", inst.ProjectID, err)
		return true
	}
	if err != nil {
		log.Printf("WARN: backup purge failed for %s, row kept as %s: %v", inst.ProjectID, domain.StatusBackupsPendingDelete, err)
		s.markBackupsPendingDelete(inst, err)
		return false
	}
	log.Printf("backup purge for %s deleted %d objects", inst.ProjectID, deleted)
	return true
}

func (s *ProvisioningService) markBackupsPendingDelete(inst *domain.DatabaseInstance, cause error) {
	inst.Status = string(domain.StatusBackupsPendingDelete)
	inst.CurrentStage = domain.StatusBackupsPendingDelete
	inst.FailureReason = "backup purge failed: " + cause.Error()
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	if err := s.store.Save(inst); err != nil {
		log.Printf(warnPersistFmt, err)
	}
}

// PurgeBackups retries the backup deletion for a row left in
// BACKUPS_PENDING_DELETE and removes the row once the prefix is clean. Live
// projects are refused so this can never be used to wipe a running
// project's backups. Returns the number of objects deleted.
func (s *ProvisioningService) PurgeBackups(ctx context.Context, projectID string) (int, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return 0, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	if inst.Status != string(domain.StatusBackupsPendingDelete) {
		return 0, ErrBackupsNotPendingDelete
	}
	if s.backupPurger == nil {
		return 0, ErrBackupPurgeNotConfigured
	}
	deleted, err := s.backupPurger.Purge(ctx, inst)
	if err != nil {
		s.markBackupsPendingDelete(inst, err)
		return deleted, err
	}
	log.Printf("backup purge retry for %s deleted %d objects", projectID, deleted)
	return deleted, s.store.Delete(projectID)
}

func (s *ProvisioningService) deleteProjectVaultPaths(ctx context.Context, inst *domain.DatabaseInstance, projectID string) {
	// One vault round-trip; one storage transaction. Path is project-scoped
	// only — no orgSlug guessing.
	prefix := vaultProjectPrefix(projectID)
	if _, err := s.vault.DeletePrefix(prefix); err != nil {
		log.Printf("WARN: vault delete prefix %s: %v", prefix, err)
	}
}

// vaultProjectPrefix is the canonical prefix for everything stored under a
// project. All credential / secret paths sit under this prefix.
func vaultProjectPrefix(projectID string) string {
	return fmt.Sprintf("projects/%s/", projectID)
}

// vaultCredentialPath builds the canonical credential path. role is e.g.
// "admin", "excalibase_app", "auth_admin", "cdc_watcher", "jwt_keys/anon_token".
func vaultCredentialPath(projectID, role string) string {
	return fmt.Sprintf("projects/%s/credentials/%s", projectID, role)
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

// execRoleSQL executes a psql command in the appropriate container (Docker or K8s).
func (s *ProvisioningService) execRoleSQL(ctx context.Context, namespace, primaryPod string, cmd []string) error {
	if s.dockerClient != nil {
		exitCode, err := s.dockerClient.ExecInContainer(ctx, namespace, cmd)
		if err != nil {
			return fmt.Errorf("exec role creation (docker): %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("exec role creation (docker): psql exit code %d", exitCode)
		}
		return nil
	}
	if s.k8sClient != nil {
		output, err := s.k8sClient.ExecInPod(ctx, namespace, primaryPod, "postgres", cmd)
		if err != nil {
			return fmt.Errorf("exec role creation (k8s): %w (output: %s)", err, output)
		}
	}
	return nil
}

// projectRoleCredentials holds the generated passwords for the roles
// createProjectRoles creates. Generated by the caller so the engine-facing
// pair can be handed to PgDog without re-reading the vault.
type projectRoleCredentials struct {
	authPassword    string
	appPassword     string
	watcherPassword string
}

func newProjectRoleCredentials(requestedAppPassword string) projectRoleCredentials {
	appPassword := requestedAppPassword
	if appPassword == "" {
		appPassword = generatePassword(32)
	}
	return projectRoleCredentials{
		authPassword:    generatePassword(32),
		appPassword:     appPassword,
		watcherPassword: generatePassword(32),
	}
}

// pgdogRoles are the engine-facing roles PgDog may route. cdc_watcher is
// excluded because logical replication cannot run through a transaction
// pooler, and the CNPG owner credential is never exposed at all.
func (c projectRoleCredentials) pgdogRoles() []PgDogRole {
	return []PgDogRole{
		{Name: "excalibase_app", Password: c.appPassword},
		{Name: "auth_admin", Password: c.authPassword},
	}
}

// registerWithPgDog exposes the project through the shared pooler. Without
// engine roles there is no least-privileged credential to route, so the
// project is left unreachable via PgDog rather than registered with the
// owner credential.
func (s *ProvisioningService) registerWithPgDog(ctx context.Context, projectID, namespace, dbName string, roles *projectRoleCredentials) {
	if s.pgdog == nil {
		return
	}
	if roles == nil {
		log.Printf("WARN: pgdog register skipped for %s: engine roles were not created", projectID)
		return
	}
	if err := s.pgdog.RegisterCluster(ctx, projectID, namespace, dbName, roles.pgdogRoles()); err != nil {
		log.Printf("WARN: pgdog register: %v", err)
	}
}

func (s *ProvisioningService) createProjectRoles(ctx context.Context, req domain.ProvisioningRequest, result *provisioner.ProvisioningResult, namespace string, pc *provisioner.ProvisionContext, creds projectRoleCredentials) error {
	pc.SetStage(domain.StageRoleCreation)
	projectID := req.ProjectName
	// Pod name = projectID (already DNS-1123 safe since ref uses hyphen)
	primaryPod := projectID + "-postgres-1"
	host := result.Host
	port := strconv.Itoa(result.Port)
	dbName := result.DatabaseName

	// Vault paths are project-scoped only — projectID is globally unique so no
	// org dimension is needed. Eliminates the silent-fallback bug where a
	// failed org lookup could file credentials under the wrong tenant.
	vaultPath := func(role string) string {
		return vaultCredentialPath(projectID, role)
	}
	registerVaultCleanup := func(path string) {
		pc.RegisterCleanup("delete vault "+path, func(ctx context.Context) error {
			return s.vault.Delete(path)
		})
	}

	// Store admin (superuser) credentials from CNPG
	pc.SetStep("store admin credentials")
	adminPath := vaultPath("admin")
	credsAdmin := map[string]string{"host": host, "port": port, "database": dbName, "username": result.Username, "password": result.Password}
	if err := s.vault.Put(adminPath, credsAdmin); err != nil {
		return pc.Fail(fmt.Errorf("vault put admin: %w", err))
	}
	registerVaultCleanup(adminPath)

	authPass, appPass, watcherPass := creds.authPassword, creds.appPassword, creds.watcherPassword

	roleSQL := BuildProjectRoleSQL(authPass, appPass, watcherPass, dbName, s.publicationName)

	// Execute psql inside the database container (K8s pod or Docker container).
	pc.SetStep("exec CREATE ROLE in database")
	cmd := []string{"psql", "-U", "postgres", "-d", dbName, "-c", roleSQL}
	if execErr := s.execRoleSQL(ctx, namespace, primaryPod, cmd); execErr != nil {
		return pc.Fail(execErr)
	}

	// Store credentials in vault at projects/{projectId}/credentials/{role}
	pc.SetStep("store auth_admin credentials")
	authPath := vaultPath("auth_admin")
	credsAuth := map[string]string{"host": host, "port": port, "database": dbName, "username": "auth_admin", "password": authPass}
	if err := s.vault.Put(authPath, credsAuth); err != nil {
		return pc.Fail(fmt.Errorf("vault put auth_admin: %w", err))
	}
	registerVaultCleanup(authPath)

	pc.SetStep("store excalibase_app credentials")
	appPath := vaultPath("excalibase_app")
	credsApp := map[string]string{"host": host, "port": port, "database": dbName, "username": "excalibase_app", "password": appPass}
	if err := s.vault.Put(appPath, credsApp); err != nil {
		return pc.Fail(fmt.Errorf("vault put excalibase_app: %w", err))
	}
	registerVaultCleanup(appPath)

	// Watcher daemon reads from this path. Distinct from excalibase_app's
	// credentials because cdc_watcher carries the REPLICATION attribute and
	// must not be reachable from any user-facing service.
	pc.SetStep("store cdc_watcher credentials")
	watcherPath := vaultPath("cdc_watcher")
	credsWatcher := map[string]string{"host": host, "port": port, "database": dbName, "username": "cdc_watcher", "password": watcherPass}
	if err := s.vault.Put(watcherPath, credsWatcher); err != nil {
		return pc.Fail(fmt.Errorf("vault put cdc_watcher: %w", err))
	}
	registerVaultCleanup(watcherPath)

	log.Printf("Created project roles for %s and stored in vault", projectID)

	// Deploy per-project watcher NOW that cdc_watcher role exists. Done here
	// rather than inside the provisioner because the watcher needs the role
	// (REPLICATION attribute) created by the SQL exec'd above. Inline creds —
	// not the CNPG `app` secret which lacks REPLICATION.
	if pgProv, ok := s.factory.Get(domain.PostgreSQL); ok {
		if pg, ok := pgProv.(*provisioner.PostgreSQLProvisioner); ok {
			if err := pg.DeployWatcher(ctx, namespace, projectID, dbName, "cdc_watcher", watcherPass); err != nil {
				log.Printf("WARN: watcher deployment for %s: %v", projectID, err)
			}
		}
	}

	return nil
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// checkClusterCapacity refuses the provision if the cluster lacks headroom
// for `tier.Instances` postgres pods of (tier.CPU, tier.Memory) plus the
// per-project sidecars (watcher + deno-runtime).
//
// Best-effort: if capacity lookup itself errors (operator unavailable, etc.)
// we log and let the provision proceed — better to fail at WAITING_FOR_READY
// than block on a flaky API call. This is the only place we trade hard
// enforcement for a soft warning; everywhere else upstream errors are fatal.
func (s *ProvisioningService) checkClusterCapacity(tier config.TierConfig) error {
	cap, err := s.k8sClient.GetClusterCapacity(context.Background())
	if err != nil {
		log.Printf("WARN: cluster capacity lookup failed (proceeding anyway): %v", err)
		return nil
	}
	// Stamp the configured headroom % so FreeCPUMilli / FreeMemBytes
	// account for our extra safety buffer on top of kubelet's Allocatable.
	cap.HeadroomPercent = s.capacityHeadroomPercent
	// Allocatable=0 means we couldn't read node status (mock or stale API
	// cache). Treat as "capacity unknown" rather than blocking every
	// provision — the WAITING_FOR_READY timeout still catches real OOMs.
	if cap.AllocatableCPUMilli == 0 || cap.AllocatableMemBytes == 0 {
		return nil
	}

	cpu, mem, err := TierResourceFootprint(tier)
	if err != nil {
		log.Printf("WARN: tier resource parse failed (proceeding anyway): %v", err)
		return nil
	}

	if cap.FreeCPUMilli() < cpu {
		// Detailed numbers go to the server log so operators can size up.
		// End user sees only a short, actionable message — no milli-CPU,
		// no allocatable counts, no signal about cluster sizing.
		log.Printf("INFO: provision refused (CPU): tier=%s need=%dm free=%dm allocatable=%dm requested=%dm headroom=%d%%",
			tier.CPUString(), cpu, cap.FreeCPUMilli(), cap.AllocatableCPUMilli, cap.RequestedCPUMilli, cap.HeadroomPercent)
		return fmt.Errorf("not enough capacity right now — try again later or contact support")
	}
	if cap.FreeMemBytes() < mem {
		log.Printf("INFO: provision refused (memory): tier=%s need=%d free=%d allocatable=%d requested=%d headroom=%d%%",
			tier.MemoryString(), mem, cap.FreeMemBytes(), cap.AllocatableMemBytes, cap.RequestedMemBytes, cap.HeadroomPercent)
		return fmt.Errorf("not enough capacity right now — try again later or contact support")
	}
	return nil
}

// TierResourceFootprint converts a tier's CPU/memory strings + instance count
// into total milli-CPU and bytes the project would request from the cluster.
// Sidecars (watcher 50m/128Mi + deno-runtime 10m/64Mi) are added once per
// project regardless of instance count. Exported so the /api/capacity
// handler can compute per-tier project headroom.
func TierResourceFootprint(tier config.TierConfig) (cpuMilli, memBytes int64, err error) {
	pgCPU, err := resource.ParseQuantity(tier.CPU)
	if err != nil {
		return 0, 0, fmt.Errorf("parse tier cpu %q: %w", tier.CPU, err)
	}
	pgMem, err := resource.ParseQuantity(tier.Memory)
	if err != nil {
		return 0, 0, fmt.Errorf("parse tier memory %q: %w", tier.Memory, err)
	}
	instances := int64(tier.Instances)
	if instances < 1 {
		instances = 1
	}
	cpuMilli = pgCPU.MilliValue()*instances + 60       // watcher 50m + deno 10m
	memBytes = pgMem.Value()*instances + 192*1024*1024 // watcher 128Mi + deno 64Mi
	return cpuMilli, memBytes, nil
}

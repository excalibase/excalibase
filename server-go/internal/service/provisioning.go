package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
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
	natsCreds      *NatsCredentialMinter    // optional, for project-scoped NATS credentials (EXC-324)
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

	// objectPurger clears the project's own object-store prefix. Nil when
	// storage is not configured: there is then no blob plane to clear, and
	// the teardown carries no purge step at all.
	objectPurger ProjectObjectPurger
	// backupPurger deletes a project's backup objects on request at
	// deprovision time. nil means confirmDeleteBackups is refused.
	backupPurger *BackupPurger

	// activity seeds the last-seen marker for a newly registered project so
	// it is not a candidate for idle-pause the moment it exists. Optional.
	activity *ActivityRecorder

	// projectEvents announces a newly registered project to the data plane.
	// Optional; nil means no announcement is published.
	projectEvents ProjectEventPublisher

	// credVerifier proves a rotated password opens the project's database.
	// Credential rotation refuses to run without it: an unverified password
	// is not evidence of anything.
	credVerifier RoleCredentialVerifier

	// deletionClaimer grants one teardown at a time per project. Lazily set
	// to the in-process claimer; multi-replica deployments wire the
	// advisory-lock one so the claim holds across them.
	deletionClaimer ProjectOperationClaimer
	claimerOnce     sync.Once
	// deletionObservers are told the moment a project is claimed for
	// teardown, so in-process caches of "is this project still live" stop
	// serving it without waiting for their own expiry. Optional.
	deletionObservers []DeletionObserver

	// defaultDeploymentMode stamps inst.DeploymentMode at provision time
	// for the k8s + docker pipelines. Empty falls back
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

// SetNatsCredentialMinter enables project-scoped NATS credentials. Nil keeps
// watchers unauthenticated, which only works without auth_callout.
func (s *ProvisioningService) SetNatsCredentialMinter(m *NatsCredentialMinter) {
	s.natsCreds = m
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
// new k8s/docker provision.
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

	// The row is the project's slot: creating it is what admits the project
	// to the organisation, and it happens before any cluster resource exists.
	if err := s.createProjectRow(ctx, inst, req.Tier); err != nil {
		return nil, err
	}

	// A teardown that claims the project mid-build owns it from that moment.
	// The pipeline must stop rather than keep creating resources the
	// teardown has already looked for and not found: it cancels the
	// provision context, and the provisioner's next call to the cluster
	// returns. Stage and step persistence is where the pipeline learns this,
	// because the store refuses those writes once the door is closed.
	ctx, abort := context.WithCancelCause(ctx)
	defer abort(nil)
	persist := func() {
		if err := s.store.Update(inst); err != nil {
			if isProjectGone(err) {
				log.Printf("provisioning of %s stops: %v", inst.ProjectID, err)
				abort(err)
				return
			}
			log.Printf(warnPersistFmt, err)
		}
	}
	pc := provisioner.NewProvisionContext(
		func(stage domain.ProvisioningStage) {
			inst.CurrentStage = stage
			inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
			persist()
		},
		func(step string) {
			inst.CurrentStep = step
			persist()
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
	// A cancelled context outranks whatever the provisioner reported: the
	// project stopped being ours to build, so nothing it produced may be
	// registered. handleProvisionFailure runs the compensations.
	if cause := context.Cause(ctx); cause != nil && isProjectGone(cause) {
		metrics.ObserveProvision(start, cause)
		return s.handleProvisionFailure(context.WithoutCancel(ctx), inst, req, cause, pc), nil
	}
	if provErr != nil {
		metrics.ObserveProvision(start, provErr)
		return s.handleProvisionFailure(context.WithoutCancel(ctx), inst, req, provErr, pc), nil
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

	// The organisation's limit is answered before anything about the cluster
	// is looked at, so a caller who has used up their projects is told that
	// rather than about capacity they are not asking for. The slot is still
	// taken atomically at insert time; this only fixes which refusal wins.
	if err := s.EnsureOrgProjectCapacity(ctx, req.OrgID, req.Tier); err != nil {
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

// generateUniqueProjectRef returns a project id no registered project holds.
func (s *ProvisioningService) generateUniqueProjectRef() (string, error) {
	return allocateProjectID(s.store)
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
	if saveErr := s.store.Update(inst); saveErr != nil {
		// A refusal here is the expected outcome when a teardown claimed the
		// project: the row is its to write now, and the compensations above
		// have already undone what this pipeline built. Nothing else to do.
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

// finalizeProvisioning stamps the provisioner's connection details onto the
// instance and hands it to the shared registration path — the same one a
// restore ends with.
func (s *ProvisioningService) finalizeProvisioning(ctx context.Context, inst *domain.DatabaseInstance, req domain.ProvisioningRequest, result *provisioner.ProvisioningResult, pc *provisioner.ProvisionContext) (*domain.ProvisioningResponse, error) {
	applyProvisioningResult(inst, req, result)

	opts := RegistrationOptions{AppPassword: req.AppPassword, Context: pc, RowAlreadyCreated: true}
	if err := s.RegisterProject(ctx, inst, opts); err != nil {
		return s.handleProvisionFailure(ctx, inst, req, err, pc), nil
	}

	return &domain.ProvisioningResponse{
		ProjectID:    inst.ProjectID,
		ProjectName:  inst.ProjectName,
		Status:       inst.Status,
		CurrentStage: inst.CurrentStage,
		Namespace:    inst.Namespace,
		Host:         inst.Host,
		Port:         inst.Port,
		DatabaseName: inst.DatabaseName,
		CreatedAt:    inst.CreatedAt,
	}, nil
}

// applyProvisioningResult copies the provisioner's output onto the instance.
// The Docker provisioner returns the container id in result.Namespace, which
// must overwrite the K8s-style namespace so deprovision and role creation
// reach the right target.
func applyProvisioningResult(inst *domain.DatabaseInstance, req domain.ProvisioningRequest, result *provisioner.ProvisioningResult) {
	port := result.Port
	inst.Host = result.Host
	inst.ReadOnlyHost = result.ReadOnlyHost
	inst.Port = &port
	inst.DatabaseName = result.DatabaseName
	inst.Username = result.Username
	inst.Password = result.Password
	inst.SSLMode = result.SSLMode
	if result.Namespace != "" {
		inst.Namespace = result.Namespace
	}
	inst.MetricsEndpoint = fmt.Sprintf("http://%s-postgres-1.%s.svc.cluster.local:9187/metrics", req.ProjectName, inst.Namespace)
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
	// ErrDeletionProtected is returned when the project opted out of being
	// deleted. Nothing is torn down.
	ErrDeletionProtected = errors.New("deletion protection is enabled")
	// ErrProjectDeleting is returned when a project under teardown is asked
	// to serve as a live project.
	ErrProjectDeleting = errors.New("project is being deleted")

	// ErrProjectRestoring is returned when a project's restore has not been
	// confirmed. Its row carries credentials, but nothing has proved the
	// recovered database answers, so they must not be handed out.
	ErrProjectRestoring = errors.New("project is being restored")
)

// isProjectGone reports whether an error means the project stopped being the
// caller's to work on — a teardown claimed it, or its record is already gone.
// Every in-flight pipeline treats it as a stop signal.
func isProjectGone(err error) bool {
	return errors.Is(err, storage.ErrProjectDeleting) || errors.Is(err, storage.ErrProjectNotFound)
}

// DeprovisionOptions tunes what Deprovision removes beyond the project.
type DeprovisionOptions struct {
	// DeleteBackups purges the project's backup objects once its resources
	// are gone. nil means the caller expressed no preference — a retry then
	// inherits whatever the running deletion recorded, so a bare retry can
	// never drop a purge someone already confirmed. A caller that explicitly
	// asks to keep backups over a confirmed purge is refused.
	DeleteBackups *bool
}

// DeleteBackupsOption builds the explicit form of the option.
func DeleteBackupsOption(delete bool) *bool { return &delete }

// ProjectObjectPurger clears everything a project stored in the object store.
// storagesvc.Service implements it.
type ProjectObjectPurger interface {
	PurgeProjectObjects(ctx context.Context, projectID string) (int, error)
}

// SetObjectPurger wires the purge of a project's stored files. Leave it unset
// when no object store is configured.
func (s *ProvisioningService) SetObjectPurger(p ProjectObjectPurger) { s.objectPurger = p }

// SetBackupPurger wires the object-store purge used when a deprovision asks
// for its backups to be deleted.
func (s *ProvisioningService) SetBackupPurger(p *BackupPurger) { s.backupPurger = p }

// Deprovision removes the project and keeps its backups.
func (s *ProvisioningService) Deprovision(ctx context.Context, projectID string) error {
	return s.DeprovisionWithOptions(ctx, projectID, DeprovisionOptions{})
}

// deletionStep is one unit of teardown. The steps run in order and each one
// is idempotent, so a retried DELETE re-runs the list from the top and
// resources an earlier attempt already removed count as done.
type deletionStep struct {
	name string
	run  func(context.Context, *domain.DatabaseInstance) error
}

// DeprovisionWithOptions tears the project down as a state machine on the
// instance row: the row moves to DELETING, every cleanup step must be
// observed complete, and only then is the row removed. A step that fails or
// times out leaves the row in DELETING carrying the failing step and why, and
// returns the error — the caller must never be told a project was deleted
// when live resources or credentials survive it.
//
// The one exception is the backup purge, which keeps its own retry marker
// (BACKUPS_PENDING_DELETE, retried via PurgeBackups) because by then the
// project's resources really are gone; it still reports the failure.
func (s *ProvisioningService) DeprovisionWithOptions(ctx context.Context, projectID string, opts DeprovisionOptions) error {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	// Protection is checked before the first claim only: once a teardown
	// owns the row the decision to delete has been made and recorded, and a
	// protection flag set meanwhile must not strand it half torn down.
	if !domain.IsDeletionStatus(inst.Status) && inst.DeletionProtection != nil && *inst.DeletionProtection {
		return fmt.Errorf("%w for %s", ErrDeletionProtected, projectID)
	}
	if opts.DeleteBackups != nil && *opts.DeleteBackups && s.backupPurger == nil {
		return ErrBackupPurgeNotConfigured
	}

	// DELETE takes the same per-project lease as pause and resume. A delete
	// that arrives mid-pause is told the project is busy and retried, which
	// is what DELETE already does against a PROVISIONING project — and far
	// better than preempting a shutdown halfway through. The lease cannot
	// hold it off indefinitely: every lifecycle operation is bounded by its
	// own timeout and releases on every path, including a panic.
	release, claimed, err := s.claimer().Claim(ctx, projectID, OperationDeletion)
	if err != nil {
		return fmt.Errorf("claim project for deletion: %w", err)
	}
	if !claimed {
		// The holder may be a pause or a resume, and for an advisory lease
		// we cannot tell which. Saying "a deletion is already running" was a
		// guess, and usually a wrong one.
		return fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	defer release()

	deleteBackups, err := s.store.BeginDeletion(projectID, opts.DeleteBackups)
	if err != nil {
		return err
	}
	for _, observer := range s.deletionObservers {
		observer.ProjectDeleting(projectID)
	}
	if deleteBackups && s.backupPurger == nil {
		return s.recordDeletionFailure(inst, domain.DeletionStepDeleteBackups, ErrBackupPurgeNotConfigured)
	}
	// Work from the claimed row, not the pre-claim snapshot.
	if inst, err = s.store.FindByProjectID(projectID); err != nil || inst == nil {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	}
	for _, step := range s.deletionSteps(deleteBackups) {
		if err := step.run(ctx, inst); err != nil {
			return s.recordDeletionFailure(inst, step.name, err)
		}
	}
	return nil
}

// DeletionObserver is told when a project is claimed for teardown. It exists
// for in-process caches that would otherwise keep treating the project as
// live until their own entry expires.
type DeletionObserver interface {
	ProjectDeleting(projectID string)
}

// AddDeletionObserver registers an observer of teardown claims.
func (s *ProvisioningService) AddDeletionObserver(o DeletionObserver) {
	s.deletionObservers = append(s.deletionObservers, o)
}

// SetOperationClaimer replaces the default in-process lifecycle lease. Wire
// the advisory-lock claimer when several control-plane replicas share a
// database; the same claimer must be given to the pause service, or the two
// will not exclude one another.
func (s *ProvisioningService) SetOperationClaimer(c ProjectOperationClaimer) { s.deletionClaimer = c }

func (s *ProvisioningService) claimer() ProjectOperationClaimer {
	s.claimerOnce.Do(func() {
		if s.deletionClaimer == nil {
			s.deletionClaimer = newInProcessOperationClaimer()
		}
	})
	return s.deletionClaimer
}

// RetainedBackupPrefix is the object-store prefix a deleted project's backups
// are kept under when the deletion did not ask for them to be purged. The
// delete response carries it because once the row is gone nothing else names
// it: the platform has no endpoint that lists or purges backups of a project
// it no longer has a record of.
func (s *ProvisioningService) RetainedBackupPrefix(inst *domain.DatabaseInstance) (string, bool) {
	keyPrefix, ok := s.backupKeyPrefix()
	if !ok && inst.DeploymentMode == domain.ModeDocker {
		// Docker's layout is whatever key prefix the purger was wired with.
		// Without one, any prefix we returned would name the wrong place.
		return "", false
	}
	prefix, err := ProjectBackupPrefix(inst.DeploymentMode, inst.ProjectID, keyPrefix)
	if err != nil {
		return "", false
	}
	return prefix, true
}

// backupKeyPrefix is the docker-mode key prefix the purger was wired with,
// and whether there is a purger to have been wired at all.
func (s *ProvisioningService) backupKeyPrefix() (string, bool) {
	if s.backupPurger == nil {
		return "", false
	}
	return s.backupPurger.DockerKeyPrefix(), true
}

// deletionSteps is the teardown order. NATS and PgDog go first so nothing
// reconnects to a database that is about to disappear; the provisioner then
// removes the database resources and waits them out; backups are purged only
// once those resources are gone; the credentials that reach them are removed
// last, and the row itself only after all of it.
func (s *ProvisioningService) deletionSteps(deleteBackups bool) []deletionStep {
	steps := []deletionStep{
		{domain.DeletionStepRevokeNats, s.revokeNatsCredentials},
		{domain.DeletionStepDeregisterPgDog, s.deregisterPgDog},
		{domain.DeletionStepDeleteResources, s.deleteDatabaseResources},
	}
	if deleteBackups {
		steps = append(steps, deletionStep{domain.DeletionStepDeleteBackups, s.purgeBackups})
	}
	// The project's stored files go whatever the caller decided about
	// backups: keeping backups is a choice about backups. With no object
	// store wired there is nothing to clear, so the step is absent rather
	// than present and failing.
	if s.objectPurger != nil {
		steps = append(steps, deletionStep{domain.DeletionStepDeleteObjects, s.purgeProjectObjects})
	}
	return append(steps,
		deletionStep{domain.DeletionStepDeleteVault, s.deleteVaultCredentials},
		deletionStep{domain.DeletionStepDeleteRecord, s.deleteProjectRecord},
	)
}

// recordDeletionFailure persists which step failed and why, then returns the
// original error so the caller reports failure. The row stays in DELETING and
// the same DELETE resumes from it.
func (s *ProvisioningService) recordDeletionFailure(inst *domain.DatabaseInstance, step string, cause error) error {
	log.Printf("deletion of %s stopped at %s: %v", inst.ProjectID, step, cause)
	status := domain.StatusDeleting
	if step == domain.DeletionStepDeleteBackups {
		// The project's resources are gone by now; only the backup objects
		// are outstanding, which is what POST /backups/purge retries.
		status = domain.StatusBackupsPendingDelete
	}
	if err := s.store.RecordDeletionFailure(inst.ProjectID, status, step, cause.Error()); err != nil {
		log.Printf(warnPersistFmt, err)
	}
	return fmt.Errorf("deletion step %s: %w", step, cause)
}

// revokeNatsCredentials drops the project's bus identity so a surviving
// watcher pod cannot reconnect and keep publishing after the project is gone.
func (s *ProvisioningService) revokeNatsCredentials(ctx context.Context, inst *domain.DatabaseInstance) error {
	return s.natsCreds.RevokeProject(ctx, inst.ProjectID)
}

// deregisterPgDog removes the project from the database gateway so no pooled
// connection survives its database.
func (s *ProvisioningService) deregisterPgDog(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.pgdog == nil {
		return nil
	}
	return s.pgdog.DeregisterCluster(ctx, inst.ProjectID)
}

// deleteDatabaseResources hands teardown to the provisioner, which returns
// only once its resources are observed gone.
func (s *ProvisioningService) deleteDatabaseResources(ctx context.Context, inst *domain.DatabaseInstance) error {
	prov, ok := s.factory.Get(inst.DBType)
	if !ok {
		return fmt.Errorf("no provisioner for database type %s", inst.DBType)
	}
	return prov.Deprovision(ctx, inst.Namespace, inst.ProjectID)
}

// purgeBackups deletes the project's backup objects. A failure converts the
// row into the BACKUPS_PENDING_DELETE retry marker PurgeBackups works from,
// and is still reported to the caller.
func (s *ProvisioningService) purgeBackups(ctx context.Context, inst *domain.DatabaseInstance) error {
	deleted, err := s.backupPurger.Purge(ctx, inst)
	if errors.Is(err, ErrNoBackupsForMode) {
		log.Printf("backup purge skipped for %s: %v", inst.ProjectID, err)
		return nil
	}
	if err != nil {
		return err
	}
	log.Printf("backup purge for %s deleted %d objects", inst.ProjectID, deleted)
	return nil
}

// purgeProjectObjects clears everything the project stored, before the
// credentials and the record go. Afterwards nothing names those bytes: the
// catalogue rows are deleted with the record, and no endpoint lists a project
// the platform has no record of. Idempotent — a prefix that is already clear
// purges nothing and succeeds, so a retried teardown resumes here.
func (s *ProvisioningService) purgeProjectObjects(ctx context.Context, inst *domain.DatabaseInstance) error {
	deleted, err := s.objectPurger.PurgeProjectObjects(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	log.Printf("object purge for %s deleted %d objects", inst.ProjectID, deleted)
	return nil
}

// deleteProjectRecord removes the row. It runs last: until it does, the
// project remains visible as DELETING with everything needed to retry.
func (s *ProvisioningService) deleteProjectRecord(_ context.Context, inst *domain.DatabaseInstance) error {
	return s.store.Delete(inst.ProjectID)
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
		if rerr := s.store.RecordDeletionFailure(projectID, domain.StatusBackupsPendingDelete,
			domain.DeletionStepDeleteBackups, "backup purge failed: "+err.Error()); rerr != nil {
			log.Printf(warnPersistFmt, rerr)
		}
		return deleted, err
	}
	log.Printf("backup purge retry for %s deleted %d objects", projectID, deleted)
	return deleted, s.store.Delete(projectID)
}

// deleteVaultCredentials removes every secret filed under the project and
// confirms the prefix is empty afterwards. These are live passwords on a
// real database, so a vault that is sealed or unreachable is a failure: the
// alternative is dropping the record that says the credentials exist.
func (s *ProvisioningService) deleteVaultCredentials(_ context.Context, inst *domain.DatabaseInstance) error {
	if s.vault == nil {
		return nil
	}
	if s.vault.Sealed() {
		return errors.New("vault is sealed; project credentials cannot be removed")
	}
	// Path is project-scoped only — no orgSlug guessing.
	prefix := vaultProjectPrefix(inst.ProjectID)
	if _, err := s.vault.DeletePrefix(prefix); err != nil {
		return fmt.Errorf("vault delete prefix %s: %w", prefix, err)
	}
	left, err := s.vault.List(prefix)
	if err != nil {
		return fmt.Errorf("vault list prefix %s: %w", prefix, err)
	}
	if len(left) > 0 {
		return fmt.Errorf("vault still holds %d secret(s) under %s", len(left), prefix)
	}
	return nil
}

// vaultProjectPrefix is the canonical prefix for everything stored under a
// project. All credential / secret paths sit under this prefix.
func vaultProjectPrefix(projectID string) string {
	return fmt.Sprintf("projects/%s/", projectID)
}

// vaultCredentialPath builds the canonical credential path. role is e.g.
// "admin", "excalibase_app", "auth_admin", "cdc_watcher", "jwt_keys/anon_token".
func vaultCredentialPath(projectID, role string) string {
	return vaultCredentialPrefix(projectID) + role
}

// vaultCredentialPrefix is the path every one of a project's role credentials
// is filed under.
func vaultCredentialPrefix(projectID string) string {
	return fmt.Sprintf("projects/%s/credentials/", projectID)
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

// notServableErr picks the sentinel that matches why the project may not be
// served, so a caller can tell a teardown from an unconfirmed restore.
func notServableErr(status string) error {
	if status == string(domain.StatusRestoring) {
		return ErrProjectRestoring
	}
	return ErrProjectDeleting
}

func (s *ProvisioningService) GetCredentials(projectID string) (*domain.CredentialsResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}
	// Credentials of a project being torn down open a database that is
	// disappearing, and the roles behind them are about to be revoked. A
	// project still being restored has credentials nothing has proved work
	// — handing them out publishes a connection string for a database that
	// may never have recovered.
	if domain.IsNotServable(inst.Status) {
		return nil, fmt.Errorf("%w: %s", notServableErr(inst.Status), projectID)
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
	return s.store.Update(inst)
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

package domain

// DatabaseType represents supported database engines.
type DatabaseType string

const (
	PostgreSQL DatabaseType = "POSTGRESQL"
	MySQL      DatabaseType = "MYSQL"
	MongoDB    DatabaseType = "MONGODB"
)

// TierType represents resource tiers.
type TierType string

const (
	Free       TierType = "FREE"
	Standard   TierType = "STANDARD"
	Enterprise TierType = "ENTERPRISE"
)

var validTiers = map[TierType]bool{Free: true, Standard: true, Enterprise: true}

func IsValidTier(t TierType) bool {
	return validTiers[t]
}

// DeploymentMode represents how a project's database is managed.
type DeploymentMode string

const (
	ModeK8s    DeploymentMode = "k8s"
	ModeDocker DeploymentMode = "docker"
)

// ProvisioningStage represents stages in the provisioning pipeline.
type ProvisioningStage string

const (
	StageValidating           ProvisioningStage = "VALIDATING"
	StageNamespaceCreation    ProvisioningStage = "NAMESPACE_CREATION"
	StageCRDDeployment        ProvisioningStage = "CRD_DEPLOYMENT"
	StageWaitingForReady      ProvisioningStage = "WAITING_FOR_READY"
	StageCredentialGeneration ProvisioningStage = "CREDENTIAL_GENERATION"
	StageBackupConfiguration  ProvisioningStage = "BACKUP_CONFIGURATION"
	StageMetricsSetup         ProvisioningStage = "METRICS_SETUP"
	StageWatcherDeployment    ProvisioningStage = "WATCHER_DEPLOYMENT"
	StageContainerCreation    ProvisioningStage = "CONTAINER_CREATION"
	StageRoleCreation         ProvisioningStage = "ROLE_CREATION"
	StageCompleted            ProvisioningStage = "COMPLETED"
	StageFailed               ProvisioningStage = "FAILED"
	// Pause/resume lifecycle (see service/pause.go). Status string,
	// not pipeline stage in the strict sense — but they live on the
	// same Status column so unifying the enum keeps the storage layer
	// simple. Intermediate states (PAUSING, RESUMING) are visible to
	// the API so callers polling status see progress.
	StatusPausing  ProvisioningStage = "PAUSING"
	StatusPaused   ProvisioningStage = "PAUSED"
	StatusResuming ProvisioningStage = "RESUMING"
	// StatusBackupsPendingDelete marks a row whose resources are gone but
	// whose backup objects could not be purged. The row is kept only so
	// POST /backups/purge (or an operator sweep) can retry the deletion.
	StatusBackupsPendingDelete ProvisioningStage = "BACKUPS_PENDING_DELETE"
	// StatusDeleting marks a project whose teardown has started. The row
	// survives until every cleanup step is observed complete, so a failed
	// or timed-out teardown keeps the record needed to retry it. A project
	// in this state is no longer usable and must not be served as active.
	StatusDeleting ProvisioningStage = "DELETING"
)

// DeletionSteps are the teardown steps, in the order Deprovision runs them.
// The name of the step that failed is persisted on the row so a retry —
// and an operator — can see exactly how far the teardown got.
const (
	DeletionStepRevokeNats      = "REVOKE_NATS_CREDENTIALS"
	DeletionStepDeregisterPgDog = "DEREGISTER_PGDOG"
	DeletionStepDeleteResources = "DELETE_DATABASE_RESOURCES"
	DeletionStepDeleteBackups   = "DELETE_BACKUPS"
	DeletionStepDeleteVault     = "DELETE_VAULT_CREDENTIALS"
	DeletionStepDeleteRecord    = "DELETE_PROJECT_RECORD"
)

// Pause reasons recorded on database_instances.pause_reason. Empty
// for ACTIVE projects.
const (
	// PauseReasonIdle is written by the idle-pause scheduler; the threshold
	// is per-tier (tier_configs.auto_pause_after_days), hence no day count.
	PauseReasonIdle      = "idle"
	PauseReasonIdle7Days = "idle_7d"
	PauseReasonManual    = "manual"
	PauseReasonTierLimit = "tier_limit"
)

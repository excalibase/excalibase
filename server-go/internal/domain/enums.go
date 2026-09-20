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
	// StatusRestoring marks a project whose database has been recovered but
	// not yet proved usable. A restore registers its target in this state and
	// only flips it to ACTIVE once a query has answered, so a recovery that
	// never happened is never served as a working project.
	StatusRestoring ProvisioningStage = "RESTORING"
)

// StatusProvisioning is the status a project holds while its provisioning
// pipeline is building it. It is the one state in which another flow is
// actively creating Kubernetes resources, vault credentials and bus
// identities for the project.
const StatusProvisioning = "PROVISIONING"

// IsBuildingStatus reports whether a status means a pipeline is currently
// creating resources for the project. Deleting such a project would race the
// build: the teardown can observe nothing left, remove the record, and the
// build then finishes into live, unowned resources.
//
// Only PROVISIONING qualifies. PAUSING and RESUMING toggle an existing
// workload rather than creating anything, and both are states a failed
// pause/resume deliberately leaves behind — refusing deletion there would
// make a stuck project undeletable. ACTIVE, PAUSED and FAILED are settled.
func IsBuildingStatus(status string) bool {
	return status == StatusProvisioning
}

// IsDeletionStatus reports whether a status means a teardown already owns the
// project. Both states are points of no return: the resources behind the
// project are being removed, so nothing may write the row back to a usable
// state. BACKUPS_PENDING_DELETE is one of them — its resources are already
// gone and only the backup purge is outstanding.
func IsDeletionStatus(status string) bool {
	return status == string(StatusDeleting) || status == string(StatusBackupsPendingDelete)
}

// StatusActive is the one status a fully provisioned, running project
// holds. It is the allow-list background work checks against: a sweep that
// asks "is this not one of the bad states?" also reaches PAUSED, PAUSING,
// RESUMING and PROVISIONING projects, whose databases are hibernated or not
// there yet, and retries against them forever.
const StatusActive ProvisioningStage = "ACTIVE"

// IsActive reports whether a project is running right now. Background work
// that opens a project's database uses this rather than !IsNotServable: the
// allow-list has one member and cannot silently grow.
func IsActive(status string) bool {
	return status == string(StatusActive)
}

// IsNotServable reports whether a project must not be served: no data-plane
// traffic routed to it, no JWT minted for it, no credentials handed out, no
// function deployed or invoked against it, no membership rewritten.
//
// Two reasons qualify. A project under teardown is about to stop existing.
// A project in RESTORING carries working-looking credentials for a database
// nothing has confirmed yet — the recovered cluster may never have come up,
// and if the process driving the restore dies, the row stays that way. In
// both cases the row exists, which is exactly why every read has to ask.
//
// The list lives here so the gate, the handlers and the data plane cannot
// drift apart.
func IsNotServable(status string) bool {
	return IsDeletionStatus(status) || status == string(StatusRestoring)
}

// NotServableReason is the fixed sentence a caller is given for a project
// that is not servable. It names what is happening and nothing else.
func NotServableReason(status string) string {
	if status == string(StatusRestoring) {
		return "project is being restored"
	}
	return "project is being deleted"
}

// DeletionSteps are the teardown steps, in the order Deprovision runs them.
// The name of the step that failed is persisted on the row so a retry —
// and an operator — can see exactly how far the teardown got.
const (
	DeletionStepRevokeNats      = "REVOKE_NATS_CREDENTIALS"
	DeletionStepDeregisterPgDog = "DEREGISTER_PGDOG"
	DeletionStepDeleteResources = "DELETE_DATABASE_RESOURCES"
	DeletionStepDeleteBackups   = "DELETE_BACKUPS"
	DeletionStepDeleteObjects   = "DELETE_PROJECT_OBJECTS"
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

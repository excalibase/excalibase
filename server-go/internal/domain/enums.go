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
	ModeBYOC   DeploymentMode = "byoc"
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
	StageCompleted            ProvisioningStage = "COMPLETED"
	StageFailed               ProvisioningStage = "FAILED"
)

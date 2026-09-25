package domain

import "errors"

// DTOs for API request/response

// --- Provisioning ---

type ProvisioningRequest struct {
	ProjectName     string                   `json:"projectName"`
	OrgID           string                   `json:"orgId"`
	OwnerID         string                   `json:"ownerId,omitempty"` // set by handler from auth context
	DBType          DatabaseType             `json:"databaseType"`
	Tier            TierType                 `json:"tier"`
	Backup          *BackupSettings          `json:"backup,omitempty"`
	Pooler          *PoolerSettings          `json:"pooler,omitempty"`
	Network         *NetworkConfig           `json:"network,omitempty"`
	Maintenance     *MaintenanceWindowConfig `json:"maintenance,omitempty"`
	Parameters      map[string]string        `json:"parameters,omitempty"`
	Tags            map[string]string        `json:"tags,omitempty"`
	WebhookURL      string                   `json:"webhookUrl,omitempty"`
	StorageClass    string                   `json:"storageClassName,omitempty"`
	PostgresVersion string                   `json:"postgresVersion,omitempty"`
	DatabaseName    string                   `json:"databaseName,omitempty"`
	MasterUsername  string                   `json:"masterUsername,omitempty"`
	ParameterGroup  string                   `json:"parameterGroupName,omitempty"`
	// DocumentDB asks for a project whose image carries the DocumentDB
	// extension. It is a create-time choice and only a create-time choice: the
	// image a cluster runs is fixed when the cluster is provisioned, so a
	// project cannot be turned into a DocumentDB project afterwards. Only
	// majors the catalogue marks as DocumentDB-capable accept it; creating the
	// extension in the database is EXC-409.
	DocumentDB bool `json:"documentDb,omitempty"`
}

type BackupSettings struct {
	Enabled   bool           `json:"enabled"`
	Schedule  string         `json:"schedule,omitempty"`
	Retention int            `json:"retention,omitempty"`
	S3        *S3Credentials `json:"s3,omitempty"`
}

type S3Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	Bucket          string `json:"bucket"`
	Region          string `json:"region,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
}

type PoolerSettings struct {
	Enabled  bool   `json:"enabled"`
	PoolMode string `json:"poolMode,omitempty"`
	PoolSize int    `json:"poolSize,omitempty"`
}

type NetworkConfig struct {
	AllowedCIDRs  []string `json:"allowedCidrs,omitempty"`
	AllowedPorts  []int    `json:"allowedPorts,omitempty"`
	PolicyEnabled bool     `json:"policyEnabled"`
}

type MaintenanceWindowConfig struct {
	Window          string `json:"window,omitempty"`
	DurationMinutes int    `json:"durationMinutes,omitempty"`
	AutoUpgrade     bool   `json:"autoMinorVersionUpgrade"`
}

type ProvisioningResponse struct {
	ProjectID     string            `json:"projectId"`
	ProjectName   string            `json:"projectName,omitempty"`
	Status        string            `json:"status"`
	CurrentStage  ProvisioningStage `json:"currentStage"`
	Namespace     string            `json:"namespace"`
	Host          string            `json:"host,omitempty"`
	Port          *int              `json:"port,omitempty"`
	DatabaseName  string            `json:"databaseName,omitempty"`
	FailureReason string            `json:"failureReason,omitempty"`
	FailureStage  ProvisioningStage `json:"failureStage,omitempty"`
	FailureStep   string            `json:"failureStep,omitempty"`
	RollbackLog   string            `json:"rollbackLog,omitempty"`
	CreatedAt     *FlexTime         `json:"createdAt,omitempty"`
}

// --- Credentials ---

type CredentialsResponse struct {
	ProjectID     string `json:"projectId"`
	Host          string `json:"host"`
	ReadOnlyHost  string `json:"readOnlyHost,omitempty"`
	Port          int    `json:"port"`
	DatabaseName  string `json:"databaseName"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	SSLMode       string `json:"sslMode"`
	ConnectionURL string `json:"connectionUrl"`
}

type CredentialsData struct {
	Host         string `json:"host"`
	ReadOnlyHost string `json:"readOnlyHost,omitempty"`
	Port         int    `json:"port"`
	DatabaseName string `json:"databaseName"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	SSLMode      string `json:"sslMode,omitempty"`
}

// --- Metrics ---

type DatabaseMetrics struct {
	ProjectID         string    `json:"projectId"`
	Timestamp         *FlexTime `json:"timestamp,omitempty"`
	Status            string    `json:"status,omitempty"`
	HealthStatus      string    `json:"healthStatus,omitempty"`
	MetricsAvailable  bool      `json:"metricsAvailable"`
	UnavailableReason *string   `json:"unavailableReason"`

	// Resource usage (from metrics-server — null if unavailable)
	CPUUsagePercent    *float64 `json:"cpuUsagePercent"`
	MemoryUsagePercent *float64 `json:"memoryUsagePercent"`
	DiskUsagePercent   *float64 `json:"diskUsagePercent"`
	CPUUsageCores      *float64 `json:"cpuUsageCores"`
	MemoryUsageMB      *int64   `json:"memoryUsageMB"`
	DiskUsageGB        *int64   `json:"diskUsageGB"`

	// Database metrics (from CNPG port 9187)
	ActiveConnections *int     `json:"activeConnections"`
	IdleConnections   *int     `json:"idleConnections"`
	MaxConnections    *int     `json:"maxConnections"`
	QueriesPerSecond  *float64 `json:"queriesPerSecond"`
	AvgQueryLatencyMs *float64 `json:"averageQueryLatencyMs"`
	SlowQueryCount    *int     `json:"slowQueryCount"`
	DatabaseSizeGB    *int64   `json:"databaseSizeGB"`

	// Backup
	LastBackupTime *FlexTime `json:"lastBackupTime"`
	NextBackupTime *FlexTime `json:"nextBackupTime"`

	// Resource limits (from tier config)
	CPULimitCores *float64 `json:"cpuLimitCores"`
	MemoryLimitMB *int64   `json:"memoryLimitMB"`
	StorageLimit  *string  `json:"storageLimit"`
	InstanceCount *int     `json:"instanceCount"`

	// Per-pod breakdown (from metrics-server)
	Pods []PodMetrics `json:"pods,omitempty"`
}

type PodMetrics struct {
	Name          string  `json:"name"`
	Role          string  `json:"role"` // "primary" or "replica"
	CPUCores      float64 `json:"cpuCores"`
	CPULimitCores float64 `json:"cpuLimitCores"`
	MemoryMB      int64   `json:"memoryMB"`
	MemoryLimitMB int64   `json:"memoryLimitMB"`
}

type MetricsHistory struct {
	ProjectID   string            `json:"projectId"`
	Metrics     []DatabaseMetrics `json:"metrics"`
	TotalPoints int               `json:"totalPoints"`
}

// --- Backup ---

type BackupConfig struct {
	Enabled       bool   `json:"enabled"`
	Schedule      string `json:"schedule"`
	RetentionDays int    `json:"retentionDays"`
	LastBackup    string `json:"lastBackup,omitempty"`
}

type BackupRecord struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`   // MANUAL, SCHEDULED
	Status    string `json:"status"` // IN_PROGRESS, COMPLETED, FAILED
}

// BackupSchedule is the persistent record driving the platform's
// scheduled-backup cron. RetentionDays is informational only at the
// scheduler layer — adapters consult it when deciding what to prune.
type BackupSchedule struct {
	ProjectID     string `json:"projectId"`
	Cron          string `json:"cron"`
	RetentionDays int    `json:"retentionDays"`
	Enabled       bool   `json:"enabled"`
}

// Restore job statuses. Source of truth is the `status` column on
// restore_jobs.
const (
	RestoreStatusRunning   = "RUNNING"
	RestoreStatusCompleted = "COMPLETED"
	RestoreStatusFailed    = "FAILED"
)

// RestoreJob is the persisted state of a restore-into-new-project run.
// Returned synchronously from POST /api/projects/.../backup/restore so
// the caller can poll status via GET /api/projects/.../restore/{jobId}.
type RestoreJob struct {
	ID              string `json:"id"`
	SourceProjectID string `json:"sourceProjectId"`
	// NewProjectID is the generated id of the project the restore creates.
	// The client learns it here — it never supplies it.
	NewProjectID   string `json:"newProjectId"`
	NewProjectName string `json:"newProjectName,omitempty"`
	Status         string `json:"status"` // RUNNING | COMPLETED | FAILED
	CurrentStep    string `json:"currentStep,omitempty"`
	TargetKind     string `json:"targetKind"` // latest | time | xid | lsn | name
	TargetValue    string `json:"targetValue,omitempty"`
	FailureReason  string `json:"failureReason,omitempty"`
	// Owner names the platform process driving this job. Only that process
	// may write progress onto it, so a replica restarting mid-deploy cannot
	// fail a restore one of its peers is still running.
	Owner string `json:"-"`
	// HeartbeatAt is when the owner last proved it was alive. A job whose
	// heartbeat has gone stale is abandoned and may be failed by any replica.
	HeartbeatAt string `json:"-"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// RestoreTargetKind extracts the canonical target kind from a
// RestoreRequest. Used by the orchestrator when persisting RestoreJob.
func (r RestoreRequest) RestoreTargetKind() (kind, value string) {
	switch {
	case r.TargetTime != nil:
		return "time", r.TargetTime.Time.Format("2006-01-02T15:04:05Z")
	case r.TargetXID != "":
		return "xid", r.TargetXID
	case r.TargetLSN != "":
		return "lsn", r.TargetLSN
	case r.TargetName != "":
		return "name", r.TargetName
	}
	return "latest", ""
}

// RestoreRequest controls a point-in-time / latest restore into a new
// project. Exactly one of TargetTime / TargetXID / TargetLSN / TargetName
// may be set; absence of all four means "restore to latest". Validate()
// enforces the single-target rule so handlers can fail fast.
type RestoreRequest struct {
	BackupID   string    `json:"backupId,omitempty"`
	TargetTime *FlexTime `json:"targetTime,omitempty"`
	TargetXID  string    `json:"targetXid,omitempty"`
	TargetLSN  string    `json:"targetLsn,omitempty"`
	TargetName string    `json:"targetName,omitempty"`
	// NewProjectName is the display name for the restored project. It is
	// the only naming the caller controls.
	NewProjectName string `json:"newProjectName,omitempty"`
	// TargetProjectID is the id the restored project is registered under.
	// The platform generates it the same way a provision does and it is
	// never read from the request body — a caller who could name it could
	// repoint another tenant's project (EXC-415).
	TargetProjectID string `json:"-"`
}

// Validate enforces the single-target invariant. Operationally the
// most common ask is "restore to just before the bad migration ran"
// which maps to TargetXID or TargetLSN — neither is supported by the
// pre-Phase-1 API. Shipping the union now (even if only TargetTime is
// wired in CNPG) avoids breaking the public surface in Phase 2/3.
func (r RestoreRequest) Validate() error {
	count := 0
	if r.TargetTime != nil {
		count++
	}
	if r.TargetXID != "" {
		count++
	}
	if r.TargetLSN != "" {
		count++
	}
	if r.TargetName != "" {
		count++
	}
	if count > 1 {
		return errors.New("restore request: at most one of targetTime, targetXid, targetLsn, targetName may be set")
	}
	if r.NewProjectName == "" {
		return errors.New("restore request: newProjectName is required")
	}
	return nil
}

// RecoveryTarget returns the CNPG `recoveryTarget` map (or nil for
// "latest") so the K8s adapter can render the bootstrap recovery spec.
// Docker-mode adapter renders WAL-G `recovery.signal` flags from the
// same fields.
func (r RestoreRequest) RecoveryTarget() map[string]interface{} {
	switch {
	case r.TargetTime != nil:
		return map[string]interface{}{"targetTime": r.TargetTime.Time.Format("2006-01-02T15:04:05Z")}
	case r.TargetXID != "":
		return map[string]interface{}{"targetXID": r.TargetXID}
	case r.TargetLSN != "":
		return map[string]interface{}{"targetLSN": r.TargetLSN}
	case r.TargetName != "":
		return map[string]interface{}{"targetName": r.TargetName}
	}
	return nil
}

// --- Performance ---

type PerformanceSummary struct {
	ActiveConnections *int     `json:"activeConnections"`
	TotalConnections  *int     `json:"totalConnections"`
	CacheHitRatio     *float64 `json:"cacheHitRatio"`
	DatabaseSize      string   `json:"databaseSize,omitempty"`
	SlowQueryCount    *int     `json:"slowQueryCount"`
	AvgQueryTimeMs    *float64 `json:"avgQueryTimeMs"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailableReason,omitempty"`
}

type QueryStat struct {
	Query           string  `json:"query"`
	Calls           int64   `json:"calls"`
	TotalExecTimeMs float64 `json:"totalExecTimeMs"`
	AvgExecTimeMs   float64 `json:"avgExecTimeMs"`
	MinExecTimeMs   float64 `json:"minExecTimeMs"`
	MaxExecTimeMs   float64 `json:"maxExecTimeMs"`
	Rows            int64   `json:"rows"`
}

type WaitEvent struct {
	WaitEventType string `json:"waitEventType"`
	WaitEvent     string `json:"waitEvent"`
	Count         int    `json:"count"`
}

// --- Alerts ---

type Alert struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Severity  string    `json:"severity"` // WARNING, CRITICAL
	Message   string    `json:"message"`
	Metric    string    `json:"metric,omitempty"`
	Value     *float64  `json:"value,omitempty"`
	Threshold *float64  `json:"threshold,omitempty"`
	Timestamp *FlexTime `json:"timestamp"`
	Resolved  bool      `json:"resolved"`
}

// --- Audit ---

type AuditConfig struct {
	Enabled  bool              `json:"enabled"`
	LogLevel string            `json:"logLevel,omitempty"`
	Settings map[string]string `json:"settings,omitempty"`
}

// --- Snapshot ---

type SnapshotInfo struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Format    string    `json:"format"`
	Size      int64     `json:"size"`
	CreatedAt *FlexTime `json:"createdAt"`
	FilePath  string    `json:"filePath,omitempty"`
}

type SnapshotExportRequest struct {
	Format        string   `json:"format,omitempty"` // custom, plain, directory, tar
	SchemaOnly    bool     `json:"schemaOnly"`
	Tables        []string `json:"tables,omitempty"`
	ExcludeTables []string `json:"excludeTables,omitempty"`
}

// --- Migration ---

type MigrationRecord struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Version         string    `json:"version,omitempty"`
	Name            string    `json:"name,omitempty"`
	Description     string    `json:"description,omitempty"`
	SQL             string    `json:"sql"`
	Status          string    `json:"status"`
	AppliedAt       *FlexTime `json:"appliedAt,omitempty"`
	ExecutionTimeMs int64     `json:"executionTimeMs"`
	ErrorMessage    string    `json:"errorMessage,omitempty"`
	Checksum        string    `json:"checksum,omitempty"`
	Output          string    `json:"output,omitempty"`
}

type MigrationRequest struct {
	Version     string `json:"version,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	SQL         string `json:"sql"`
}

// --- Setup ---

type OperatorInstallRequest struct {
	DBType DatabaseType `json:"databaseType"`
}

// OperatorStatus is the control plane's internal view of which database
// operators the cluster runs. It never leaves the process: the wire shape is
// SetupStatusResponse.
type OperatorStatus struct {
	PostgreSQL bool `json:"postgresql"`
	MySQL      bool `json:"mysql"`
	MongoDB    bool `json:"mongodb"`
}

// SetupStatusResponse is what the unauthenticated GET /api/setup/status
// answers: one bit, because the installer polls it before any credential
// exists and an anonymous caller must learn nothing else about the cluster.
type SetupStatusResponse struct {
	Complete bool `json:"complete"`
}

// --- Cost ---

type CostEstimation struct {
	Tier           TierType           `json:"tier"`
	MonthlyCostUSD float64            `json:"monthlyCostUsd"`
	Breakdown      map[string]float64 `json:"breakdown"`
}

// --- Parameter Group ---

type ParameterGroupRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Parameters  map[string]string `json:"parameters"`
}

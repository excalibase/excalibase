package domain

// DTOs for API request/response

// --- Provisioning ---

type ProvisioningRequest struct {
	ProjectName     string            `json:"projectName"`
	OrgID           string            `json:"orgId"`
	OwnerID         string            `json:"ownerId,omitempty"` // set by handler from auth context
	DBType          DatabaseType      `json:"databaseType"`
	Tier            TierType          `json:"tier"`
	Backup          *BackupSettings   `json:"backup,omitempty"`
	Pooler          *PoolerSettings   `json:"pooler,omitempty"`
	Network         *NetworkConfig    `json:"network,omitempty"`
	Maintenance     *MaintenanceWindowConfig `json:"maintenance,omitempty"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	WebhookURL      string            `json:"webhookUrl,omitempty"`
	StorageClass    string            `json:"storageClassName,omitempty"`
	PostgresVersion string            `json:"postgresVersion,omitempty"`
	DatabaseName    string            `json:"databaseName,omitempty"`
	MasterUsername  string            `json:"masterUsername,omitempty"`
	ParameterGroup  string            `json:"parameterGroupName,omitempty"`
	AppPassword     string            `json:"appPassword,omitempty"` // optional: password for excalibase_app role
}

// BYOCRequest is used to register an externally managed database.
type BYOCRequest struct {
	ProjectName string `json:"projectName"`
	OrgID       string `json:"orgId"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Database    string `json:"database"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	SSLMode     string `json:"sslMode,omitempty"`
}

type BackupSettings struct {
	Enabled   bool            `json:"enabled"`
	Schedule  string          `json:"schedule,omitempty"`
	Retention int             `json:"retention,omitempty"`
	S3        *S3Credentials  `json:"s3,omitempty"`
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
	AllowedCIDRs   []string `json:"allowedCidrs,omitempty"`
	AllowedPorts   []int    `json:"allowedPorts,omitempty"`
	PolicyEnabled  bool     `json:"policyEnabled"`
}

type MaintenanceWindowConfig struct {
	Window          string `json:"window,omitempty"`
	DurationMinutes int    `json:"durationMinutes,omitempty"`
	AutoUpgrade     bool   `json:"autoMinorVersionUpgrade"`
}

type ProvisioningResponse struct {
	ProjectID    string            `json:"projectId"`
	ProjectName  string            `json:"projectName,omitempty"`
	Status       string            `json:"status"`
	CurrentStage ProvisioningStage `json:"currentStage"`
	Namespace    string            `json:"namespace"`
	Host         string            `json:"host,omitempty"`
	Port         *int              `json:"port,omitempty"`
	DatabaseName string            `json:"databaseName,omitempty"`
	FailureReason string           `json:"failureReason,omitempty"`
	FailureStage  ProvisioningStage `json:"failureStage,omitempty"`
	FailureStep   string            `json:"failureStep,omitempty"`
	RollbackLog   string            `json:"rollbackLog,omitempty"`
	CreatedAt    *FlexTime         `json:"createdAt,omitempty"`
}

// --- Credentials ---

type CredentialsResponse struct {
	ProjectID    string `json:"projectId"`
	Host         string `json:"host"`
	ReadOnlyHost string `json:"readOnlyHost,omitempty"`
	Port         int    `json:"port"`
	DatabaseName string `json:"databaseName"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	SSLMode      string `json:"sslMode"`
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
	ProjectID          string    `json:"projectId"`
	Timestamp          *FlexTime `json:"timestamp,omitempty"`
	Status             string     `json:"status,omitempty"`
	HealthStatus       string     `json:"healthStatus,omitempty"`
	MetricsAvailable   bool       `json:"metricsAvailable"`
	UnavailableReason  *string    `json:"unavailableReason"`

	// Resource usage (from metrics-server — null if unavailable)
	CPUUsagePercent    *float64 `json:"cpuUsagePercent"`
	MemoryUsagePercent *float64 `json:"memoryUsagePercent"`
	DiskUsagePercent   *float64 `json:"diskUsagePercent"`
	CPUUsageCores      *float64 `json:"cpuUsageCores"`
	MemoryUsageMB      *int64   `json:"memoryUsageMB"`
	DiskUsageGB        *int64   `json:"diskUsageGB"`

	// Database metrics (from CNPG port 9187)
	ActiveConnections   *int     `json:"activeConnections"`
	IdleConnections     *int     `json:"idleConnections"`
	MaxConnections      *int     `json:"maxConnections"`
	QueriesPerSecond    *float64 `json:"queriesPerSecond"`
	AvgQueryLatencyMs   *float64 `json:"averageQueryLatencyMs"`
	SlowQueryCount      *int     `json:"slowQueryCount"`
	DatabaseSizeGB      *int64   `json:"databaseSizeGB"`

	// Backup
	LastBackupTime *FlexTime `json:"lastBackupTime"`
	NextBackupTime *FlexTime `json:"nextBackupTime"`

	// Resource limits (from tier config)
	CPULimitCores    *float64 `json:"cpuLimitCores"`
	MemoryLimitMB    *int64   `json:"memoryLimitMB"`
	StorageLimit     *string  `json:"storageLimit"`
	InstanceCount    *int     `json:"instanceCount"`

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

type RestoreRequest struct {
	BackupID       string    `json:"backupId,omitempty"`
	TargetTime     *FlexTime `json:"targetTime,omitempty"`
	NewProjectName string    `json:"newProjectName,omitempty"`
	NewProjectID   string    `json:"newProjectId,omitempty"`
}

func (r RestoreRequest) GetNewProject() string {
	if r.NewProjectName != "" {
		return r.NewProjectName
	}
	return r.NewProjectID
}

type CloneRequest struct {
	SourceProjectID string `json:"sourceProjectId"`
	NewProjectName  string `json:"newProjectName"`
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
	Query          string  `json:"query"`
	Calls          int64   `json:"calls"`
	TotalExecTimeMs float64 `json:"totalExecTimeMs"`
	AvgExecTimeMs  float64 `json:"avgExecTimeMs"`
	MinExecTimeMs  float64 `json:"minExecTimeMs"`
	MaxExecTimeMs  float64 `json:"maxExecTimeMs"`
	Rows           int64   `json:"rows"`
}

type WaitEvent struct {
	WaitEventType string `json:"waitEventType"`
	WaitEvent     string `json:"waitEvent"`
	Count         int    `json:"count"`
}

// --- Alerts ---

type Alert struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Severity  string     `json:"severity"` // WARNING, CRITICAL
	Message   string     `json:"message"`
	Metric    string     `json:"metric,omitempty"`
	Value     *float64   `json:"value,omitempty"`
	Threshold *float64   `json:"threshold,omitempty"`
	Timestamp *FlexTime `json:"timestamp"`
	Resolved  bool       `json:"resolved"`
}

// --- Audit ---

type AuditConfig struct {
	Enabled    bool              `json:"enabled"`
	LogLevel   string            `json:"logLevel,omitempty"`
	Settings   map[string]string `json:"settings,omitempty"`
}

// --- Snapshot ---

type SnapshotInfo struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Format    string     `json:"format"`
	Size      int64      `json:"size"`
	CreatedAt *FlexTime `json:"createdAt"`
	FilePath  string     `json:"filePath,omitempty"`
}

type SnapshotExportRequest struct {
	Format      string   `json:"format,omitempty"` // custom, plain, directory, tar
	SchemaOnly  bool     `json:"schemaOnly"`
	Tables      []string `json:"tables,omitempty"`
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

type SetupStatusResponse struct {
	PostgreSQL bool `json:"postgresql"`
	MySQL      bool `json:"mysql"`
	MongoDB    bool `json:"mongodb"`
}

// --- Cost ---

type CostEstimation struct {
	Tier           TierType `json:"tier"`
	MonthlyCostUSD float64  `json:"monthlyCostUsd"`
	Breakdown      map[string]float64 `json:"breakdown"`
}

// --- Parameter Group ---

type ParameterGroupRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Parameters  map[string]string `json:"parameters"`
}

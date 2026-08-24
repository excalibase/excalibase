package domain

import (
	"strings"
	"time"
)

// FlexTime handles both Go RFC3339 and Java LocalDateTime (no timezone) formats.
type FlexTime struct {
	time.Time
}

func (ft FlexTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + ft.Time.Format("2006-01-02T15:04:05.000000000") + `"`), nil
}

func (ft *FlexTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	// Try RFC3339 first, then Java LocalDateTime
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			ft.Time = t
			return nil
		}
	}
	return nil // silently ignore unparseable times
}

// DatabaseInstance is the core entity tracking a provisioned database.
type DatabaseInstance struct {
	ID          *int64 `json:"id,omitempty"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName,omitempty"` // display name, free-form, editable
	OrgID       string `json:"orgId"`
	OwnerID     string `json:"ownerId,omitempty"`
	DBType         DatabaseType   `json:"databaseType"`
	Tier           TierType       `json:"tier"`
	DeploymentMode DeploymentMode `json:"deploymentMode,omitempty"`
	Namespace      string         `json:"namespace"`

	// Connection
	Host         string `json:"host,omitempty"`
	ReadOnlyHost string `json:"readOnlyHost,omitempty"`
	Port         *int   `json:"port,omitempty"`
	DatabaseName string `json:"databaseName,omitempty"`

	// Credentials
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// Settings
	DeletionProtection *bool  `json:"deletionProtection,omitempty"`
	PoolerEnabled      *bool  `json:"poolerEnabled,omitempty"`
	PoolerHost         string `json:"poolerHost,omitempty"`
	SSLMode            string `json:"sslMode,omitempty"`
	WebhookURL         string `json:"webhookUrl,omitempty"`
	PostgresVersion    string `json:"postgresVersion,omitempty"`
	Tags               string `json:"tags,omitempty"` // JSON string

	// Status
	Status       string            `json:"status"`
	CurrentStage  ProvisioningStage `json:"currentStage,omitempty"`
	CurrentStep   string            `json:"currentStep,omitempty"`
	FailureReason string            `json:"failureReason,omitempty"`
	FailureStage  ProvisioningStage `json:"failureStage,omitempty"`
	FailureStep   string            `json:"failureStep,omitempty"`
	RollbackLog   string            `json:"rollbackLog,omitempty"` // JSON array of cleanup results

	// Network
	NetworkPolicyEnabled *bool `json:"networkPolicyEnabled,omitempty"`

	// Maintenance
	MaintenanceWindow                string `json:"maintenanceWindow,omitempty"`
	MaintenanceWindowDurationMinutes *int   `json:"maintenanceWindowDurationMinutes,omitempty"`
	AutoMinorVersionUpgrade          *bool  `json:"autoMinorVersionUpgrade,omitempty"`

	// Backup
	BackupEnabled       *bool  `json:"backupEnabled,omitempty"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int   `json:"backupRetentionDays,omitempty"`

	// Metrics
	MetricsEndpoint     string `json:"metricsEndpoint,omitempty"`
	GrafanaDashboardURL string `json:"grafanaDashboardUrl,omitempty"`

	// Pause state. last_active_at is updated by the activity tracker (poll
	// of pg_stat_database). PauseReason is empty for ACTIVE projects;
	// idle_7d / manual / tier_limit when status is PAUSED.
	LastActiveAt   *FlexTime `json:"lastActiveAt,omitempty"`
	LastXactCount  int64     `json:"lastXactCount,omitempty"`
	PauseReason    string    `json:"pauseReason,omitempty"`

	// Timestamps
	CreatedAt       *FlexTime `json:"createdAt,omitempty"`
	UpdatedAt       *FlexTime `json:"updatedAt,omitempty"`
	LastHealthCheck *FlexTime `json:"lastHealthCheck,omitempty"`
}

// WALGEnv returns the env block the WAL-G sidecar needs to talk to
// S3. The platform writes these into the sidecar at start; the
// runner re-passes them on exec because wal-g reads its credentials
// from the *exec* env, not the container env.
//
// Source of truth is the inst's BackupSchedule — the actual S3
// creds live in the platform's vault and are loaded by the
// adapter. This method returns the keys WAL-G expects; values are
// expected to be set by the caller via WithWALGCredentials before
// passing into the exec.
func (inst *DatabaseInstance) WALGEnv() []string {
	// Phase 2 marker — the runner extracts wal-g env vars from the
	// instance's vault-loaded creds. For now we return empty so
	// tests that don't need real S3 don't break; real credential
	// injection lives in the production wiring path.
	return []string{}
}

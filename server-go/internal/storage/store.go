package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// InstanceStore persists database instance metadata and credentials.
type InstanceStore interface {
	Save(instance *domain.DatabaseInstance) error
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
	FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error)
	FindAll() ([]*domain.DatabaseInstance, error)
	Delete(projectID string) error
}

// ParameterGroupStore persists parameter groups.
type ParameterGroupStore interface {
	Save(pg *domain.ParameterGroup) error
	FindByName(name string) (*domain.ParameterGroup, error)
	FindAll() ([]*domain.ParameterGroup, error)
	Delete(name string) error
}

// MetricsStore persists time-series metrics.
type MetricsStore interface {
	Append(ctx context.Context, m *domain.DatabaseMetrics) error
	GetHistory(ctx context.Context, projectID string, limit int) ([]domain.DatabaseMetrics, error)
}

// AlertStore persists alerts.
type AlertStore interface {
	Save(ctx context.Context, alert *domain.Alert) error
	GetActive(ctx context.Context) ([]domain.Alert, error)
	GetActiveForProject(ctx context.Context, projectID string) ([]domain.Alert, error)
	GetHistory(ctx context.Context, limit int) ([]domain.Alert, error)
}

// MigrationRecordStore persists migration records.
type MigrationRecordStore interface {
	Save(ctx context.Context, record *domain.MigrationRecord) error
	ListByProject(ctx context.Context, projectID string) ([]domain.MigrationRecord, error)
}

// BackupRecordStore persists backup records.
type BackupRecordStore interface {
	Save(ctx context.Context, record *domain.BackupRecord) error
	ListByProject(ctx context.Context, projectID string) ([]domain.BackupRecord, error)
	UpdateStatus(ctx context.Context, id, status string) error
}

// BackupScheduleStore persists per-project backup schedules so the
// scheduler can replay them after a platform restart.
type BackupScheduleStore interface {
	UpsertSchedule(ctx context.Context, s *domain.BackupSchedule) error
	ListEnabledSchedules(ctx context.Context) ([]domain.BackupSchedule, error)
	DeleteSchedule(ctx context.Context, projectID string) error
}

// RestoreJobStore persists the async restore state machine. The
// orchestrator reads/writes through here so a platform restart can
// replay (or fail) jobs that were RUNNING.
type RestoreJobStore interface {
	UpsertRestoreJob(ctx context.Context, j *domain.RestoreJob) error
	FindRestoreJob(ctx context.Context, id string) (*domain.RestoreJob, error)
	ListRunningRestoreJobs(ctx context.Context) ([]domain.RestoreJob, error)
}

// UserStore persists users for auth.
type UserStore interface {
	CreateUser(ctx context.Context, user *domain.User) error
	FindUserByID(ctx context.Context, id string) (*domain.User, error)
	FindUserByUsername(ctx context.Context, username string) (*domain.User, error)
	FindUserByEmail(ctx context.Context, email string) (*domain.User, error)
	FindAllUsers(ctx context.Context) ([]*domain.User, error)
	DeleteUser(ctx context.Context, id string) error
	// UpdateUserPassword writes the already-hashed password to the user
	// record by username. Hashing is the caller's responsibility.
	UpdateUserPassword(ctx context.Context, username, passwordHash string) error
}

// TokenStore persists access tokens (PAT pattern).
type TokenStore interface {
	CreateToken(ctx context.Context, token *domain.AccessToken) error
	FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error)
	ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error)
	DeleteToken(ctx context.Context, tokenHash string) error
}

// AuditLogStore persists audit entries.
type AuditLogStore interface {
	Log(ctx context.Context, entry *domain.AuditEntry) error
	Query(ctx context.Context, limit int) ([]domain.AuditEntry, error)
}

// OrgStore persists organizations and memberships.
type OrgStore interface {
	CreateOrg(ctx context.Context, org *domain.Org) error
	FindOrgByID(ctx context.Context, id string) (*domain.Org, error)
	FindOrgBySlug(ctx context.Context, slug string) (*domain.Org, error)
	FindOrgsByUser(ctx context.Context, userID string) ([]*domain.Org, error)
	FindAllOrgs(ctx context.Context) ([]*domain.Org, error)
	UpdateOrg(ctx context.Context, org *domain.Org) error
	DeleteOrg(ctx context.Context, id string) error

	AddOrgMember(ctx context.Context, m *domain.OrgMember) error
	RemoveOrgMember(ctx context.Context, orgID, userID string) error
	UpdateOrgMemberRole(ctx context.Context, orgID, userID, role string) error
	ListOrgMembers(ctx context.Context, orgID string) ([]*domain.OrgMember, error)
	GetOrgMember(ctx context.Context, orgID, userID string) (*domain.OrgMember, error)

	AddProjectMember(ctx context.Context, m *domain.ProjectMember) error
	RemoveProjectMember(ctx context.Context, projectID, userID string) error
	UpdateProjectMemberRole(ctx context.Context, projectID, userID, role string) error
	ListProjectMembers(ctx context.Context, projectID string) ([]*domain.ProjectMember, error)
	GetProjectMember(ctx context.Context, projectID, userID string) (*domain.ProjectMember, error)

	CreatePendingInvite(ctx context.Context, invite *domain.PendingInvite) error
	FindPendingInvitesByEmail(ctx context.Context, email string) ([]*domain.PendingInvite, error)
	DeletePendingInvite(ctx context.Context, id int64) error
	ListPendingInvites(ctx context.Context, orgID string) ([]*domain.PendingInvite, error)
}

// PgDogConfigStore persists PgDog connection pooler configuration.
type PgDogConfigStore interface {
	RegisterPgDogDatabase(ctx context.Context, db *domain.PgDogDatabase) error
	RemovePgDogDatabase(ctx context.Context, name string) error
	RegisterPgDogUser(ctx context.Context, user *domain.PgDogUser) error
	RemovePgDogUser(ctx context.Context, name, database string) error
}

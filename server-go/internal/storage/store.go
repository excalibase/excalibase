package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ErrProjectExists is returned by InstanceStore.Create when the project id is
// already registered. A project id identifies a tenant's database, so a
// collision is always a conflict — never an overwrite of the existing row.
var ErrProjectExists = errors.New("project id already registered")

// ErrProjectNotFound is returned by InstanceStore.Update when there is no row
// to update. Update never inserts: a missing row means the caller is working
// from a stale view.
var ErrProjectNotFound = errors.New("project not found")

// ErrProjectDeleting is returned by Update when the stored row is already
// being torn down. Deletion is a one-way door enforced here rather than by
// convention: a caller that read the row before the teardown started would
// otherwise write it back to a usable status — reviving a project whose
// namespace, cluster and credentials are already going away, and wiping the
// record of how far the teardown got. Only the deletion flow's own narrow
// writes may touch such a row.
var ErrProjectDeleting = errors.New("project is being deleted")

// ErrProjectBusy is returned when a deletion is asked for while the project
// is still being built. The build creates resources and credentials the
// teardown would not see, so the two must not overlap.
var ErrProjectBusy = errors.New("project is busy")

// StaleBuildAfter is how long a PROVISIONING row keeps counting as a live
// build. The pipeline stamps updated_at on every stage, so a running
// provision never reaches it; a row that has not moved for this long belongs
// to a process that is gone, and must stay deletable. Matches the restore
// orchestrator's staleness window.
const StaleBuildAfter = 30 * time.Minute

// CheckNotBuilding refuses a deletion claim while a build is in flight,
// judging "in flight" by how recently the row moved.
func CheckNotBuilding(projectID, status string, updatedAt, now time.Time) error {
	if !domain.IsBuildingStatus(status) {
		return nil
	}
	if now.Sub(updatedAt) >= StaleBuildAfter {
		// The build stopped moving long ago; its process is gone.
		return nil
	}
	return fmt.Errorf("%w: %s is %s", ErrProjectBusy, projectID, status)
}

// ErrProjectNotDeleting is returned by the deletion flow's narrow writes when
// the row they name is not being torn down. They exist only to move a
// teardown forward, so they must never be a second way into a deletion state.
var ErrProjectNotDeleting = errors.New("project is not being deleted")

// ErrBackupPurgeAlreadyConfirmed is returned when a retried deletion asks to
// keep backups that an earlier attempt was already told to purge. The first
// confirmation stands: the objects may already be partly gone, so silently
// switching to "keep" would report a set of backups that no longer exists.
var ErrBackupPurgeAlreadyConfirmed = errors.New("this deletion was already confirmed to delete the project's backups")

// ErrUnsupportedDeploymentMode is returned when a stored instance row names a
// deployment mode the platform does not operate. The platform hosts the
// databases it provisions, so only k8s and docker are operable; anything else
// must fail loudly rather than be read as a managed instance the platform
// would then try to pause, back up or deprovision.
var ErrUnsupportedDeploymentMode = errors.New("database_instances row has a deployment_mode the platform does not operate")

// CheckDeploymentMode reports whether mode names an operable deployment. An
// empty mode predates the column and reads as k8s.
func CheckDeploymentMode(projectID string, mode domain.DeploymentMode) error {
	switch mode {
	case "", domain.ModeK8s, domain.ModeDocker:
		return nil
	default:
		return fmt.Errorf("%w: project %q is %q", ErrUnsupportedDeploymentMode, projectID, mode)
	}
}

// LeaderLease is one holder's claim on a leader lock. Only its holder can
// release it, and releasing twice does nothing.
type LeaderLease interface {
	Release(ctx context.Context) error
	// Valid reports whether the lease still holds what it was given. A
	// holder whose session died must stop believing it leads.
	Valid(ctx context.Context) bool
}

// LeaderLock guards multi-replica scheduling. Acquire hands back a lease
// when this caller now leads, and (nil, false, nil) when another holder
// already does.
type LeaderLock interface {
	Acquire(ctx context.Context) (LeaderLease, bool, error)
}

// InstanceStore persists database instance metadata and credentials.
type InstanceStore interface {
	// Create registers a new project. Returns ErrProjectExists when the
	// project id is taken.
	Create(instance *domain.DatabaseInstance) error
	// Update persists changes to an existing project. It never changes the
	// project's id or its owning org, returns ErrProjectNotFound when the
	// row is absent, and ErrProjectDeleting when the stored row is already
	// being torn down — the write is refused outright rather than reviving
	// a project whose resources are going away.
	Update(instance *domain.DatabaseInstance) error
	// BeginDeletion claims the project for teardown: it moves the row into
	// DELETING and records whether the project's backups are to be purged.
	// deleteBackups nil means the caller expressed no preference, which on a
	// retry inherits the decision the first attempt recorded — a bare retry
	// can never drop a purge someone confirmed. It returns the decision now
	// in force, or ErrBackupPurgeAlreadyConfirmed when the caller asks to
	// keep backups an earlier attempt was told to delete.
	BeginDeletion(projectID string, deleteBackups *bool) (bool, error)
	// RecordDeletionFailure stores how far a teardown got on a row that is
	// already being deleted. status lets the backup step leave its own retry
	// marker. It refuses rows that are not being deleted, so it can never be
	// used to push a live project into a deletion state.
	RecordDeletionFailure(projectID string, status domain.ProvisioningStage, step, reason string) error
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
	FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error)
	FindAll() ([]*domain.DatabaseInstance, error)
	Delete(projectID string) error
}

// ApplyBeginDeletionIntent builds the explicit form of the backup decision
// passed to BeginDeletion.
func ApplyBeginDeletionIntent(delete bool) *bool { return &delete }

// ApplyBeginDeletion applies the claim rules to an in-memory row so every
// store enforces the same one-way door. It reports the backup decision now in
// force. Callers persist the row afterwards.
func ApplyBeginDeletion(inst *domain.DatabaseInstance, deleteBackups *bool) (bool, error) {
	if err := CheckNotBuilding(inst.ProjectID, inst.Status, lastMoved(inst), time.Now()); err != nil {
		return false, err
	}
	effective := inst.DeletionDeleteBackups
	switch {
	case deleteBackups == nil:
		// No preference: the recorded decision stands.
	case *deleteBackups:
		effective = true
	case inst.DeletionDeleteBackups && domain.IsDeletionStatus(inst.Status):
		return false, fmt.Errorf("%w: %s", ErrBackupPurgeAlreadyConfirmed, inst.ProjectID)
	default:
		effective = false
	}
	inst.DeletionDeleteBackups = effective
	inst.Status = string(domain.StatusDeleting)
	inst.CurrentStage = domain.StatusDeleting
	inst.DeletionStep = ""
	inst.DeletionError = ""
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	return effective, nil
}

// ApplyDeletionFailure records a stopped teardown on an in-memory row,
// refusing rows no teardown owns.
func ApplyDeletionFailure(inst *domain.DatabaseInstance, status domain.ProvisioningStage, step, reason string) error {
	if !domain.IsDeletionStatus(inst.Status) {
		return fmt.Errorf("%w: %s is %s", ErrProjectNotDeleting, inst.ProjectID, inst.Status)
	}
	inst.Status = string(status)
	inst.CurrentStage = status
	inst.DeletionStep = step
	inst.DeletionError = reason
	if status == domain.StatusBackupsPendingDelete {
		// The retry marker also carries the reason on failure_reason, where
		// the purge-retry endpoint and the project view already read it.
		inst.FailureReason = reason
	}
	inst.UpdatedAt = &domain.FlexTime{Time: time.Now()}
	return nil
}

// CheckUpdatable reports whether a general Update may write the stored row.
func CheckUpdatable(stored *domain.DatabaseInstance) error {
	if domain.IsDeletionStatus(stored.Status) {
		return fmt.Errorf("%w: %s", ErrProjectDeleting, stored.ProjectID)
	}
	return nil
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
	// FindRestoreJob resolves a job only within projectID — the project the
	// caller is already bound to. An id alone never resolves.
	FindRestoreJob(ctx context.Context, projectID, id string) (*domain.RestoreJob, error)
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
	// UpdateTokenExpiry rewrites expires_at; nil clears it (never expires).
	// Rotation uses it to shorten the old token to its grace window.
	UpdateTokenExpiry(ctx context.Context, tokenHash string, expiresAt *time.Time) error
	// TouchTokenLastUsed records when the token last authenticated a request.
	TouchTokenLastUsed(ctx context.Context, tokenHash string, at time.Time) error
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
	// RemovePgDogUsers drops every user routed to the logical database, so a
	// deprovision never leaves a stale (user, database) pair behind.
	RemovePgDogUsers(ctx context.Context, database string) error
}

// NatsCredentialStore persists the per-principal NATS bus credentials the
// auth_callout responder authenticates against (EXC-324). Only bcrypt
// hashes are kept; the plaintext lives in the principal's k8s Secret.
type NatsCredentialStore interface {
	UpsertNatsCredential(ctx context.Context, principal, projectID, passwordHash string) error
	LookupNatsCredentialHash(ctx context.Context, principal string) (string, bool, error)
	DeleteNatsCredential(ctx context.Context, principal string) error
	DeleteNatsCredentialsForProject(ctx context.Context, projectID string) error
}

// lastMoved is when the row was last written. A row that has never been
// updated is judged by when it was created.
func lastMoved(inst *domain.DatabaseInstance) time.Time {
	if inst.UpdatedAt != nil {
		return inst.UpdatedAt.Time
	}
	if inst.CreatedAt != nil {
		return inst.CreatedAt.Time
	}
	return time.Time{}
}

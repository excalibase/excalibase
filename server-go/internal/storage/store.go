package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// InstanceStore persists database instance metadata and credentials.
type InstanceStore interface {
	Save(instance *domain.DatabaseInstance) error
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
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

// UserStore persists users for auth.
type UserStore interface {
	CreateUser(ctx context.Context, user *domain.User) error
	FindUserByID(ctx context.Context, id string) (*domain.User, error)
	FindUserByUsername(ctx context.Context, username string) (*domain.User, error)
	FindAllUsers(ctx context.Context) ([]*domain.User, error)
	DeleteUser(ctx context.Context, id string) error
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

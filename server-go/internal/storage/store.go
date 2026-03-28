package storage

import "github.com/excalibase/provisioning-poc/internal/domain"

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

package storage

import (
	"fmt"
	"maps"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ApplyDatabaseChoices is the in-memory stores' RecordDatabaseChoices: the
// stored row with the new database's create-time choices written onto it.
func ApplyDatabaseChoices(stored, choices *domain.DatabaseInstance, expected string) (*domain.DatabaseInstance, error) {
	if err := CheckUpdatable(stored); err != nil {
		return nil, err
	}
	if !stored.NoDatabase {
		return nil, fmt.Errorf("%w: %s", ErrProjectHasDatabase, stored.ProjectID)
	}
	if stored.Status != expected {
		return nil, fmt.Errorf("%w: %s is %s, expected %s", ErrProjectStatusChanged, stored.ProjectID, stored.Status, expected)
	}
	updated := stored.Clone()
	updated.DocumentDB = choices.DocumentDB
	updated.StorageClass = choices.StorageClass
	updated.Parameters = maps.Clone(choices.Parameters)
	updated.StorageSize = choices.StorageSize
	return updated, nil
}

// ApplyDatabaseAdded is the in-memory stores' MarkDatabaseAdded.
func ApplyDatabaseAdded(stored *domain.DatabaseInstance) (*domain.DatabaseInstance, error) {
	if err := CheckUpdatable(stored); err != nil {
		return nil, err
	}
	if !stored.NoDatabase {
		return nil, fmt.Errorf("%w: %s", ErrProjectHasDatabase, stored.ProjectID)
	}
	updated := stored.Clone()
	updated.NoDatabase = false
	return updated, nil
}

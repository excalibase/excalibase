package service

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Backup adapters are keyed by (deploy mode, engine) (ADR 0041 decision 6).
// Postgres, the platform's first engine, keeps the mode-keyed map; any other
// engine is looked up here and never falls back onto Postgres's adapter.

type engineAdapterKey struct {
	mode   domain.DeploymentMode
	engine domain.DatabaseType
}

// RegisterEngineAdapter serves projects of engine deployed in mode.
func (s *BackupService) RegisterEngineAdapter(mode domain.DeploymentMode, engine domain.DatabaseType, adapter BackupAdapter) {
	if s.engineAdapters == nil {
		s.engineAdapters = map[engineAdapterKey]BackupAdapter{}
	}
	s.engineAdapters[engineAdapterKey{mode: mode, engine: engine}] = adapter
}

// adapterFor resolves the adapter for a project's mode and engine.
func (s *BackupService) adapterFor(inst *domain.DatabaseInstance) (BackupAdapter, error) {
	if inst.DBType == "" || inst.DBType == domain.PostgreSQL {
		return resolveAdapter(s.adapters, inst)
	}
	mode := inst.DeploymentMode
	if mode == "" {
		mode = domain.ModeK8s
	}
	adapter, ok := s.engineAdapters[engineAdapterKey{mode: mode, engine: inst.DBType}]
	if !ok {
		return nil, ErrUnsupportedBackupMode
	}
	return adapter, nil
}

// everyAdapter names every registered adapter, for wiring and its errors.
func (s *BackupService) everyAdapter() map[string]BackupAdapter {
	all := make(map[string]BackupAdapter, len(s.adapters)+len(s.engineAdapters))
	for mode, adapter := range s.adapters {
		all[string(mode)] = adapter
	}
	for key, adapter := range s.engineAdapters {
		all[fmt.Sprintf("%s %s", key.mode, key.engine)] = adapter
	}
	return all
}

// SupportsInPlaceRestoreFor reports whether inst's database can be replaced
// from its own backups.
func (s *BackupService) SupportsInPlaceRestoreFor(inst *domain.DatabaseInstance) bool {
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return false
	}
	_, ok := adapter.(InPlaceCluster)
	return ok
}

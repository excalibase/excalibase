package main

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// matrixMajorUpgrades answers past the gates; the matrix asserts the gates.
type matrixMajorUpgrades struct{}

func (matrixMajorUpgrades) Status(context.Context, string) (*service.MajorUpgradeStatus, error) {
	return &service.MajorUpgradeStatus{Targets: []string{}, Blockers: []string{}}, nil
}

func (matrixMajorUpgrades) Start(context.Context, string, domain.MajorUpgradeRequest, string) (*domain.MajorUpgrade, error) {
	return nil, service.ErrMajorUpgradeNeedsBackups
}

func TestWireMajorUpgradesNeedsARunStore(t *testing.T) {
	deps := &handlerDeps{}
	stop := wireMajorUpgrades(config.AppConfig{}, nil, majorUpgradeDeps{}, deps)
	stop()
	if deps.majorUpgradeHandler != nil {
		t.Fatal("routes were served with nowhere to record a run")
	}
}

// sweepProbeStore records the recovery sweep asking for running runs.
type sweepProbeStore struct {
	storage.PlatformStore
	storage.MajorUpgradeStore
	listed chan struct{}
}

func (s *sweepProbeStore) RunningMajorUpgrades(context.Context) ([]domain.MajorUpgrade, error) {
	select {
	case s.listed <- struct{}{}:
	default:
	}
	return nil, nil
}

// A run left open must still end after the feature is turned off.
func TestWireMajorUpgradesRecoversWithTheFeatureOff(t *testing.T) {
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runs := &sweepProbeStore{listed: make(chan struct{}, 1)}
	backups := service.NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]service.BackupAdapter{}, "")
	stop := wireMajorUpgrades(config.AppConfig{}, runs, majorUpgradeDeps{store: store, backupSvc: backups}, &handlerDeps{})
	defer stop()
	select {
	case <-runs.listed:
	case <-time.After(5 * time.Second):
		t.Fatal("the recovery sweep did not run with the feature off")
	}
}

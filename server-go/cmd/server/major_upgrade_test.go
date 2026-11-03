package main

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
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

package storage

import (
	"context"
	"errors"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

var (
	// ErrMajorUpgradeRunning refuses a second run while one is unfinished.
	ErrMajorUpgradeRunning = errors.New("a major upgrade of this project is already running")
	// ErrMajorUpgradeNotRunning is a write to a run that has already ended
	// or already converted.
	ErrMajorUpgradeNotRunning = errors.New("the major upgrade is not running")
)

// MajorUpgradeStore keeps each major upgrade run (EXC-668).
type MajorUpgradeStore interface {
	// StartMajorUpgrade records a RUNNING run; ErrMajorUpgradeRunning when
	// the project already has one.
	StartMajorUpgrade(ctx context.Context, run *domain.MajorUpgrade) error
	// UpdateMajorUpgrade writes the run's step, outcome, backup, error,
	// warnings and finish time. Converted is never written here; a run
	// already ended is ErrMajorUpgradeNotRunning.
	UpdateMajorUpgrade(ctx context.Context, run *domain.MajorUpgrade) error
	// TouchMajorUpgrade records that the run's driver is alive.
	TouchMajorUpgrade(ctx context.Context, id string, at time.Time) error
	// MarkMajorUpgradeConverted records that the data now lives on
	// toVersion and moves the project's recorded version in the same
	// transaction; ErrMajorUpgradeNotRunning once already marked.
	MarkMajorUpgradeConverted(ctx context.Context, id, projectID, toVersion string, at time.Time) error
	// LatestMajorUpgrade is the project's most recent run, nil without one.
	LatestMajorUpgrade(ctx context.Context, projectID string) (*domain.MajorUpgrade, error)
	// ConvertedMajorUpgrades are the runs that moved the data, oldest first.
	ConvertedMajorUpgrades(ctx context.Context, projectID string) ([]domain.MajorUpgrade, error)
	// RunningMajorUpgrades are every project's unfinished runs.
	RunningMajorUpgrades(ctx context.Context) ([]domain.MajorUpgrade, error)
}

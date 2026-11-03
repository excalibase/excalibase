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

// MajorUpgradeProject is the project status written in the same
// transaction as its run.
type MajorUpgradeProject struct {
	Status, Step, Reason string
}

// MajorUpgradeStore keeps each major upgrade run (EXC-668). A RUNNING run
// belongs to one owner at a time; every write to it names that owner, and a
// write by anyone else is ErrMajorUpgradeNotRunning.
type MajorUpgradeStore interface {
	// StartMajorUpgrade records a RUNNING run owned by run.Owner;
	// ErrMajorUpgradeRunning when the project already has one.
	StartMajorUpgrade(ctx context.Context, run *domain.MajorUpgrade) error
	// UpdateMajorUpgrade writes the run's step, outcome, backup, error,
	// warnings and finish time, and project when given, in one transaction.
	// Converted and owner are never written here. A converted run ends only
	// as COMPLETED.
	UpdateMajorUpgrade(ctx context.Context, run *domain.MajorUpgrade, project *MajorUpgradeProject) error
	// TouchMajorUpgrade records that the run's owner is alive.
	TouchMajorUpgrade(ctx context.Context, id, owner string, at time.Time) error
	// MarkMajorUpgradeConverted records that the data now lives on
	// toVersion and moves the project's recorded version in the same
	// transaction; ErrMajorUpgradeNotRunning once already marked.
	MarkMajorUpgradeConverted(ctx context.Context, id, projectID, toVersion, owner string, at time.Time) error
	// ClaimMajorUpgrade hands a RUNNING run whose heartbeat is older than
	// staleBefore to owner and returns it as claimed; nil when it is not
	// claimable.
	ClaimMajorUpgrade(ctx context.Context, id, owner string, staleBefore, at time.Time) (*domain.MajorUpgrade, error)
	// LatestMajorUpgrade is the project's most recent run, nil without one.
	LatestMajorUpgrade(ctx context.Context, projectID string) (*domain.MajorUpgrade, error)
	// ConvertedMajorUpgrades are the runs that moved the data, oldest first.
	ConvertedMajorUpgrades(ctx context.Context, projectID string) ([]domain.MajorUpgrade, error)
	// RunningMajorUpgrades are every project's unfinished runs.
	RunningMajorUpgrades(ctx context.Context) ([]domain.MajorUpgrade, error)
}

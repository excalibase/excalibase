package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// StatusUpgrading is a project whose database is being moved to a newer
// major version (EXC-668). Its step says how far the run got.
const StatusUpgrading ProvisioningStage = "UPGRADING"

// Steps of a major upgrade, written to the project's current step.
const (
	MajorUpgradeStepChecking    = "UPGRADE_CHECKING"
	MajorUpgradeStepBackup      = "UPGRADE_BACKUP"
	MajorUpgradeStepConverting  = "UPGRADE_CONVERTING"
	MajorUpgradeStepStarting    = "UPGRADE_STARTING"
	MajorUpgradeStepFinishing   = "UPGRADE_FINISHING"
	MajorUpgradeStepRollingBack = "UPGRADE_ROLLING_BACK"
	// MajorUpgradeStepWaiting: the conversion outlasted its watch; the run
	// stays open and recovery checks it again until it settles.
	MajorUpgradeStepWaiting = "UPGRADE_WAITING"
)

// Outcomes of a major upgrade record.
const (
	MajorUpgradeRunning = "RUNNING"
	// MajorUpgradeCompleted: the data is on the new major (warnings may say
	// what the after-steps could not do).
	MajorUpgradeCompleted = "COMPLETED"
	// MajorUpgradeRolledBack: the data never left the old major.
	MajorUpgradeRolledBack = "ROLLED_BACK"
	// MajorUpgradeFailed: the run stopped somewhere it could not put back by
	// itself; Error says what to do.
	MajorUpgradeFailed = "FAILED"
)

var (
	// ErrMajorUpgradeUnconfirmed refuses a request the caller has not
	// confirmed: a major upgrade cannot be undone in place.
	ErrMajorUpgradeUnconfirmed = errors.New("a major upgrade cannot be undone in place; confirm it to continue")
	// ErrMajorUpgradeTarget refuses a target that is not a bare major version.
	ErrMajorUpgradeTarget = errors.New("targetVersion must be a major version such as 17")
)

var bareMajor = regexp.MustCompile(`^[0-9]{1,4}$`)

// MajorUpgradeRequest is an Admin's request to move the database to TargetVersion.
type MajorUpgradeRequest struct {
	TargetVersion string `json:"targetVersion"`
	Confirm       bool   `json:"confirm"`
}

// Target is the requested major, trimmed.
func (r MajorUpgradeRequest) Target() string { return strings.TrimSpace(r.TargetVersion) }

// Validate checks the shape only; the catalogue decides whether it is offered.
func (r MajorUpgradeRequest) Validate() error {
	if !bareMajor.MatchString(r.Target()) {
		return ErrMajorUpgradeTarget
	}
	if !r.Confirm {
		return ErrMajorUpgradeUnconfirmed
	}
	return nil
}

// MajorUpgrade is one run of a project's major upgrade and how it ended.
type MajorUpgrade struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Engine      string `json:"engine"`
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	Status      string `json:"status"`
	Step        string `json:"step,omitempty"`
	// SafetyBackupID is the backup taken before anything changed.
	SafetyBackupID string `json:"safetyBackupId,omitempty"`
	// Converted: the data now lives on ToVersion. Set once, in the same
	// write that moves the project's recorded version.
	Converted bool `json:"converted"`
	// Error is fixed wording for the user; causes stay in the log.
	Error       string    `json:"error,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	RequestedBy string    `json:"-"`
	StartedAt   time.Time `json:"startedAt"`
	// WritesStoppedAt is when the old database stopped taking writes: a
	// restore point after it and before ConvertedAt is the old one's end.
	WritesStoppedAt *time.Time `json:"writesStoppedAt,omitempty"`
	ConvertedAt     *time.Time `json:"convertedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	// HeartbeatAt is when its driver last said it was alive; recovery takes
	// only a run whose heartbeat has gone stale.
	HeartbeatAt time.Time `json:"-"`
}

// Finished reports whether the run has ended, whichever way.
func (u MajorUpgrade) Finished() bool { return u.Status != MajorUpgradeRunning }

// BackupArchiveName names where backups of a database on major go, for a
// project first created on original: the original major keeps the layout
// every project starts with (""), each later one gets "pg<major>", so an
// upgraded database never writes into the archive of the one it replaced.
func BackupArchiveName(original, major string) string {
	if original == "" || major == "" || major == original {
		return ""
	}
	return "pg" + major
}

package k8s

import (
	"errors"
	"fmt"

	"github.com/robfig/cron/v3"
)

// ErrInvalidBackupSchedule refuses a schedule CloudNativePG would not run as written.
var ErrInvalidBackupSchedule = errors.New("backup schedule must be six cron fields: second minute hour day-of-month month day-of-week")

// CloudNativePG parses with a leading seconds field and an optional day of
// week, so it reads a five-field crontab shifted by one field instead of
// refusing it. Only the full six fields are accepted, so nothing is misread.
var backupScheduleParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ValidateBackupSchedule checks a ScheduledBackup schedule before it reaches CloudNativePG.
func ValidateBackupSchedule(schedule string) error {
	if _, err := backupScheduleParser.Parse(schedule); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidBackupSchedule, schedule, err)
	}
	return nil
}

package k8s

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/robfig/cron/v3"
)

// ErrInvalidBackupSchedule refuses a backup schedule outside the one format
// every route accepts.
var ErrInvalidBackupSchedule = errors.New("invalid backup schedule")

// backupScheduleRule is what a refused schedule is told.
const backupScheduleRule = "use five cron fields (minute hour day-of-month month day-of-week), " +
	"or @hourly, @daily, @weekly or @monthly, such as \"0 2 * * *\""

// backupScheduleParser reads the standard five-field crontab, the one format
// a backup schedule is given in on every route.
var backupScheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// backupDescriptors are the shorthands, as the six fields CloudNativePG reads.
var backupDescriptors = map[string]string{
	"@hourly":  "0 0 * * * *",
	"@daily":   "0 0 0 * * *",
	"@weekly":  "0 0 0 * * 0",
	"@monthly": "0 0 0 1 * *",
}

// ValidateBackupSchedule refuses a schedule that is not five cron fields (or
// a shorthand) or that would back up more often than once an hour.
func ValidateBackupSchedule(schedule string) error {
	_, err := CNPGBackupSchedule(schedule)
	return err
}

// CNPGBackupSchedule validates a five-field schedule and returns it in the
// six-field form CloudNativePG reads, with seconds pinned to zero. A
// five-field schedule handed to CloudNativePG as is would be misread one
// field over.
func CNPGBackupSchedule(schedule string) (string, error) {
	if six, ok := backupDescriptors[schedule]; ok {
		return six, nil
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return "", fmt.Errorf("%w %q: %s", ErrInvalidBackupSchedule, schedule, backupScheduleRule)
	}
	if _, err := backupScheduleParser.Parse(schedule); err != nil {
		return "", fmt.Errorf("%w %q: %s", ErrInvalidBackupSchedule, schedule, backupScheduleRule)
	}
	if minute, err := strconv.Atoi(fields[0]); err != nil || minute < 0 || minute > 59 {
		return "", fmt.Errorf("%w %q: backups run at most once an hour, so the minute must be one number from 0 to 59", ErrInvalidBackupSchedule, schedule)
	}
	return "0 " + strings.Join(fields, " "), nil
}

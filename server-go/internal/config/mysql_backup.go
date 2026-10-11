package config

import (
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// mysqlBackupSchedules bound what a MySQL restore can reach: MOCO has no
// continuous binlog archiving, so the backup interval is the RPO (ADR 0041).
var mysqlBackupSchedules = map[domain.TierType]string{
	domain.Free:       "0 */6 * * *",
	domain.Standard:   "0 * * * *",
	domain.Enterprise: "0 * * * *",
}

// MySQLBackupSchedule is the five-field schedule a MySQL project of tier is
// backed up on; a tier without one is refused, never given a default.
func MySQLBackupSchedule(tier domain.TierType) (string, error) {
	schedule, ok := mysqlBackupSchedules[tier]
	if !ok {
		return "", fmt.Errorf("no MySQL backup schedule for tier %s", tier)
	}
	return schedule, nil
}

// mysqlBackupIntervals are the schedules' periods: a single host takes MySQL
// backups itself and runs one once the last is this old.
var mysqlBackupIntervals = map[domain.TierType]time.Duration{
	domain.Free:       6 * time.Hour,
	domain.Standard:   time.Hour,
	domain.Enterprise: time.Hour,
}

// MySQLBackupInterval is how often a MySQL project of tier is backed up.
func MySQLBackupInterval(tier domain.TierType) (time.Duration, error) {
	interval, ok := mysqlBackupIntervals[tier]
	if !ok {
		return 0, fmt.Errorf("no MySQL backup interval for tier %s", tier)
	}
	return interval, nil
}

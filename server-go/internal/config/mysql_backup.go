package config

import (
	"fmt"

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

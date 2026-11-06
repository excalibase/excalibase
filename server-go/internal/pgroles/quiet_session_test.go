package pgroles

import (
	"strings"
	"testing"
)

func TestQuietSessionCoversEveryWayAStatementReachesTheServerLog(t *testing.T) {
	sql := QuietSessionSQL()
	options := strings.Join(QuietSessionOptions(), " ")
	for _, setting := range []string{"log_statement", "log_min_error_statement", "log_min_duration_statement",
		"log_min_duration_sample", "log_transaction_sample_rate", "log_error_verbosity", "pgaudit.log"} {
		if !strings.Contains(sql, "SET "+setting+" = ") {
			t.Errorf("the session statement does not set %s", setting)
		}
		if !strings.Contains(options, setting+"=") {
			t.Errorf("the connect options do not set %s", setting)
		}
	}
}

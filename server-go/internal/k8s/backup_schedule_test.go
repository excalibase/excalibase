package k8s

import (
	"errors"
	"testing"
)

func TestValidateBackupScheduleAcceptsSixFields(t *testing.T) {
	for _, schedule := range []string{"0 0 2 * * *", "0 30 */6 * * 1-5", "15 0 0 1 * *"} {
		if err := ValidateBackupSchedule(schedule); err != nil {
			t.Errorf("%q: %v", schedule, err)
		}
	}
}

// CloudNativePG reads a schedule with a seconds field first and an optional
// day of week, so a five-field crontab is not refused there: "0 0 * * *"
// runs every hour, not every day. It is refused here instead.
func TestValidateBackupScheduleRefusesWhatCloudNativePGWouldMisread(t *testing.T) {
	for _, schedule := range []string{"", "0 0 * * *", "0 2 * * *", "@daily", "0 0 2 * * * *", "0 0 25 * * *", "every day"} {
		if err := ValidateBackupSchedule(schedule); !errors.Is(err, ErrInvalidBackupSchedule) {
			t.Errorf("%q: err = %v, want ErrInvalidBackupSchedule", schedule, err)
		}
	}
}

func TestBuildScheduledBackupRefusesAnInvalidSchedule(t *testing.T) {
	obj, err := BuildScheduledBackup("p", "ns", "0 0 * * *")
	if !errors.Is(err, ErrInvalidBackupSchedule) || obj != nil {
		t.Fatalf("got %v, %v; want no object and ErrInvalidBackupSchedule", obj, err)
	}
}

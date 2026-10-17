package scheduler

import (
	"strings"
	"testing"
)

func TestValidTargetAndTaskID(t *testing.T) {
	if !ValidTarget("jobs/send", "run") {
		t.Error("a plain module and export were refused")
	}
	for _, bad := range [][2]string{{"../x", "run"}, {"jobs", "a-b"}, {"", "run"}} {
		if ValidTarget(bad[0], bad[1]) {
			t.Errorf("%q/%q was accepted", bad[0], bad[1])
		}
	}
	if !ValidTaskID("abc123") || ValidTaskID("") || ValidTaskID(strings.Repeat("a", maxTaskIDLen+1)) {
		t.Error("task id bounds are wrong")
	}
}

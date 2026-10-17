package pgroles

import (
	"strings"
	"testing"
)

// The warning names the same roles and test the guard refuses on, but only
// warns, so it can run in the transaction that must never abort.
func TestSquatWarningSQLWarnsOnTheGuardsTest(t *testing.T) {
	warning, guard := SquatWarningSQL(), SquatGuardSQL()
	for _, want := range []string{"admin_option", "NOT u.rolsuper", textArray(reservedNames), "RAISE WARNING"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning lacks %q", want)
		}
	}
	if strings.Contains(warning, "RAISE EXCEPTION") {
		t.Fatal("the squat warning must never abort")
	}
	if !strings.Contains(guard, "RAISE EXCEPTION") {
		t.Fatal("the squat guard itself must still refuse")
	}
}

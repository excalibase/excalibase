package platformdb

import (
	"strings"
	"testing"
)

// The claim lease is read off the row, so the column has to exist wherever
// the scheduler tables are defined — including on a database created before
// the lease did, which the ALTER upgrades.
func TestSchedulerDDL_DeclaresTheClaimLeaseColumn(t *testing.T) {
	if !strings.Contains(schedulerDDL, "claimed_at timestamptz") {
		t.Error("the scheduled-functions table has no claimed_at column")
	}
	if !strings.Contains(schedulerDDL, "ADD COLUMN IF NOT EXISTS claimed_at timestamptz") {
		t.Error("a database created before the lease is never upgraded")
	}
}

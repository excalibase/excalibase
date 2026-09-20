package postgres

import (
	"testing"
)

// The pool has to hold the standing leadership claims plus the lifecycle
// operations in flight, so a value too small to do that is refused rather
// than quietly wedging the control plane.
func TestPlatformDBMaxConns(t *testing.T) {
	if got := platformDBMaxConns(); got != defaultPlatformDBMaxConns {
		t.Errorf("unset: got %d, want the default %d", got, defaultPlatformDBMaxConns)
	}

	t.Setenv("PLATFORM_DB_MAX_CONNS", "40")
	if got := platformDBMaxConns(); got != 40 {
		t.Errorf("explicit: got %d, want 40", got)
	}

	for _, raw := range []string{"not-a-number", "0", "-5", "1"} {
		t.Setenv("PLATFORM_DB_MAX_CONNS", raw)
		if got := platformDBMaxConns(); got != defaultPlatformDBMaxConns {
			t.Errorf("%q must fall back to the default, got %d", raw, got)
		}
	}
}

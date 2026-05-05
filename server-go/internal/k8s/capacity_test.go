package k8s

import "testing"

// TestClusterCapacity_HeadroomMath pins the rule that Usable = Allocatable * (100 - headroom%) / 100,
// and Free = Usable - Requested. Allocatable already excludes kube-reserved + system-reserved
// (kubelet does that); HeadroomPercent is our extra cushion on top.
func TestClusterCapacity_HeadroomMath(t *testing.T) {
	cases := []struct {
		name        string
		c           ClusterCapacity
		wantUsable  [2]int64 // cpu milli, mem bytes
		wantFree    [2]int64 // cpu milli, mem bytes
	}{
		{
			name: "zero headroom: usable == allocatable",
			c: ClusterCapacity{
				AllocatableCPUMilli: 24000,
				AllocatableMemBytes: 64 * 1024 * 1024 * 1024,
				RequestedCPUMilli:   2000,
				RequestedMemBytes:   8 * 1024 * 1024 * 1024,
				HeadroomPercent:     0,
			},
			wantUsable: [2]int64{24000, 64 * 1024 * 1024 * 1024},
			wantFree:   [2]int64{22000, 56 * 1024 * 1024 * 1024},
		},
		{
			name: "15% headroom: usable = allocatable * 0.85",
			c: ClusterCapacity{
				AllocatableCPUMilli: 24000,
				AllocatableMemBytes: 64 * 1024 * 1024 * 1024,
				RequestedCPUMilli:   2000,
				RequestedMemBytes:   8 * 1024 * 1024 * 1024,
				HeadroomPercent:     15,
			},
			wantUsable: [2]int64{20400, 58411555225}, // 64Gi * 0.85 = 68719476736*85/100
			wantFree:   [2]int64{18400, 49821620633}, // usable - 8Gi requested
		},
		{
			name: "100% headroom clamped: usable == allocatable (degenerate)",
			c: ClusterCapacity{
				AllocatableCPUMilli: 1000,
				AllocatableMemBytes: 1024,
				HeadroomPercent:     100,
			},
			wantUsable: [2]int64{1000, 1024},
			wantFree:   [2]int64{1000, 1024},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertCapacityCase(t, c.c, c.wantUsable, c.wantFree)
		})
	}
}

// assertCapacityCase validates CPU/memory usable and free values against expectations.
// Memory comparisons allow a 1MB rounding tolerance from integer arithmetic.
func assertCapacityCase(t *testing.T, c ClusterCapacity, wantUsable, wantFree [2]int64) {
	t.Helper()
	if got := c.UsableCPUMilli(); got != wantUsable[0] {
		t.Errorf("UsableCPUMilli: got %d, want %d", got, wantUsable[0])
	}
	assertMemWithTolerance(t, "UsableMemBytes", c.UsableMemBytes(), wantUsable[1])
	if got := c.FreeCPUMilli(); got != wantFree[0] {
		t.Errorf("FreeCPUMilli: got %d, want %d", got, wantFree[0])
	}
	assertMemWithTolerance(t, "FreeMemBytes", c.FreeMemBytes(), wantFree[1])
}

// assertMemWithTolerance fails if |got - want| > 1 MiB.
func assertMemWithTolerance(t *testing.T, field string, got, want int64) {
	t.Helper()
	diff := got - want
	const toleranceMiB = 1024 * 1024
	if diff < -toleranceMiB || diff > toleranceMiB {
		t.Errorf("%s: got %d, want ~%d (diff %d)", field, got, want, diff)
	}
}

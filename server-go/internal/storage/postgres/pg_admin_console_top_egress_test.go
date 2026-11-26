//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"
)

// EXC-844: TopEgress reads container_metrics (single-host monitoring) until the
// net meter is on, then reads app_net_samples only, so there is one source.
func TestAdminConsoleTopEgressSourceFollowsTheNetMeterFlag(t *testing.T) {
	cases := []struct {
		name                 string
		netMeter             bool
		containerRows, meter bool
		wantTx               float64 // 0: no entry
	}{
		{"flag off, nothing recorded", false, false, false, 0},
		{"flag off, host monitoring only", false, true, false, 111},
		{"flag off, meter rows are ignored", false, false, true, 0},
		{"flag on, meter rows", true, false, true, 222},
		{"flag on, host monitoring rows are ignored", true, true, false, 0},
		{"flag on, both: the meter alone", true, true, true, 222},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := consoleFixture(t)
			ctx := context.Background()
			if tc.containerRows {
				if _, err := store.db.ExecContext(ctx, `INSERT INTO container_metrics (timestamp, container_name, project_id, network_tx_bytes_per_sec)
					VALUES (NOW(), 'c1', 'p-cust', 111)`); err != nil {
					t.Fatal(err)
				}
			}
			if tc.meter {
				if _, err := store.db.ExecContext(ctx, `INSERT INTO app_net_samples (ts, project_id, app_id, instances, rx_bps, tx_bps, max_instance_tx_bps)
					VALUES (NOW(), 'p-cust', 'a1', 1, 5, 200, 200), (NOW(), 'p-cust', 'a2', 1, 5, 22, 22),
					       (NOW() - INTERVAL '1 hour', 'p-cust', 'a1', 1, 5, 9999, 9999),
					       (NOW() - INTERVAL '1 hour', 'p-cust', 'gone', 1, 5, 5000, 5000)`); err != nil {
					t.Fatal(err)
				}
			}
			console := NewAdminConsole(store.db).WithNetSamples(func() bool { return tc.netMeter })
			overview, err := console.Overview(ctx, time.Now().Add(-24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantTx == 0 {
				if len(overview.TopEgress) != 0 {
					t.Fatalf("top egress = %+v, want none", overview.TopEgress)
				}
				return
			}
			if len(overview.TopEgress) != 1 || overview.TopEgress[0].ProjectID != "p-cust" || overview.TopEgress[0].EgressBytesPS != tc.wantTx {
				t.Fatalf("top egress = %+v, want p-cust at %v", overview.TopEgress, tc.wantTx)
			}
		})
	}
}

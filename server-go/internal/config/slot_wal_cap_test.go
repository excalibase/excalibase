package config

import "testing"

func TestSlotWALKeepSizeIsAFifthOfTheTierStorage(t *testing.T) {
	cases := map[string]string{"5Gi": "1024MB", "50Gi": "10240MB", "500Gi": "102400MB", "10G": "1907MB"}
	for storage, want := range cases {
		got, err := TierConfig{StorageSize: storage}.SlotWALKeepSize()
		if err != nil || got != want {
			t.Errorf("storage %s: cap = %q, %v; want %q", storage, got, err, want)
		}
	}
}

func TestSlotWALKeepSizeRefusesAnUnusableStorageSize(t *testing.T) {
	for _, storage := range []string{"", "lots", "0", "1Ki"} {
		if got, err := (TierConfig{StorageSize: storage}).SlotWALKeepSize(); err == nil {
			t.Errorf("storage %q: cap %q, want an error", storage, got)
		}
	}
}

func TestEveryBuiltInTierHasASlotWALCap(t *testing.T) {
	for tier, tc := range tiers {
		if _, err := tc.SlotWALKeepSize(); err != nil {
			t.Errorf("tier %s: %v", tier, err)
		}
	}
}

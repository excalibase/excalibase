package config

import (
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
)

// slotWALCapPercent of a tier's storage is the most WAL a replication slot may
// retain. Past it Postgres invalidates the slot instead of filling the disk.
const slotWALCapPercent = 20

const mebibyte = 1 << 20

// SlotWALKeepSize is the tier's max_slot_wal_keep_size, in Postgres MB.
func (t TierConfig) SlotWALKeepSize() (string, error) {
	storage, err := resource.ParseQuantity(t.StorageSize)
	if err != nil {
		return "", fmt.Errorf("storage size %q: %w", t.StorageSize, err)
	}
	capMB := storage.Value() * slotWALCapPercent / 100 / mebibyte
	if capMB < 1 {
		return "", fmt.Errorf("storage size %q leaves no room for a WAL cap", t.StorageSize)
	}
	return strconv.FormatInt(capMB, 10) + "MB", nil
}

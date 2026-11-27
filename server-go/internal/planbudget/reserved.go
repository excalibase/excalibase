package planbudget

import (
	"encoding/json"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Database is what the reservation of one project's database is read from.
type Database struct {
	Status     string
	NoDatabase bool
	PerCopy    Size
	Copies     int64
}

// Reservation is the size one copy of a database reserves and how many copies run.
type Reservation struct {
	PerCopy Size
	Copies  int64
}

// ReservedDatabase is what a database holds against its org's budget. A paused
// or pending-deletion database releases CPU and memory and keeps its disk; one
// being deleted, or that failed, holds nothing.
func ReservedDatabase(db Database) Size {
	if db.Copies <= 0 {
		return Size{}
	}
	switch db.Status {
	case string(domain.StatusDeleting), string(domain.StatusBackupsPendingDelete), string(domain.StageFailed):
		return Size{}
	case string(domain.StatusPaused), string(domain.StatusPendingDeletion):
		return Size{DiskBytes: db.PerCopy.DiskBytes * db.Copies, Services: 1}
	}
	total := db.PerCopy.Times(db.Copies)
	total.Services = 1
	return total
}

// functionsRuntimeSizes is the pod a project's functions share, by plan. It
// mirrors the limits the Deno runtime is rendered with (k8s/deno_runtime.go).
var functionsRuntimeSizes = map[string]Size{
	"FREE":       {CPUMilli: 200, MemoryBytes: 128 << 20, Services: 1},
	"STANDARD":   {CPUMilli: 500, MemoryBytes: 256 << 20, Services: 1},
	"ENTERPRISE": {CPUMilli: 1000, MemoryBytes: 512 << 20, Services: 1},
}

// ReservedFunctions is what a project's functions runtime holds once its first
// function is deployed. An unknown plan holds the smallest, never nothing.
func ReservedFunctions(tier string) Size {
	if size, ok := functionsRuntimeSizes[tier]; ok {
		return size
	}
	return functionsRuntimeSizes["FREE"]
}

// appFacts is the part of an app record a reservation is read from.
type appFacts struct {
	Tier     domain.TierType `json:"tier"`
	Replicas int             `json:"replicas"`
	Status   string          `json:"status"`
	Disk     *struct {
		Size string `json:"size"`
	} `json:"disk"`
}

// ReservedApp reads an app record and says what it holds: the tier's limits for
// every replica while it runs, and its disk for as long as it exists.
func ReservedApp(doc []byte) (Size, error) {
	var facts appFacts
	if err := json.Unmarshal(doc, &facts); err != nil {
		return Size{}, fmt.Errorf("read app record: %w", err)
	}
	return reservedAppFacts(facts)
}

// ReservedAppAs is what the same record would hold if its status were status:
// a deploy of a stopped app, or a resume, is admitted as the app running.
func ReservedAppAs(doc []byte, status string) (Size, error) {
	var facts appFacts
	if err := json.Unmarshal(doc, &facts); err != nil {
		return Size{}, fmt.Errorf("read app record: %w", err)
	}
	facts.Status = status
	return reservedAppFacts(facts)
}

func reservedAppFacts(facts appFacts) (Size, error) {
	if facts.Status == string(domain.StatusDeleting) {
		return Size{}, nil
	}
	size := Size{Services: 1}
	if facts.Disk != nil && facts.Disk.Size != "" {
		disk, err := ParseBytes(facts.Disk.Size)
		if err != nil {
			return Size{}, fmt.Errorf("app disk: %w", err)
		}
		size.DiskBytes = disk
	}
	if facts.Replicas <= 0 || facts.Status == string(domain.StatusPaused) {
		return size, nil
	}
	tier, err := config.GetAppTierConfig(facts.Tier)
	if err != nil {
		return Size{}, err
	}
	cpu, err := ParseCPUMilli(tier.CPULimit)
	if err != nil {
		return Size{}, err
	}
	memory, err := ParseBytes(tier.MemoryLimit)
	if err != nil {
		return Size{}, err
	}
	size.CPUMilli = cpu * int64(facts.Replicas)
	size.MemoryBytes = memory * int64(facts.Replicas)
	return size, nil
}

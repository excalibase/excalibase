// Package storagebudget holds every volume on the platform's storage to a
// share of it (owner, 2026-09-29). Volumes are hard-limited, so the sum of
// what they reserve is exactly what they can ever take; the budget keeps that
// sum within the configured share, leaving the rest for the node itself.
package storagebudget

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ErrExceeded refuses an allocation that would take the platform past its budget.
var ErrExceeded = errors.New("the platform's storage budget would be exceeded")

// ExceededError names what was asked for against the budget.
type ExceededError struct {
	What                                            string
	AddBytes, AllocatedBytes, BudgetBytes, Capacity int64
	Percent                                         int
}

func (e *ExceededError) Error() string {
	// Only what is left: a tenant reads this, and the node's size is not theirs to know.
	left := max(e.BudgetBytes-e.AllocatedBytes, 0)
	return fmt.Sprintf("%v: %s needs %s and %s is left", ErrExceeded, e.What, formatBytes(e.AddBytes), formatBytes(left))
}

func (e *ExceededError) Unwrap() error { return ErrExceeded }

// Source reports the storage and what is allocated from it.
type Source interface {
	Capacity(ctx context.Context) (int64, error)
	StorageAllocated(ctx context.Context) (k8s.StorageAllocation, error)
}

// Budget admits allocations. A nil *Budget admits everything: the install
// does not know its storage (a test cluster), and says so in its report.
type Budget struct {
	source  Source
	percent int
	// mu makes check-then-allocate one step; provisioning runs one replica.
	mu sync.Mutex
}

func New(source Source, percent int) *Budget { return &Budget{source: source, percent: percent} }

// Report is the budget as it stands.
type Report struct {
	Enabled        bool    `json:"enabled"`
	CapacityBytes  int64   `json:"capacityBytes"`
	Percent        int     `json:"percent"`
	BudgetBytes    int64   `json:"budgetBytes"`
	AllocatedBytes int64   `json:"allocatedBytes"`
	TenantBytes    int64   `json:"tenantBytes"`
	PlatformBytes  int64   `json:"platformBytes"`
	PendingBytes   int64   `json:"pendingBytes"`
	FreeBytes      int64   `json:"freeBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}

func (b *Budget) Report(ctx context.Context) (Report, error) {
	if b == nil {
		return Report{}, nil
	}
	capacity, err := b.source.Capacity(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("read the platform's storage capacity: %w", err)
	}
	allocation, err := b.source.StorageAllocated(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("read what the platform's volumes reserve: %w", err)
	}
	budget := capacity * int64(b.percent) / 100
	report := Report{
		Enabled: true, CapacityBytes: capacity, Percent: b.percent, BudgetBytes: budget,
		AllocatedBytes: allocation.Total(), TenantBytes: allocation.TenantBytes,
		PlatformBytes: allocation.PlatformBytes, PendingBytes: allocation.PendingBytes,
		FreeBytes: budget - allocation.Total(),
	}
	if budget > 0 {
		report.UsedPercent = float64(report.AllocatedBytes) * 100 / float64(budget)
	}
	return report, nil
}

// Check refuses addBytes more when they would pass the budget. It is the
// early answer a request gets; Reserve is the enforcement.
func (b *Budget) Check(ctx context.Context, addBytes int64, what string) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.check(ctx, addBytes, what)
}

// Reserve runs allocate only while addBytes more fit, holding the budget until
// it returns, so two allocations cannot both take the last room. allocate must
// create the object the next check counts (a claim, or a cluster's spec).
func (b *Budget) Reserve(ctx context.Context, addBytes int64, what string, allocate func() error) error {
	if b == nil {
		return allocate()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.check(ctx, addBytes, what); err != nil {
		return err
	}
	return allocate()
}

func (b *Budget) check(ctx context.Context, addBytes int64, what string) error {
	if addBytes <= 0 {
		return nil
	}
	report, err := b.Report(ctx)
	if err != nil {
		return err
	}
	if report.AllocatedBytes+addBytes > report.BudgetBytes {
		return &ExceededError{What: what, AddBytes: addBytes, AllocatedBytes: report.AllocatedBytes,
			BudgetBytes: report.BudgetBytes, Capacity: report.CapacityBytes, Percent: report.Percent}
	}
	return nil
}

// FixedCapacity is a capacity given by configuration (STORAGE_NODE_CAPACITY).
type FixedCapacity int64

func (c FixedCapacity) Capacity(context.Context) (int64, error) { return int64(c), nil }

// formatBytes writes whole GiB when it is, otherwise MiB rounded up.
func formatBytes(bytes int64) string {
	const mebibyte, gibibyte = int64(1) << 20, int64(1) << 30
	if bytes > 0 && bytes%gibibyte == 0 {
		return strconv.FormatInt(bytes/gibibyte, 10) + "Gi"
	}
	return strconv.FormatInt((bytes+mebibyte-1)/mebibyte, 10) + "Mi"
}

// Allocator counts what the cluster's volumes reserve.
type Allocator interface {
	StorageAllocated(ctx context.Context) (k8s.StorageAllocation, error)
}

// LVMReader reads an LVM volume group's size.
type LVMReader interface {
	LVMVolumeGroupBytes(ctx context.Context, namespace, volumeGroup string) (int64, error)
}

// LVMCapacity is the volume group every tenant and platform volume is cut from.
type LVMCapacity struct {
	Reader      LVMReader
	Namespace   string
	VolumeGroup string
}

func (c LVMCapacity) Capacity(ctx context.Context) (int64, error) {
	return c.Reader.LVMVolumeGroupBytes(ctx, c.Namespace, c.VolumeGroup)
}

// CapacityReader reads the storage the budget is a share of.
type CapacityReader interface {
	Capacity(ctx context.Context) (int64, error)
}

type clusterSource struct {
	CapacityReader
	Allocator
}

// NewClusterSource budgets the cluster's volumes against capacity.
func NewClusterSource(capacity CapacityReader, allocations Allocator) Source {
	return clusterSource{CapacityReader: capacity, Allocator: allocations}
}

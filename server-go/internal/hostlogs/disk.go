package hostlogs

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"syscall"
	"time"
)

const (
	defaultMinFreePercent = 10
	maxMinFreePercent     = 50
	// resumeMargin is how far above the pause line free space must climb again.
	resumeMargin = 5
	// minFreeBytes pauses a small disk whatever its percentage; resume needs one GiB more.
	minFreeBytes    = uint64(2) << 30
	resumeFreeBytes = minFreeBytes + uint64(1)<<30
	diskCheckEvery  = 30 * time.Second
)

// errDiskLow is a batch held back while the host's disk is nearly full.
var errDiskLow = errors.New("host disk nearly full: log shipping paused")

// DiskGuard pauses every project's shipping while the disk that holds the
// platform's volumes, Loki's included, is nearly full (EXC-678). Lines wait
// in the engine's log meanwhile; nothing is skipped.
type DiskGuard struct {
	path           string
	minFreePercent float64
	statfs         func(path string) (free, total uint64, err error)
	now            func() time.Time

	mu        sync.Mutex
	paused    bool
	checkedAt time.Time
}

// NewDiskGuard watches the filesystem holding path; minFreePercent outside
// 1..50 takes the default of 10.
func NewDiskGuard(path string, minFreePercent int) *DiskGuard {
	percent := float64(minFreePercent)
	switch {
	case minFreePercent < 1:
		percent = defaultMinFreePercent
	case minFreePercent > maxMinFreePercent:
		percent = maxMinFreePercent
	}
	return &DiskGuard{path: path, minFreePercent: percent, statfs: statfsFree, now: time.Now}
}

// Check reads the disk once: a path that cannot be read would never pause.
func (g *DiskGuard) Check() error {
	_, total, err := g.statfs(g.path)
	if err != nil {
		return fmt.Errorf("free space of %s: %w", g.path, err)
	}
	if total == 0 {
		return fmt.Errorf("free space of %s: the filesystem reports no size", g.path)
	}
	return nil
}

// Paused answers whether shipping waits, reading the disk at most every 30 s.
// The read happens outside the lock: a slow mount holds no flush.
func (g *DiskGuard) Paused() bool {
	if g == nil || g.statfs == nil {
		return false
	}
	g.mu.Lock()
	now := g.now()
	if !g.checkedAt.IsZero() && now.Sub(g.checkedAt) < diskCheckEvery {
		paused := g.paused
		g.mu.Unlock()
		return paused
	}
	g.checkedAt = now
	g.mu.Unlock()
	free, total, err := g.statfs(g.path)
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case err != nil:
		// The EXC-670 disk alert still fires; a disk we cannot read keeps the last state.
		log.Printf("host logs: reading free space of %s: %v", g.path, err)
		return g.paused
	case total == 0:
		log.Printf("host logs: %s reports no size", g.path)
		return g.paused
	}
	percent := float64(free) * 100 / float64(total)
	switch {
	case !g.paused && (percent < g.minFreePercent || free < minFreeBytes):
		g.paused = true
		log.Printf("host logs: %.1f%% (%d MiB) free on the host disk; shipping paused until %.0f%% is free", percent, free>>20, g.minFreePercent+resumeMargin)
	case g.paused && percent >= g.minFreePercent+resumeMargin && free >= resumeFreeBytes:
		g.paused = false
		log.Printf("host logs: %.1f%% free on the host disk; shipping resumed", percent)
	}
	return g.paused
}

func statfsFree(path string) (uint64, uint64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, 0, err
	}
	block := uint64(fs.Bsize) //nolint:gosec // a block size is positive
	return fs.Bavail * block, fs.Blocks * block, nil
}

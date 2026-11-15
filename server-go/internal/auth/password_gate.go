package auth

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/excalibase/provisioning-poc/internal/loginguard"
)

// ErrBusy means every password-hashing slot stayed taken for the whole wait.
// Callers answer 503 with Retry-After instead of queueing without bound.
var ErrBusy = errors.New("too many password checks in flight")

const (
	// Share of the container's memory password hashing may hold at once.
	gateMemoryPercent = 25
	// Share of the container's CPUs password hashing may occupy at once.
	gateCPUPercent = 50
	gateWait       = 500 * time.Millisecond
)

// gateReservedPercent is the share of slots only checks against accounts that
// exist may use, so a flood of unknown usernames cannot fill the whole gate.
const gateReservedPercent = 25

// hashGate bounds how many argon2 runs are in flight; a run holds
// argon2Memory and several threads for its whole duration. Runs for unknown
// accounts also draw from the smaller unknown pool, so the reserved slots are
// always free for real accounts.
type hashGate struct {
	slots   chan struct{}
	unknown chan struct{}
	wait    time.Duration
}

func newHashGate(slots int, wait time.Duration) *hashGate {
	return &hashGate{
		slots:   make(chan struct{}, slots),
		unknown: make(chan struct{}, slots-reservedSlots(slots)),
		wait:    wait,
	}
}

// reservedSlots is a quarter of the gate, at least one. A single slot cannot
// be split, so it stays shared and unknown names are served there too.
func reservedSlots(slots int) int {
	if slots < 2 {
		return 0
	}
	return max(slots*gateReservedPercent/100, 1)
}

// run executes fn for a real account's work inside a slot, or returns ErrBusy
// after the wait.
func (g *hashGate) run(fn func()) error { return g.runFor(true, fn) }

// runFor is run for work on behalf of a known account or an unknown name.
func (g *hashGate) runFor(knownAccount bool, fn func()) error {
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	if !knownAccount {
		select {
		case g.unknown <- struct{}{}:
			defer func() { <-g.unknown }()
		case <-timer.C:
			return ErrBusy
		}
	}
	select {
	case g.slots <- struct{}{}:
	case <-timer.C:
		return ErrBusy
	}
	defer func() { <-g.slots }()
	fn()
	return nil
}

// gateSlots sizes the gate as the smaller of a memory share and a CPU share,
// never below one so sign-in always works on a small container. An unknown
// memory limit (0) leaves the CPU share to decide.
func gateSlots(memoryBytes uint64, cpus int) int {
	slots := cpus * gateCPUPercent / 100
	if memoryBytes > 0 {
		byMemory := int(memoryBytes * gateMemoryPercent / 100 / (argon2Memory * 1024))
		if byMemory < slots || slots < 1 {
			slots = byMemory
		}
	}
	return max(slots, 1)
}

var passwordGate atomic.Pointer[hashGate]

func currentGate() *hashGate {
	if g := passwordGate.Load(); g != nil {
		return g
	}
	g := newHashGate(gateSlots(containerMemoryLimit(), runtime.GOMAXPROCS(0)), gateWait)
	if passwordGate.CompareAndSwap(nil, g) {
		return g
	}
	return passwordGate.Load()
}

// ConfigurePasswordGate replaces the gate (zero slots refuses everything) and
// returns a function that puts the previous one back.
func ConfigurePasswordGate(slots int, wait time.Duration) (restore func()) {
	previous := passwordGate.Swap(newHashGate(slots, wait))
	return func() { passwordGate.Store(previous) }
}

// containerMemoryLimit is the cgroup limit, else host memory; 0 when unknown.
func containerMemoryLimit() uint64 {
	if limit := loginguard.ContainerMemoryLimit(); limit > 0 {
		return limit
	}
	return hostMemory()
}

func hostMemory() uint64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, _ := strconv.ParseUint(fields[1], 10, 64)
			return kib * 1024
		}
	}
	return 0
}

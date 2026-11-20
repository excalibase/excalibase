package auth

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

// gateClientPercent is the share of the gate one client address may hold at
// once, so a few addresses cannot fill it. It is the same for every request:
// the gate never looks at whether the account exists, which would tell a
// guesser which names are real by who gets refused.
const gateClientPercent = 25

// hashGate bounds how many argon2 runs are in flight; a run holds
// argon2Memory and several threads for its whole duration, and one client may
// hold at most clientShare of the slots.
type hashGate struct {
	slots     chan struct{}
	wait      time.Duration
	perClient int

	mu      sync.Mutex
	clients map[string]*clientSlots
}

type clientSlots struct {
	held    chan struct{}
	waiting int
}

func newHashGate(slots int, wait time.Duration) *hashGate {
	return &hashGate{
		slots:     make(chan struct{}, slots),
		wait:      wait,
		perClient: clientShare(slots),
		clients:   map[string]*clientSlots{},
	}
}

// clientShare is a quarter of the gate, at least one.
func clientShare(slots int) int {
	return max(slots*gateClientPercent/100, 1)
}

// run executes fn for work that belongs to no client address, bounded by the
// gate alone.
func (g *hashGate) run(fn func()) error { return g.runFor("", fn) }

// runFor executes fn inside a slot on behalf of client (an address, IPv6
// grouped by /64; empty for none), or returns ErrBusy after the wait.
func (g *hashGate) runFor(client string, fn func()) error {
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	if client != "" {
		entry := g.enter(client)
		defer g.leave(client, entry)
		select {
		case entry.held <- struct{}{}:
			defer func() { <-entry.held }()
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

func (g *hashGate) enter(client string) *clientSlots {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.clients[client]
	if !ok {
		entry = &clientSlots{held: make(chan struct{}, g.perClient)}
		g.clients[client] = entry
	}
	entry.waiting++
	return entry
}

// leave forgets a client once nothing of it is waiting or running.
func (g *hashGate) leave(client string, entry *clientSlots) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry.waiting--
	if entry.waiting == 0 {
		delete(g.clients, client)
	}
}

func (g *hashGate) trackedClients() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.clients)
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

// RunInPasswordGate runs fn inside a slot of the shared gate on behalf of
// client, exactly as a password check does. Tests use it to hold slots open and
// saturate the gate; it returns ErrBusy when no slot frees up in time.
func RunInPasswordGate(client string, fn func()) error {
	return currentGate().runFor(client, fn)
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

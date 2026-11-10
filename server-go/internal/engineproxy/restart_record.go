package engineproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Restarts turns the guard on. Rootless Podman has no firewall to wait for and goes without.
type Restarts struct {
	// FirewallContainer must report healthy before a guarded container starts.
	FirewallContainer string
	// StoppedFile keeps the ids stopped on purpose across a reboot: a header, one id per line, an end mark.
	StoppedFile string
}

const (
	stoppedHeader = "excalibase-stopped-tenants 1"
	stoppedEnd    = "end"
)

var stoppedID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type restartGuard struct {
	Restarts
	startTimeout time.Duration
	workers      int
	// stopGrace is how long a stop with no answer may still be going on in the engine.
	stopGrace time.Duration
	// corrupt: the file holds something it cannot read. It is kept for whoever looks, and
	// the supervisor starts nothing, so a stop needs no record and platform stops go on working.
	corrupt bool
	// corruptLogged is the supervisor's alone.
	corruptLogged time.Time
	// waiting is the supervisor's alone.
	waiting bool

	mu      sync.Mutex
	stopped map[string]bool
	// stopping is stops not answered yet; pending is never-started tenants a refused
	// start left wanted, for this process: their callers saw the refusal.
	stopping map[string]time.Time
	pending  map[string]bool
	// adopted is what this process has checked for an older release's policy.
	adopted map[string]bool
	// handedBack is the apps this process has left to the engine's policy.
	handedBack map[string]bool
	// restarted counts the apps' crashes this process restarted.
	restarted map[string]int64
	// starting serialises the supervisor's start of a container with a stop of it, so a stop
	// is never overtaken by a start that read the record first.
	starting map[string]chan struct{}
	// uncertain is a start given up on, which the engine may yet carry out.
	uncertain map[string]bool
}

func newRestartGuard(restarts Restarts) (*restartGuard, error) {
	guard := &restartGuard{Restarts: restarts, startTimeout: superviseStartTimeout, workers: superviseWorkers, stopGrace: superviseStopGrace,
		stopped: map[string]bool{}, stopping: map[string]time.Time{}, pending: map[string]bool{}, adopted: map[string]bool{},
		handedBack: map[string]bool{}, restarted: map[string]int64{}, starting: map[string]chan struct{}{}, uncertain: map[string]bool{}}
	raw, err := os.ReadFile(restarts.StoppedFile)
	if errors.Is(err, os.ErrNotExist) {
		return guard, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", restarts.StoppedFile, err)
	}
	stopped, ok := parseStopped(string(raw))
	guard.stopped, guard.corrupt = stopped, !ok
	// A stop may have been in flight when an earlier run ended: give it the engine's time before reading it as anything else.
	for id := range stopped {
		guard.stopping[id] = time.Now().Add(guard.stopGrace)
	}
	return guard, nil
}

// parseStopped reads a whole record, or reports it cannot.
func parseStopped(raw string) (map[string]bool, bool) {
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	if len(lines) < 2 || lines[0] != stoppedHeader || lines[len(lines)-1] != stoppedEnd {
		return map[string]bool{}, false
	}
	stopped := make(map[string]bool, len(lines)-2)
	for _, id := range lines[1 : len(lines)-1] {
		if !stoppedID.MatchString(id) {
			return map[string]bool{}, false
		}
		stopped[id] = true
	}
	return stopped, true
}

// setStopped records a change first: a stop forgotten across a reboot would look like a crash.
func (g *restartGuard) setStopped(id string, stopped bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.corrupt {
		return nil
	}
	if g.stopped[id] == stopped {
		return nil
	}
	next := make(map[string]bool, len(g.stopped)+1)
	for held := range g.stopped {
		next[held] = true
	}
	if stopped {
		next[id] = true
	} else {
		delete(next, id)
	}
	if err := g.save(next); err != nil {
		return fmt.Errorf("record %s: %w", g.StoppedFile, err)
	}
	g.stopped = next
	return nil
}

func (g *restartGuard) save(stopped map[string]bool) error {
	ids := make([]string, 0, len(stopped))
	for id := range stopped {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	lines := append(append([]string{stoppedHeader}, ids...), stoppedEnd)
	return WriteBridges(g.StoppedFile, lines)
}

func (g *restartGuard) isStopped(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped[id]
}

func (g *restartGuard) beginStopping(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopping[id] = time.Time{}
}

func (g *restartGuard) endStopping(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.stopping, id)
}

// expireStopping gives a stop with no answer the engine's time to finish.
func (g *restartGuard) expireStopping(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopping[id] = time.Now().Add(g.stopGrace)
}

// isStopping: a stop unanswered (no end set) or within its grace.
func (g *restartGuard) isStopping(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.stopping[id]
	return ok && (until.IsZero() || time.Now().Before(until))
}

func (g *restartGuard) want(id string)           { g.setIn(g.pending, id, true) }
func (g *restartGuard) unwant(id string)         { g.setIn(g.pending, id, false) }
func (g *restartGuard) wanted(id string) bool    { return g.holds(g.pending, id) }
func (g *restartGuard) markAdopted(id string)    { g.setIn(g.adopted, id, true) }
func (g *restartGuard) isAdopted(id string) bool { return g.holds(g.adopted, id) }

// holdStarts waits for a start of the container in progress, and keeps others out until the release.
func (g *restartGuard) holdStarts(ctx context.Context, id string) error {
	g.mu.Lock()
	gate, ok := g.starting[id]
	if !ok {
		gate = make(chan struct{}, 1)
		g.starting[id] = gate
	}
	g.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *restartGuard) releaseStarts(id string) {
	g.mu.Lock()
	gate := g.starting[id]
	g.mu.Unlock()
	<-gate
}

func (g *restartGuard) noteRestart(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.restarted[id]++
}

func (g *restartGuard) clearRestarts(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.restarted, id)
	delete(g.uncertain, id)
}

func (g *restartGuard) restartsOf(id string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.restarted[id]
}

func (g *restartGuard) anyRestarts() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.restarted) > 0
}

func (g *restartGuard) setIn(set map[string]bool, id string, on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if on {
		set[id] = true
	} else {
		delete(set, id)
	}
}

func (g *restartGuard) holds(set map[string]bool, id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return set[id]
}

// forget drops what the engine no longer holds.
func (g *restartGuard) forget(held map[string]bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id := range g.restarted {
		if !held[id] {
			delete(g.restarted, id)
		}
	}
	for id, gate := range g.starting {
		if !held[id] && len(gate) == 0 {
			delete(g.starting, id)
		}
	}
	for _, set := range []map[string]bool{g.adopted, g.pending, g.handedBack, g.uncertain} {
		for id := range set {
			if !held[id] {
				delete(set, id)
			}
		}
	}
	for id := range g.stopping {
		if !held[id] {
			delete(g.stopping, id)
		}
	}
	if g.corrupt {
		return nil
	}
	next := make(map[string]bool, len(g.stopped))
	for id := range g.stopped {
		if held[id] {
			next[id] = true
		}
	}
	if len(next) == len(g.stopped) {
		return nil
	}
	if err := g.save(next); err != nil {
		return fmt.Errorf("record %s: %w", g.StoppedFile, err)
	}
	g.stopped = next
	return nil
}

package natsauth

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Sizing for the verification pool. A credential check is a bcrypt
// comparison, so it is CPU-bound: the pool is derived from the processor
// budget rather than from a guess about how many pods reconnect at once.
// Slightly over-subscribing the CPUs keeps a worker ready while another is
// between requests, and the ceiling stops a large node from spawning a pool
// that only adds scheduler pressure.
const (
	workersPerProcessor = 2
	minWorkers          = 8
	maxWorkers          = 64
	// queueDepthPerWorker bounds how many requests may be admitted beyond
	// the running ones. Past that, requests are refused outright instead of
	// being queued into a delay the server would time out anyway.
	queueDepthPerWorker = 8
)

func defaultWorkers() int {
	return clampWorkers(workersPerProcessor * runtime.GOMAXPROCS(0))
}

func clampWorkers(workers int) int {
	if workers < minWorkers {
		return minWorkers
	}
	if workers > maxWorkers {
		return maxWorkers
	}
	return workers
}

// dispatcher runs work on a bounded pool. It is deliberately not a generic
// job queue: every rejection path has to be visible to the caller, because
// for an auth callout a request that cannot be served must be answered with
// a denial rather than left to expire.
type dispatcher struct {
	slots       chan struct{}
	waitBudget  time.Duration
	queueBound  int
	inFlight    atomic.Int64
	pendingWork sync.WaitGroup

	mu       sync.Mutex
	pending  int
	draining bool
}

func newDispatcher(workers, queueCapacity int, waitBudget time.Duration) *dispatcher {
	return &dispatcher{
		slots:      make(chan struct{}, workers),
		waitBudget: waitBudget,
		queueBound: queueCapacity,
	}
}

// run executes served on a pool goroutine. When the pool is draining, the
// queue bound is reached, or no worker frees up within the wait budget,
// rejected is called instead — never both, and never neither.
func (d *dispatcher) run(served, rejected func()) {
	if !d.admit() {
		rejected()
		return
	}
	go func() {
		defer d.release()
		if !d.acquireWorker() {
			rejected()
			return
		}
		defer func() { <-d.slots }()
		d.inFlight.Add(1)
		defer d.inFlight.Add(-1)
		served()
	}()
}

// admit reserves a place in the queue, under the same lock that close uses,
// so no work can be started after a drain has begun.
func (d *dispatcher) admit() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.draining || d.pending >= d.queueBound {
		return false
	}
	d.pending++
	d.pendingWork.Add(1)
	return true
}

func (d *dispatcher) release() {
	d.mu.Lock()
	d.pending--
	d.mu.Unlock()
	d.pendingWork.Done()
}

// acquireWorker waits for a free worker for at most the wait budget, which
// is shorter than the server's authorization timeout so a refusal still
// reaches the connecting client in time.
func (d *dispatcher) acquireWorker() bool {
	timer := time.NewTimer(d.waitBudget)
	defer timer.Stop()
	select {
	case d.slots <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

// close stops admitting work and waits for everything already admitted to
// finish. It is safe to call more than once.
func (d *dispatcher) close() {
	d.mu.Lock()
	d.draining = true
	d.mu.Unlock()
	d.pendingWork.Wait()
}

func (d *dispatcher) inFlightCount() int64 {
	return d.inFlight.Load()
}

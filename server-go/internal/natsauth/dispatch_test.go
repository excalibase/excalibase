package natsauth

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const dispatchWait = 500 * time.Millisecond

func TestDispatcherRunsWorkConcurrentlyUpToItsPoolSize(t *testing.T) {
	const workers = 4
	pool := newDispatcher(workers, 16, dispatchWait)
	defer pool.close()

	var concurrent, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		pool.run(func() {
			defer wg.Done()
			running := concurrent.Add(1)
			for {
				highest := peak.Load()
				if running <= highest || peak.CompareAndSwap(highest, running) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			concurrent.Add(-1)
		}, func() { wg.Done(); t.Error("work was rejected while the pool was free") })
	}
	wg.Wait()

	if peak.Load() < 2 {
		t.Errorf("peak concurrency was %d, want the pool to overlap work", peak.Load())
	}
	if peak.Load() > workers {
		t.Errorf("peak concurrency was %d, want at most %d", peak.Load(), workers)
	}
}

func TestDispatcherRejectsPastItsQueueBound(t *testing.T) {
	pool := newDispatcher(1, 2, dispatchWait)
	// close() drains, so the blocked work must be released before it runs,
	// including when an assertion below ends the test early.
	defer pool.close()
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()

	var served, rejected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		pool.run(func() {
			defer wg.Done()
			served.Add(1)
			<-block
		}, func() {
			defer wg.Done()
			rejected.Add(1)
		})
	}
	release()
	wg.Wait()

	if rejected.Load() == 0 {
		t.Error("nothing was rejected; the queue is unbounded")
	}
	if served.Load()+rejected.Load() != 10 {
		t.Errorf("served %d + rejected %d, want every request accounted for", served.Load(), rejected.Load())
	}
}

func TestDispatcherRejectsWorkItCannotStartWithinTheWaitBudget(t *testing.T) {
	pool := newDispatcher(1, 8, 100*time.Millisecond)
	defer pool.close()
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()

	// Wait until the only worker is genuinely occupied: submitting the
	// second request first would let it win the slot and prove nothing.
	occupied := make(chan struct{})
	reportOccupied := sync.OnceFunc(func() { close(occupied) })
	var wg sync.WaitGroup
	wg.Add(1)
	pool.run(func() {
		defer wg.Done()
		reportOccupied()
		<-block
	}, func() {
		defer wg.Done()
		reportOccupied()
		t.Error("first request rejected while the pool was free")
	})
	<-occupied

	rejected := make(chan struct{}, 1)
	pool.run(func() { t.Error("work ran although the only worker was busy") }, func() { rejected <- struct{}{} })

	select {
	case <-rejected:
	case <-time.After(2 * time.Second):
		t.Fatal("queued work was never rejected; it would outlive the server's authorization timeout")
	}
	release()
	wg.Wait()
}

func TestDispatcherCloseDrainsInFlightWorkAndStopsAdmitting(t *testing.T) {
	pool := newDispatcher(2, 8, dispatchWait)

	finished := make(chan struct{})
	pool.run(func() {
		time.Sleep(100 * time.Millisecond)
		close(finished)
	}, func() { t.Error("work was rejected while the pool was free") })

	pool.close()
	select {
	case <-finished:
	default:
		t.Error("close returned before in-flight work finished")
	}
	if pool.inFlightCount() != 0 {
		t.Errorf("in-flight count is %d after close", pool.inFlightCount())
	}

	ranAfterClose := false
	pool.run(func() { ranAfterClose = true }, func() {})
	if ranAfterClose {
		t.Error("a closed dispatcher admitted new work")
	}
}

func TestDispatcherLeavesNoGoroutinesBehind(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for round := 0; round < 3; round++ {
		pool := newDispatcher(4, 16, dispatchWait)
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			pool.run(func() { defer wg.Done(); time.Sleep(time.Millisecond) }, func() { wg.Done() })
		}
		wg.Wait()
		pool.close()
	}
	// The runtime reclaims exited goroutines promptly, but not instantly.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - baseline; leaked > 0 {
		t.Errorf("goroutine count grew by %d across three dispatcher lifetimes", leaked)
	}
}

func TestDefaultWorkersScalesWithTheProcessorBudgetAndStaysBounded(t *testing.T) {
	workers := defaultWorkers()
	if workers < minWorkers {
		t.Errorf("defaultWorkers() = %d, want at least %d", workers, minWorkers)
	}
	if workers > maxWorkers {
		t.Errorf("defaultWorkers() = %d, want at most %d", workers, maxWorkers)
	}
	if expected := workersPerProcessor * runtime.GOMAXPROCS(0); workers != clampWorkers(expected) {
		t.Errorf("defaultWorkers() = %d, want %d derived from GOMAXPROCS", workers, clampWorkers(expected))
	}
}

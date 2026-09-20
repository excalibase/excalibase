package scheduler

import "context"

// Semaphore bounds how many invocations are in flight. A tenant can write
// millions of due rows; without a bound the sweep would answer that with
// millions of goroutines and runtime calls.
type Semaphore struct {
	slots chan struct{}
}

// NewSemaphore returns a semaphore of n slots. n <= 0 yields a single slot:
// a limiter that allows nothing would stop the scheduler entirely.
func NewSemaphore(n int) *Semaphore {
	if n <= 0 {
		n = 1
	}
	return &Semaphore{slots: make(chan struct{}, n)}
}

// Acquire blocks until a slot is free or the context ends.
func (s *Semaphore) Acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a slot. Calling it without a matching Acquire is a
// programming error and panics on the empty channel read below.
func (s *Semaphore) Release() {
	<-s.slots
}

package provisioner

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock drives a Poller without sleeping: every After() fires immediately
// and advances the clock by the interval it was asked to wait.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func readyPoller(clock *fakeClock, timeout time.Duration) Poller {
	return Poller{Interval: time.Second, Timeout: timeout, Now: clock.Now, After: clock.After}
}

func TestWaitUntilReadyReturnsOnceReady(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	attempts := 0
	err := readyPoller(clock, time.Minute).WaitUntilReady(context.Background(), "cluster", func(context.Context) (bool, error) {
		attempts++
		return attempts == 3, nil
	})
	if err != nil {
		t.Fatalf("WaitUntilReady: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts: got %d, want 3", attempts)
	}
}

func TestWaitUntilReadyTimesOutWhenNeverReady(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	err := readyPoller(clock, 3*time.Second).WaitUntilReady(context.Background(), "cluster", func(context.Context) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("err: got %v, want ErrWaitTimeout", err)
	}
}

func TestWaitUntilReadyStopsOnFatalError(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	boom := errors.New("cluster is unrecoverable")
	attempts := 0
	err := readyPoller(clock, time.Hour).WaitUntilReady(context.Background(), "cluster", func(context.Context) (bool, error) {
		attempts++
		return false, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err: got %v, want %v", err, boom)
	}
	if attempts != 1 {
		t.Errorf("a fatal error must stop the wait, got %d attempts", attempts)
	}
}

func TestWaitUntilReadyHonoursContextCancellation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := readyPoller(clock, time.Hour).WaitUntilReady(ctx, "cluster", func(context.Context) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: got %v, want context.Canceled", err)
	}
}

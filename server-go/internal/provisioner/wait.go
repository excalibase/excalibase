package provisioner

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrWaitTimeout is returned when a resource is still present after the
// poller's budget is spent. Callers match on it to report a teardown as
// incomplete rather than failed outright.
var ErrWaitTimeout = errors.New("timed out waiting for resource")

// Poller bounds a wait: how often to re-check a resource and how long to
// keep trying. Now and After are the only clock access, so tests drive the
// wait without sleeping.
type Poller struct {
	Interval time.Duration
	Timeout  time.Duration
	Now      func() time.Time
	After    func(time.Duration) <-chan time.Time
}

// NewPoller returns a wall-clock poller.
func NewPoller(interval, timeout time.Duration) Poller {
	return Poller{Interval: interval, Timeout: timeout, Now: time.Now, After: time.After}
}

func (p Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p Poller) after(d time.Duration) <-chan time.Time {
	if p.After != nil {
		return p.After(d)
	}
	return time.After(d)
}

// WaitUntilReady polls ready until it reports true. what names the thing
// being waited on and appears in the timeout error. An error from ready is
// fatal and stops the wait immediately: it means the resource has told us it
// will never become ready, so spending the rest of the budget is pointless.
// Transient "cannot tell yet" conditions belong in the false return, not the
// error.
func (p Poller) WaitUntilReady(ctx context.Context, what string, ready func(context.Context) (bool, error)) error {
	deadline := p.now().Add(p.Timeout)
	for {
		ok, err := ready(ctx)
		if err != nil {
			return fmt.Errorf("wait for %s: %w", what, err)
		}
		if ok {
			return nil
		}
		if !p.now().Before(deadline) {
			return fmt.Errorf("%w: %s not ready after %s", ErrWaitTimeout, what, p.Timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w", what, ctx.Err())
		case <-p.after(p.Interval):
		}
	}
}

// WaitUntilClear polls remaining until it reports nothing left. what names
// the thing being torn down and appears in the timeout error along with
// whatever was still there on the last look.
func (p Poller) WaitUntilClear(ctx context.Context, what string, remaining func(context.Context) ([]string, error)) error {
	deadline := p.now().Add(p.Timeout)
	for {
		left, err := remaining(ctx)
		if err != nil {
			return fmt.Errorf("check %s: %w", what, err)
		}
		if len(left) == 0 {
			return nil
		}
		if !p.now().Before(deadline) {
			return fmt.Errorf("%w: %s still holds %v after %s", ErrWaitTimeout, what, left, p.Timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w", what, ctx.Err())
		case <-p.after(p.Interval):
		}
	}
}

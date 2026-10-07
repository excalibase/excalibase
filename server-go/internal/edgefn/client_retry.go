package edgefn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"syscall"
	"time"
)

// DefaultRuntimeRetryBudget bounds how long a call waits for a restarting
// runtime: a secret or allowlist change rolls the pod, and the new one is
// usually serving within a few seconds (EXC-569).
const DefaultRuntimeRetryBudget = 30 * time.Second

const (
	defaultRetryBase = 250 * time.Millisecond
	maxRetryWait     = 2 * time.Second
)

// ErrRuntimeUnreachable means the runtime did not accept the call within the
// retry budget. The wrapped error names the runtime's address: log it, never
// show it to a caller.
var ErrRuntimeUnreachable = errors.New("function runtime unreachable")

// ErrRuntimeTransport means the connection to the runtime failed in a way a
// retry could not or may not repair. Like ErrRuntimeUnreachable, the wrapped
// error is for the log.
var ErrRuntimeTransport = errors.New("function runtime connection failed")

// ErrFunctionNotDeployed means the runtime answered but does not hold the
// function, which is what a restarted runtime looks like until the function
// is deployed to it again.
var ErrFunctionNotDeployed = errors.New("function not deployed in the runtime")

// retryPolicy says which transport failures a call may be sent again after.
type retryPolicy int

const (
	// retryRefused only retries a connection the runtime refused: the
	// request never reached it, so even a non-idempotent call is safe.
	retryRefused retryPolicy = iota
	// retryTransport retries any transport failure, for calls that replace
	// state and may safely land twice.
	retryTransport
)

// SetRetryBudget bounds how long a call waits for the runtime to accept it;
// zero fails on the first refused connection.
func (c *RuntimeClient) SetRetryBudget(budget time.Duration) {
	c.retryBudget = budget
}

// send performs the request, retrying transport failures the policy allows
// with a capped backoff until the client's budget or ctx runs out.
func (c *RuntimeClient) send(ctx context.Context, method, path string, body []byte, policy retryPolicy) (*http.Response, error) {
	deadline := time.Now().Add(c.retryBudget)
	wait := c.retryBase
	for {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf(errCreateRequest, err)
		}
		c.setHeaders(req)
		resp, err := c.http.Do(req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || !retryable(err, policy) {
			return nil, fmt.Errorf("%w: %w", ErrRuntimeTransport, err)
		}
		if time.Now().Add(wait).After(deadline) {
			return nil, fmt.Errorf("%w: %w", ErrRuntimeUnreachable, err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ErrRuntimeTransport, err)
		case <-time.After(wait):
		}
		wait = min(wait*2, maxRetryWait)
	}
}

func retryable(err error, policy retryPolicy) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return policy == retryTransport
}

// notDeployed reports whether a failed invoke means the runtime lacks the
// function: a 404, or the 500 older runtimes answer for an unknown id.
func notDeployed(status int, body []byte, id string) bool {
	if status == http.StatusNotFound {
		return true
	}
	if status != http.StatusInternalServerError {
		return false
	}
	var payload struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Error == "function not found: "+id
}

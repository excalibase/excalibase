//go:build integration

package natsauth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

const (
	// verifierLatency is the cost one credential verification stands in for.
	// bcrypt at DefaultCost is ~70ms on an idle core and an order of
	// magnitude worse on a throttled one; the injected value makes the
	// assertions below depend on the responder's dispatch shape instead of
	// on how busy the machine is.
	verifierLatency = 300 * time.Millisecond
	// loadConnections is the reconnect storm the responder must absorb:
	// every tenant watcher plus the platform services coming back at once.
	loadConnections = 40
	// loadWorkers is pinned so the expected throughput is arithmetic rather
	// than a property of the test machine's core count.
	loadWorkers = 16
	// slowVerificationDelay is long enough that a serial responder cannot
	// hide it behind the other connections. It stays under two seconds
	// because past that the server pings inside the client's connect
	// handshake (see handshakePingRace) and the slow connection fails for a
	// reason that has nothing to do with dispatch.
	slowVerificationDelay = 1500 * time.Millisecond
)

// scriptedVerifier answers from a plaintext table after a per-principal
// delay, and can be told to panic for one principal. It replaces the bcrypt
// comparison so concurrency, not CPU speed, is what these tests measure.
type scriptedVerifier struct {
	mu        sync.Mutex
	passwords map[string]string
	delay     time.Duration
	slow      map[string]time.Duration
	panicOn   string
	entered   chan string
}

// verification is the snapshot one Verify call works from, so the test
// goroutine can rewrite the script without racing the responder's workers.
type verification struct {
	delay       time.Duration
	shouldPanic bool
	password    string
	known       bool
}

func newScriptedVerifier(env *calloutEnv) *scriptedVerifier {
	passwords := make(map[string]string, len(env.passwords))
	for principal, password := range env.passwords {
		passwords[principal] = password
	}
	return &scriptedVerifier{
		passwords: passwords,
		delay:     verifierLatency,
		slow:      map[string]time.Duration{},
		entered:   make(chan string, 4*loadConnections),
	}
}

func (v *scriptedVerifier) Verify(_ context.Context, principal, password string) error {
	script := v.scriptFor(principal)
	if script.shouldPanic {
		panic("scripted verification failure for " + principal)
	}
	time.Sleep(script.delay)
	if !script.known || script.password != password {
		return errDenied
	}
	return nil
}

// scriptFor snapshots the script for one principal and records that its
// verification has started.
func (v *scriptedVerifier) scriptFor(principal string) verification {
	v.mu.Lock()
	defer v.mu.Unlock()
	select {
	case v.entered <- principal:
	default:
	}
	script := verification{delay: v.delay, shouldPanic: principal == v.panicOn}
	if slow, ok := v.slow[principal]; ok {
		script.delay = slow
	}
	script.password, script.known = v.passwords[principal]
	return script
}

func (v *scriptedVerifier) setDelay(delay time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.delay = delay
}

func (v *scriptedVerifier) setSlow(principal string, delay time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.slow[principal] = delay
}

func (v *scriptedVerifier) setPanicOn(principal string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.panicOn = principal
}

// loadPrincipals names one tenant watcher per simulated pod.
func loadPrincipals(count int) []string {
	principals := make([]string, 0, count)
	for i := 0; i < count; i++ {
		principals = append(principals, TenantWatcherPrincipal(fmt.Sprintf("proj-load-%02d", i)))
	}
	return principals
}

// setupScriptedCallout wires an environment whose responder verifies through
// the scripted verifier. The verifier is built inside the factory because it
// needs the passwords the environment mints.
func setupScriptedCallout(t *testing.T, workers, queueCapacity int, principals []string) (*calloutEnv, *Responder, *scriptedVerifier) {
	t.Helper()
	var verifier *scriptedVerifier
	env, responder := setupCalloutEnvWith(t, func(built *calloutEnv) []ResponderOption {
		verifier = newScriptedVerifier(built)
		opts := []ResponderOption{WithVerifier(verifier), WithWorkers(workers)}
		if queueCapacity > 0 {
			opts = append(opts, WithQueueCapacity(queueCapacity))
		}
		return opts
	}, principals...)
	return env, responder, verifier
}

// TestCallout_ConcurrentConnectionsAreNotSerialised is the EXC-414
// regression: one async subscription handled every callout in turn, so a
// reconnect storm queued behind a single verification and the tail of it was
// failed by the server's authorization timeout.
func TestCallout_ConcurrentConnectionsAreNotSerialised(t *testing.T) {
	principals := loadPrincipals(loadConnections)
	env, _, _ := setupScriptedCallout(t, loadWorkers, 0, principals)

	started := time.Now()
	errs := connectAllConcurrently(t, env, principals)
	elapsed := time.Since(started)

	for principal, err := range errs {
		if err != nil {
			t.Errorf("%s could not connect under load: %v", principal, err)
		}
	}
	// Serial dispatch needs loadConnections × verifierLatency (12s here);
	// a pool of loadWorkers needs ceil(40/16) × 300ms = 900ms. The bound is
	// deliberately loose so only the serial shape can breach it.
	if elapsed > ServerAuthTimeout {
		t.Errorf("%d concurrent connections took %v, want under %v", loadConnections, elapsed, ServerAuthTimeout)
	}
	t.Logf("%d concurrent authorizations in %v (%.1f conn/s) at %d workers",
		loadConnections, elapsed, float64(loadConnections)/elapsed.Seconds(), loadWorkers)
}

// connectAllConcurrently dials every principal at the same instant and
// collects the per-principal outcome.
func connectAllConcurrently(t *testing.T, env *calloutEnv, principals []string) map[string]error {
	t.Helper()
	var mu sync.Mutex
	errs := make(map[string]error, len(principals))
	release := make(chan struct{})
	var wg sync.WaitGroup
	for _, principal := range principals {
		wg.Add(1)
		go func(principal string) {
			defer wg.Done()
			<-release
			_, err := env.connectAs(t, principal)
			mu.Lock()
			defer mu.Unlock()
			errs[principal] = err
		}(principal)
	}
	close(release)
	wg.Wait()
	return errs
}

// TestCallout_SlowVerificationDoesNotDelayOthers pins head-of-line blocking
// directly: one verification that takes most of the auth budget must not
// push the connections behind it past it.
func TestCallout_SlowVerificationDoesNotDelayOthers(t *testing.T) {
	slowPrincipal := TenantWatcherPrincipal("proj-slow")
	fastPrincipals := loadPrincipals(4)
	principals := append([]string{slowPrincipal}, fastPrincipals...)

	env, _, verifier := setupScriptedCallout(t, loadWorkers, 0, principals)
	verifier.setSlow(slowPrincipal, slowVerificationDelay)

	slowDone := make(chan error, 1)
	go func() {
		_, err := env.connectAs(t, slowPrincipal)
		slowDone <- err
	}()
	// Wait for the slow verification to be in flight rather than sleeping:
	// only then does a serial responder have something to block behind.
	waitForVerifierEntry(t, verifier, slowPrincipal)

	started := time.Now()
	errs := connectAllConcurrently(t, env, fastPrincipals)
	elapsed := time.Since(started)

	for principal, err := range errs {
		if err != nil {
			t.Errorf("%s was blocked behind a slow verification: %v", principal, err)
		}
	}
	if elapsed > slowVerificationDelay/2 {
		t.Errorf("fast connections took %v while one slow verification ran, want well under %v", elapsed, slowVerificationDelay)
	}
	if err := <-slowDone; err != nil {
		t.Errorf("the slow connection itself was refused: %v", err)
	}
}

// waitForVerifierEntry blocks until the named principal's verification has
// actually started inside the responder.
func waitForVerifierEntry(t *testing.T, verifier *scriptedVerifier, principal string) {
	t.Helper()
	deadline := time.After(ServerAuthTimeout)
	for {
		select {
		case entered := <-verifier.entered:
			if entered == principal {
				return
			}
		case <-deadline:
			t.Fatalf("verification for %s never started", principal)
		}
	}
}

// TestCallout_CloseDrainsInFlightVerifications covers shutdown: a responder
// torn down mid-verification must finish what it already accepted.
func TestCallout_CloseDrainsInFlightVerifications(t *testing.T) {
	principals := loadPrincipals(8)
	env, responder, verifier := setupScriptedCallout(t, loadWorkers, 0, principals)
	verifier.setDelay(time.Second)

	var wg sync.WaitGroup
	for _, principal := range principals {
		wg.Add(1)
		go func(principal string) {
			defer wg.Done()
			_, _ = env.connectAs(t, principal)
		}(principal)
	}
	waitForVerifierEntry(t, verifier, principals[0])

	responder.Close()
	if inFlight := responder.InFlight(); inFlight != 0 {
		t.Errorf("Close returned with %d verifications still running", inFlight)
	}
	wg.Wait()
}

// TestCallout_PanicInOneRequestDeniesOnlyThatConnection proves a bad request
// cannot take the responder down with it.
func TestCallout_PanicInOneRequestDeniesOnlyThatConnection(t *testing.T) {
	poison := TenantWatcherPrincipal("proj-poison")
	healthy := TenantWatcherPrincipal("proj-healthy")
	env, _, verifier := setupScriptedCallout(t, loadWorkers, 0, []string{healthy, poison})
	verifier.setPanicOn(poison)

	if conn, err := env.connectAs(t, poison); err == nil {
		conn.Close()
		t.Error("a request whose verification panicked was authorized")
	}
	if _, err := env.connectAs(t, healthy); err != nil {
		t.Errorf("responder stopped serving after a panicking request: %v", err)
	}
}

// TestCallout_SaturatedPoolDeniesRatherThanAllows is the fail-closed half of
// backpressure: past the queue bound a request is refused, never waved
// through unverified.
func TestCallout_SaturatedPoolDeniesRatherThanAllows(t *testing.T) {
	principals := loadPrincipals(12)
	env, _, verifier := setupScriptedCallout(t, 1, 2, principals)
	verifier.setDelay(time.Second)

	errs := connectAllConcurrently(t, env, principals)
	refused := 0
	for _, err := range errs {
		if err != nil {
			refused++
		}
	}
	if refused == 0 {
		t.Error("a pool of one worker with a queue of two accepted every connection; backpressure is not bounded")
	}
	t.Logf("%d of %d connections refused under a deliberately starved pool", refused, len(principals))
}

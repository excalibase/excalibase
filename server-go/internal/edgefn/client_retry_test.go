package edgefn

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// lateRuntime is a runtime address that refuses connections until start is
// called: what a restarting runtime pod looks like from provisioning.
type lateRuntime struct {
	addr string
	srv  *httptest.Server
}

func newLateRuntime(t *testing.T) *lateRuntime {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	rt := &lateRuntime{addr: addr}
	t.Cleanup(func() {
		if rt.srv != nil {
			rt.srv.Close()
		}
	})
	return rt
}

func (rt *lateRuntime) url() string { return "http://" + rt.addr }

// startAfter begins accepting on the reserved address after delay.
func (rt *lateRuntime) startAfter(t *testing.T, delay time.Duration, handler http.Handler) {
	t.Helper()
	ready := make(chan struct{})
	go func() {
		time.Sleep(delay)
		var ln net.Listener
		var err error
		for i := 0; i < 50; i++ {
			if ln, err = net.Listen("tcp", rt.addr); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			panic(err)
		}
		srv := httptest.NewUnstartedServer(handler)
		srv.Listener = ln
		srv.Start()
		rt.srv = srv
		close(ready)
	}()
	t.Cleanup(func() { <-ready })
}

func fastRetryClient(url string, budget time.Duration) *RuntimeClient {
	client := NewRuntimeClient(url, "s")
	client.retryBudget = budget
	client.retryBase = 20 * time.Millisecond
	return client
}

func TestRuntimeClient_DeployWaitsForARestartingRuntime(t *testing.T) {
	rt := newLateRuntime(t)
	var deploys atomic.Int32
	rt.startAfter(t, 300*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deploys.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))

	err := fastRetryClient(rt.url(), 5*time.Second).Deploy(context.Background(), DeployRequest{ID: testFnID, Code: "x"})
	if err != nil {
		t.Fatalf("deploy to a runtime that came back: %v", err)
	}
	if deploys.Load() != 1 {
		t.Fatalf("deploys = %d, want 1", deploys.Load())
	}
}

func TestRuntimeClient_InvokeWaitsForARestartingRuntime(t *testing.T) {
	rt := newLateRuntime(t)
	rt.startAfter(t, 300*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testContentType, testAppJSON)
		_, _ = w.Write([]byte(`{"status":200,"headers":{},"body":"ok"}`))
	}))

	resp, err := fastRetryClient(rt.url(), 5*time.Second).Invoke(context.Background(), testFnID, InvokeRequest{Method: "POST"})
	if err != nil {
		t.Fatalf("invoke on a runtime that came back: %v", err)
	}
	if resp.Body != "ok" {
		t.Fatalf("body = %q", resp.Body)
	}
}

func TestRuntimeClient_GivesUpAfterTheBudgetAsUnreachable(t *testing.T) {
	rt := newLateRuntime(t)
	start := time.Now()
	err := fastRetryClient(rt.url(), 300*time.Millisecond).Deploy(context.Background(), DeployRequest{ID: testFnID})
	if !errors.Is(err, ErrRuntimeUnreachable) {
		t.Fatalf("err = %v, want ErrRuntimeUnreachable", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("gave up after %s, budget was 300ms", elapsed)
	}
}

func TestRuntimeClient_StopsRetryingWhenTheCallerGivesUp(t *testing.T) {
	rt := newLateRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := fastRetryClient(rt.url(), 10*time.Second).Invoke(ctx, testFnID, InvokeRequest{})
	if err == nil {
		t.Fatal("want an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("kept retrying %s after the caller's deadline", elapsed)
	}
}

// An invoke the runtime received may have run: a broken connection after the
// request was sent is not retried, or a function could run twice.
func TestRuntimeClient_InvokeIsNotRepeatedOnceTheRuntimeHasIt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()

	_, err := fastRetryClient(srv.URL, 2*time.Second).Invoke(context.Background(), testFnID, InvokeRequest{})
	if !errors.Is(err, ErrRuntimeTransport) {
		t.Fatalf("err = %v, want ErrRuntimeTransport", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("invoke sent %d times, want 1", calls.Load())
	}
}

// A deploy replaces the function, so sending it again is safe.
func TestRuntimeClient_DeployIsRepeatedAfterABrokenConnection(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	if err := fastRetryClient(srv.URL, 2*time.Second).Deploy(context.Background(), DeployRequest{ID: testFnID}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("deploy sent %d times, want 2", calls.Load())
	}
}

func TestRuntimeClient_ARuntimeAnswerIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"syntax error"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	err := fastRetryClient(srv.URL, 2*time.Second).Deploy(context.Background(), DeployRequest{ID: testFnID})
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("err = %v, want the runtime's refusal", err)
	}
	if errors.Is(err, ErrRuntimeUnreachable) {
		t.Fatal("a runtime answer is not unreachable")
	}
	if calls.Load() != 1 {
		t.Fatalf("deploy sent %d times, want 1", calls.Load())
	}
}

// A restarted runtime has none of the project's functions until they are
// deployed again. Older runtimes say so with a 500, newer ones with a 404.
func TestRuntimeClient_InvokeReportsAFunctionTheRuntimeDoesNotHave(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"404":                 {http.StatusNotFound, `{"error":"not found"}`},
		"500 function absent": {http.StatusInternalServerError, `{"error":"function not found: test-fn"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := fastRetryClient(srv.URL, time.Second).Invoke(context.Background(), testFnID, InvokeRequest{})
			if !errors.Is(err, ErrFunctionNotDeployed) {
				t.Fatalf("err = %v, want ErrFunctionNotDeployed", err)
			}
		})
	}
}

func TestRuntimeClient_AFunctionErrorIsNotAMissingFunction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"function not found: other-fn"}`))
	}))
	defer srv.Close()
	_, err := fastRetryClient(srv.URL, time.Second).Invoke(context.Background(), testFnID, InvokeRequest{})
	if err == nil || errors.Is(err, ErrFunctionNotDeployed) {
		t.Fatalf("err = %v, want a plain runtime error", err)
	}
}

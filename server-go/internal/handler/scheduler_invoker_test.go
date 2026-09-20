package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// runtimeCall records what the fake runtime received on /invoke/{id}.
type runtimeCall struct {
	Path   string
	Secret string
	Body   string
}

// fakeRuntime answers /invoke/{id} with a programmable status and records
// every request, so a test can assert the scheduler reached the runtime by
// the same route a user invocation takes.
func fakeRuntime(t *testing.T, status int, calls *[]runtimeCall) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, runtimeCall{
			Path:   r.URL.Path,
			Secret: r.Header.Get("X-Runtime-Secret"),
			Body:   string(body),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":200,"body":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// invokerHandler builds a handler whose single runtime is the fake server,
// with one project in the given status.
func invokerHandler(t *testing.T, srv *httptest.Server, projectStatus string) *FunctionHandler {
	t.Helper()
	store := edgefn.NewFunctionStore(t.TempDir())
	instances := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_a": {ProjectID: "proj_a", OrgID: "default", Status: projectStatus},
	}}
	client := edgefn.NewRuntimeClient(srv.URL, "runtime-shared")
	return NewFunctionHandler(store, nil, client, instances, nil, "")
}

func TestSchedulerInvoker_DispatchesThroughTheRuntime(t *testing.T) {
	var calls []runtimeCall
	srv := fakeRuntime(t, http.StatusOK, &calls)
	h := invokerHandler(t, srv, "ACTIVE")

	err := h.SchedulerInvoker().Invoke(context.Background(),
		"proj_a", "jobs", "send", json.RawMessage(`{"to":"ada"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("runtime calls: got %d, want 1", len(calls))
	}
	if calls[0].Path != "/invoke/proj_a__jobs" {
		t.Errorf("path: got %q, want /invoke/proj_a__jobs", calls[0].Path)
	}
	if calls[0].Secret == "" {
		t.Error("runtime call carried no X-Runtime-Secret header")
	}
	// The runtime receives an InvokeRequest envelope whose Body is the
	// v2 call payload `{args}` the export is run with.
	var envelope edgefn.InvokeRequest
	if err := json.Unmarshal([]byte(calls[0].Body), &envelope); err != nil {
		t.Fatalf("decode invoke envelope %q: %v", calls[0].Body, err)
	}
	var body struct {
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(envelope.Body), &body); err != nil {
		t.Fatalf("decode invoke body %q: %v", envelope.Body, err)
	}
	if string(body.Args) != `{"to":"ada"}` {
		t.Errorf("args: got %s, want the scheduled args", body.Args)
	}
}

// A project under teardown or an unconfirmed restore has no runtime to
// dispatch into. The scheduler must be told so it can close the task.
func TestSchedulerInvoker_NotServableProjectIsNotInvoked(t *testing.T) {
	for _, status := range []string{string(domain.StatusDeleting), string(domain.StatusRestoring)} {
		t.Run(status, func(t *testing.T) {
			var calls []runtimeCall
			srv := fakeRuntime(t, http.StatusOK, &calls)
			h := invokerHandler(t, srv, status)

			err := h.SchedulerInvoker().Invoke(context.Background(),
				"proj_a", "jobs", "send", nil)
			if !errors.Is(err, scheduler.ErrNotServable) {
				t.Fatalf("err: got %v, want it to wrap scheduler.ErrNotServable", err)
			}
			if len(calls) != 0 {
				t.Errorf("a project that may not be served was invoked anyway: %v", calls)
			}
		})
	}
}

// A runtime that answers with an error is an ordinary failure: the worker
// owns the retry decision, so the invoker only has to report it.
func TestSchedulerInvoker_RuntimeErrorIsReported(t *testing.T) {
	var calls []runtimeCall
	srv := fakeRuntime(t, http.StatusInternalServerError, &calls)
	h := invokerHandler(t, srv, "ACTIVE")

	err := h.SchedulerInvoker().Invoke(context.Background(), "proj_a", "jobs", "send", nil)
	if err == nil {
		t.Fatal("a failing runtime must be reported as an error")
	}
	if errors.Is(err, scheduler.ErrNotServable) {
		t.Errorf("a runtime failure must not be reported as not-servable: %v", err)
	}
}

// An unknown project cannot be resolved to a runtime at all.
func TestSchedulerInvoker_UnknownProject(t *testing.T) {
	var calls []runtimeCall
	srv := fakeRuntime(t, http.StatusOK, &calls)
	h := invokerHandler(t, srv, "ACTIVE")

	if err := h.SchedulerInvoker().Invoke(context.Background(), "proj_missing", "jobs", "send", nil); err == nil {
		t.Fatal("unknown project: want an error")
	}
}

// The invocation is bounded: a runtime that never answers must not hold a
// worker goroutine forever.
func TestSchedulerInvoker_TimesOut(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked)
	h := invokerHandler(t, srv, "ACTIVE")

	inv := h.SchedulerInvoker()
	inv.timeout = 50 * time.Millisecond
	start := time.Now()
	if err := inv.Invoke(context.Background(), "proj_a", "jobs", "send", nil); err == nil {
		t.Fatal("a runtime that never answers must produce an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("invoke was not bounded: took %s", elapsed)
	}
}

// The runtime answers 200 with the function's own failing response; that is
// still a failed task, not a completed one.
func TestSchedulerInvoker_FunctionErrorStatusIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":500,"body":"boom"}`))
	}))
	defer srv.Close()
	h := invokerHandler(t, srv, "ACTIVE")

	err := h.SchedulerInvoker().Invoke(context.Background(), "proj_a", "jobs", "send", nil)
	if err == nil {
		t.Fatal("a function that answered 500 must fail the task")
	}
}

// The registry the sweep consults is the platform's own function store.
func TestSchedulerFunctions_AnswersFromTheFunctionStore(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	if err := store.Save(&edgefn.Function{
		ID: "jobs", ProjectID: "proj_a", Name: "jobs",
		Files: []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	h := NewFunctionHandler(store, nil, nil, nil, nil, "")
	registry := h.SchedulerFunctions()

	known, err := registry.HasFunction("proj_a", "jobs")
	if err != nil || !known {
		t.Errorf("deployed function: got (%v, %v), want (true, nil)", known, err)
	}
	known, err = registry.HasFunction("proj_a", "never-deployed")
	if err != nil || known {
		t.Errorf("undeployed function: got (%v, %v), want (false, nil)", known, err)
	}
	// Another project's function is not this project's.
	known, err = registry.HasFunction("proj_b", "jobs")
	if err != nil || known {
		t.Errorf("another project's function: got (%v, %v), want (false, nil)", known, err)
	}
}

// Without a store the registry cannot answer, and saying "no such function"
// would be a guess.
func TestSchedulerFunctions_WithoutAStore(t *testing.T) {
	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	if _, err := h.SchedulerFunctions().HasFunction("proj_a", "jobs"); err == nil {
		t.Fatal("a registry with no store must report an error")
	}
}

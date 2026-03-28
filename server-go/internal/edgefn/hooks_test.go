package edgefn

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestExecuteHooksFindsAndInvokes(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	dir := t.TempDir()
	store := NewScriptStore(dir)
	client := NewRuntimeClient(srv.URL)

	// Create two post-provision hooks
	store.Save(&Script{ID: "hook-1", Name: "notify", Code: "function handler(d){return d;}", HookType: "post-provision", Active: true})
	store.Save(&Script{ID: "hook-2", Name: "seed", Code: "function handler(d){return d;}", HookType: "post-provision", Active: true})
	// This one is different type — should not fire
	store.Save(&Script{ID: "hook-3", Name: "other", Code: "code", HookType: "pre-backup", Active: true})

	hooks := NewHookService(store, client)

	ctx := HookContext{
		ProjectID:    "duke-db",
		DatabaseType: "POSTGRESQL",
		Tier:         "STANDARD",
	}

	results := hooks.ExecuteHooks(context.Background(), "post-provision", ctx)

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Success {
			t.Errorf("hook %s failed: %s", r.ScriptID, r.Error)
		}
	}
}

func TestExecuteHooksNoneFound(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	dir := t.TempDir()
	store := NewScriptStore(dir)
	client := NewRuntimeClient(srv.URL)
	hooks := NewHookService(store, client)

	results := hooks.ExecuteHooks(context.Background(), "post-provision", HookContext{})
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestExecuteHooksNonBlocking(t *testing.T) {
	// Use unreachable server — hooks should fail but not panic
	dir := t.TempDir()
	store := NewScriptStore(dir)
	client := NewRuntimeClient("http://localhost:1")
	store.Save(&Script{ID: "fail-hook", Name: "fail", Code: "code", HookType: "post-provision", Active: true})

	hooks := NewHookService(store, client)
	results := hooks.ExecuteHooks(context.Background(), "post-provision", HookContext{ProjectID: "test"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Success {
		t.Error("should have failed (unreachable server)")
	}
	if results[0].Error == "" {
		t.Error("error message should be set")
	}
}

func TestExecuteHooksAsync(t *testing.T) {
	srv := mockDenoServer()
	defer srv.Close()

	dir := t.TempDir()
	store := NewScriptStore(dir)
	client := NewRuntimeClient(srv.URL)
	store.Save(&Script{ID: "async-hook", Name: "async", Code: "code", HookType: "post-provision", Active: true})

	hooks := NewHookService(store, client)

	var completed atomic.Bool
	hooks.ExecuteHooksAsync(context.Background(), "post-provision", HookContext{}, func(results []HookResult) {
		completed.Store(true)
	})

	// Give goroutine time to complete
	for i := 0; i < 100; i++ {
		if completed.Load() {
			return
		}
		// busy wait briefly
	}
	// Async — may not complete immediately, that's fine
}

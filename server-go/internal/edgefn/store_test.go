package edgefn

import (
	"testing"
)

func TestScriptStoreSaveAndGet(t *testing.T) {
	dir := t.TempDir()
	store := NewScriptStore(dir)

	script := &Script{
		ID:       "test-1",
		Name:     "hello",
		Code:     "function handler(d) { return d; }",
		HookType: "custom",
		Active:   true,
	}

	if err := store.Save(script); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("test-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "hello" {
		t.Errorf("name: got %s", got.Name)
	}
	if got.Code != script.Code {
		t.Error("code mismatch")
	}
	if got.Version != 1 {
		t.Errorf("version: got %d, want 1", got.Version)
	}
}

func TestScriptStoreList(t *testing.T) {
	dir := t.TempDir()
	store := NewScriptStore(dir)

	store.Save(&Script{ID: "a", Name: "func-a", Code: "fn()", HookType: "post-provision", Active: true})
	store.Save(&Script{ID: "b", Name: "func-b", Code: "fn()", HookType: "custom", Active: true})
	store.Save(&Script{ID: "c", Name: "func-c", Code: "fn()", HookType: "post-provision", Active: false})

	all, _ := store.List("")
	if len(all) != 3 {
		t.Errorf("List all: got %d, want 3", len(all))
	}

	hooks, _ := store.ListByHookType("post-provision")
	if len(hooks) != 1 {
		t.Errorf("ListByHookType: got %d, want 1 (active only)", len(hooks))
	}
}

func TestScriptStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewScriptStore(dir)

	store.Save(&Script{ID: "del-1", Name: "del", Code: "fn()", Active: true})
	store.Delete("del-1")

	got, _ := store.Get("del-1")
	if got != nil {
		t.Error("should be nil after delete")
	}
}

func TestScriptStoreUpdate(t *testing.T) {
	dir := t.TempDir()
	store := NewScriptStore(dir)

	store.Save(&Script{ID: "upd-1", Name: "v1", Code: "old", Active: true})

	// Update
	store.Save(&Script{ID: "upd-1", Name: "v2", Code: "new", Active: true})

	got, _ := store.Get("upd-1")
	if got.Name != "v2" {
		t.Errorf("name: got %s, want v2", got.Name)
	}
	if got.Version != 2 {
		t.Errorf("version: got %d, want 2", got.Version)
	}
}

func TestScriptStorePersistence(t *testing.T) {
	dir := t.TempDir()
	store1 := NewScriptStore(dir)
	store1.Save(&Script{ID: "persist", Name: "test", Code: "code", Active: true})

	// New store from same dir
	store2 := NewScriptStore(dir)
	got, _ := store2.Get("persist")
	if got == nil {
		t.Fatal("should persist to disk and reload")
	}
	if got.Name != "test" {
		t.Errorf("name: got %s", got.Name)
	}
}

func TestScriptStoreNotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewScriptStore(dir)
	got, _ := store.Get("nope")
	if got != nil {
		t.Error("should be nil")
	}
}

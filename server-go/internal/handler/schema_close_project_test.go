package handler

import (
	"database/sql"
	"testing"
	"time"
)

// EXC-431: the handler keeps one connection per project for ten minutes, and
// until this existed a teardown had no way to ask for it back — so the session
// outlived the request, Postgres would not shut down, and the namespace could
// not terminate.
func TestCloseProjectReleasesTheCachedConnection(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h := &SchemaHandler{connCache: map[string]*connEntry{
		"proj-1": {db: db, created: time.Now()},
	}}

	if err := h.CloseProject("proj-1"); err != nil {
		t.Fatalf("CloseProject: %v", err)
	}
	if _, present := h.connCache["proj-1"]; present {
		t.Error("the entry must be gone from the cache, not just closed")
	}
	if err := db.Ping(); err == nil {
		t.Error("the connection must be closed")
	}
}

// A project nobody has queried holds nothing, and a teardown must not fail
// over that.
func TestCloseProjectIsQuietWhenNothingIsHeld(t *testing.T) {
	h := &SchemaHandler{connCache: map[string]*connEntry{}}

	if err := h.CloseProject("never-queried"); err != nil {
		t.Errorf("CloseProject on an unheld project: %v", err)
	}
}

// The teardown reaches this through the DeletionObserver hook it already
// tells the function handler and the pool opener about, so the close happens
// the moment the project is claimed — before anything asks its Postgres to
// stop.
func TestProjectDeletingClosesTheCachedConnection(t *testing.T) {
	db, _ := sql.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	h := &SchemaHandler{connCache: map[string]*connEntry{
		"proj-1": {db: db, created: time.Now()},
	}}

	var observer interface{ ProjectDeleting(string) } = h
	observer.ProjectDeleting("proj-1")

	if _, present := h.connCache["proj-1"]; present {
		t.Error("a project claimed for teardown must not keep its connection")
	}
	if err := db.Ping(); err == nil {
		t.Error("the connection must be closed")
	}
}

// Closing one project leaves every other project's connection alone.
func TestCloseProjectLeavesOtherProjectsConnected(t *testing.T) {
	kept, _ := sql.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	closed, _ := sql.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	h := &SchemaHandler{connCache: map[string]*connEntry{
		"proj-keep":  {db: kept, created: time.Now()},
		"proj-close": {db: closed, created: time.Now()},
	}}

	if err := h.CloseProject("proj-close"); err != nil {
		t.Fatalf("CloseProject: %v", err)
	}
	if _, present := h.connCache["proj-keep"]; !present {
		t.Fatal("an unrelated project lost its cached connection")
	}
	if err := kept.Close(); err != nil {
		t.Errorf("the kept connection should still have been open: %v", err)
	}
}

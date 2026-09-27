package main

import (
	"os"
	"strings"
	"testing"
)

// EXC-431: every other holder of a tenant connection is a deletion observer;
// this one was not, and a queried project then took 301s to delete and failed.
func TestSchemaHandlerIsRegisteredAsADeletionObserver(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "provSvc.AddDeletionObserver(deps.schemaHandler)") {
		t.Error("a deleted project keeps the schema handler's cached connection, " +
			"which stops its database shutting down")
	}
}

// A paused project's database is down; the schema browser's pool must hear it.
func TestSchemaHandlerIsRegisteredAsAPauseObserver(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "pauseSvc.AddStatusObserver(deps.schemaHandler)") {
		t.Error("a paused project keeps the schema browser's pool open against a stopped database")
	}
}

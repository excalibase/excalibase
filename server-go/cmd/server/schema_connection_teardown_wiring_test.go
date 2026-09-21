package main

import (
	"os"
	"strings"
	"testing"
)

// EXC-431: the schema browser caches one connection per project for ten
// minutes. Every other holder of a tenant connection is registered as a
// deletion observer — the function handler and the pool opener both are — but
// this one was not, so a project that had answered a single query kept our
// session open, its Postgres would not shut down, and the teardown gave up
// after five minutes waiting for the namespace.
//
// Measured before the fix: 11s to delete a project nothing had queried, 301s
// and a failure for one that had.
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

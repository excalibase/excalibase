package main

import (
	"os"
	"strings"
	"testing"
)

// Without the session store a reset is refused outright, so the server must wire it.
func TestResetIsWiredToEndSessions(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "emailTokensHandler.SetSessionStore(sqlStore)") {
		t.Error("password resets would be refused: the reset handler has no session store")
	}
}

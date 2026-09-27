package main

import (
	"os"
	"strings"
	"testing"
)

// Without a public listener the edge would reach a port nothing serves, or
// provisioning would have to guess which peers to believe; it refuses to start.
func TestServerRefusesToStartWithoutThePublicListener(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "cfg.CheckPublicListener()") {
		t.Error("runServer does not check the public listener config at boot")
	}
}

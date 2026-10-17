package sitebuilds

import (
	"fmt"
	"strings"
	"testing"
)

// A build stopped because its connector lost access records that reason, not an internal error.
func TestUserMessage_NamesAnInactiveConnection(t *testing.T) {
	message := userMessage(fmt.Errorf("deploy gate: %w", ErrConnectionInactive))
	if !strings.Contains(message, "can no longer deploy") {
		t.Fatalf("message %q", message)
	}
}

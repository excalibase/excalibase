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

// A build image that did not pull is the image's failure, not the platform's, and keeps the registry's words out.
func TestStartFailure_NamesAnImageThatDidNotPull(t *testing.T) {
	got := startFailure(fmt.Errorf("%w: pull node:22: registry.internal refused", ErrImagePull))
	if got.kind != FailImage || strings.Contains(got.reason, "registry.internal") {
		t.Fatalf("%+v", got)
	}
	if other := startFailure(fmt.Errorf("create network: boom")); other.kind != FailPlatform {
		t.Fatalf("%+v", other)
	}
}

// These refusals reach an app's page too, so none of them may talk about "the site".
func TestSharedRefusals_DoNotCallTheTargetASite(t *testing.T) {
	for _, err := range []error{ErrNotConnected, ErrNotAuthorized, ErrStateInvalid, ErrSingleHost} {
		if strings.Contains(strings.ToLower(err.Error()), "the site") || strings.Contains(strings.ToLower(err.Error()), "this site") ||
			strings.Contains(strings.ToLower(err.Error()), "from the site") {
			t.Errorf("%q names a site", err)
		}
	}
}

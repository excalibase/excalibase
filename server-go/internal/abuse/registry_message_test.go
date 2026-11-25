package abuse

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistryRefusal_NamesTheHostItRefused(t *testing.T) {
	err := &RegistryRefusal{Host: "other.example"}
	if !strings.Contains(err.Error(), "images from other.example are not allowed") || strings.Contains(err.Error(), "ttl.sh") {
		t.Errorf("message = %q, want it to name other.example only", err)
	}
	if !errors.Is(err, ErrBlockedRegistry) {
		t.Error("a refusal must match ErrBlockedRegistry")
	}
}

func TestBlockedRegistry_FollowsTheSharedList(t *testing.T) {
	for _, host := range []string{"ttl.sh"} {
		if !BlockedRegistry(host + "/test:1h") {
			t.Errorf("%s is on the shared list but its images pass", host)
		}
	}
}

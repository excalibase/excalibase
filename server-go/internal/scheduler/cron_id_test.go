package scheduler

import (
	"errors"
	"strings"
	"testing"
)

// A task id must be unguessable or not exist: a generator that cannot reach
// the system's entropy source has to say so, never hand back something
// derived from the clock.
func TestBase32RandID_RefusesWhenEntropyIsUnavailable(t *testing.T) {
	original := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("entropy source unavailable") }
	defer func() { randRead = original }()

	id, err := base32RandID()
	if err == nil {
		t.Fatalf("no entropy: got id %q and no error, want a refusal", id)
	}
	if id != "" {
		t.Errorf("no entropy: got id %q, want empty", id)
	}
}

func TestBase32RandID_ShapeMatchesTheRuntime(t *testing.T) {
	id, err := base32RandID()
	if err != nil {
		t.Fatalf("base32RandID: %v", err)
	}
	if len(id) != 30 {
		t.Errorf("length: got %d, want 30", len(id))
	}
	for _, r := range id {
		if !strings.ContainsRune(base32Alphabet, r) {
			t.Errorf("character %q is outside the alphabet", r)
		}
	}
}

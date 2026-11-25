package domain

import (
	"encoding/json"
	"os"
	"testing"
)

// The Studio's mapping reads the same file, so the two cannot drift apart.
func TestClassifyFailure(t *testing.T) {
	raw, err := os.ReadFile("../../../shared-fixtures/failure-kinds.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text string      `json:"text"`
		Kind FailureKind `json:"kind"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("fixture: %v (%d cases)", err, len(cases))
	}
	for _, tc := range cases {
		if got := ClassifyFailure(tc.Text); got != tc.Kind {
			t.Errorf("ClassifyFailure(%q) = %q, want %q", tc.Text, got, tc.Kind)
		}
	}
}

func TestClassifyFailureOfNothingIsNoKind(t *testing.T) {
	for _, text := range []string{"", "  \n"} {
		if got := ClassifyFailure(text); got != "" {
			t.Errorf("ClassifyFailure(%q) = %q, want no kind", text, got)
		}
	}
}

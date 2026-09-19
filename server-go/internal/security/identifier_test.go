package security

import (
	"strings"
	"testing"
)

func TestValidateIdentifier_Accepts(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"project id", "proj-abc123"},
		{"snapshot id", "test-db-20260919-120000"},
		{"parameter group", "pg1"},
		{"underscores", "high_memory_group"},
		{"inner dot", "postgres16.tuned"},
		{"single character", "a"},
		{"at the length limit", strings.Repeat("x", maxIdentifierLength)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateIdentifier(c.input); err != nil {
				t.Errorf("ValidateIdentifier(%q) = %v, want nil", c.input, err)
			}
		})
	}
}

func TestValidateIdentifier_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"current directory", "."},
		{"parent directory", ".."},
		{"leading traversal", "../snapshots/other-project"},
		{"embedded traversal", "snapshots/../../etc/passwd"},
		{"forward slash", "snapshots/other"},
		{"backslash", "snapshots\\other"},
		{"NUL byte", "name\x00.json"},
		{"leading dot", ".hidden"},
		{"dot segment without separator", "a..b"},
		{"space", "two words"},
		{"shell metacharacter", "name;rm"},
		{"newline", "name\nmore"},
		{"percent encoding", "%2e%2e"},
		{"over the length limit", strings.Repeat("x", maxIdentifierLength+1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateIdentifier(c.input); err == nil {
				t.Errorf("ValidateIdentifier(%q) = nil, want an error", c.input)
			}
		})
	}
}

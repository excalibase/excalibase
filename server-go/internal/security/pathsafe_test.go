package security

import "testing"

// TestSafePathComponent_Valid pins the rule that opaque IDs (proj-*, fn IDs,
// JSON file basenames) pass through unchanged. If this fails, downstream
// callers (function_store, backup, snapshot, parameter_groups) will start
// rejecting their own valid filesystem entries.
func TestSafePathComponent_Valid(t *testing.T) {
	cases := []string{
		"proj-abc123",
		"users.json",
		"backup-20260504-083343.json",
		"a",
		"with-hyphens-and-digits-9",
		"00000010000000000000003.gz",
	}
	for _, c := range cases {
		got, err := SafePathComponent(c)
		if err != nil {
			t.Errorf("%q should pass: %v", c, err)
			continue
		}
		if got != c {
			t.Errorf("%q: got %q, want unchanged", c, got)
		}
	}
}

// TestSafePathComponent_Rejects pins the security boundary. Adding a
// rejected case here means a class of attack we want to refuse at the
// file-IO boundary even if upstream validation slips.
func TestSafePathComponent_Rejects(t *testing.T) {
	bad := []struct {
		in  string
		why string
	}{
		{"", "empty"},
		{".", "current dir"},
		{"..", "parent dir"},
		{"../escape", "leading traversal"},
		{"foo/../bar", "embedded traversal"},
		{"path/with/slash", "forward slash"},
		{"path\\with\\backslash", "backslash"},
		{"null\x00inject", "NUL byte"},
		{"a/b", "minimal slash"},
	}
	for _, c := range bad {
		_, err := SafePathComponent(c.in)
		if err == nil {
			t.Errorf("%q (%s) should fail", c.in, c.why)
		}
	}
}

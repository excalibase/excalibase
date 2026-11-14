package loginguard

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLimit(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "limit")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadMemoryLimit(t *testing.T) {
	if got := readMemoryLimit([]string{writeLimit(t, "1073741824\n")}); got != 1<<30 {
		t.Fatalf("limit = %d", got)
	}
	for name, content := range map[string]string{"unlimited v2": "max\n", "unlimited v1": "9223372036854771712\n", "junk": "x", "zero": "0"} {
		if got := readMemoryLimit([]string{writeLimit(t, content)}); got != 0 {
			t.Errorf("%s: limit = %d, want 0", name, got)
		}
	}
	if got := readMemoryLimit([]string{"/nonexistent/memory.max", writeLimit(t, "2048")}); got != 2048 {
		t.Fatalf("a missing first file must fall through to the next, got %d", got)
	}
	_ = ContainerMemoryLimit()
}

func TestUnknownNameBound(t *testing.T) {
	if got := UnknownNameBound(1<<30, 0.03); got != 189483 {
		t.Fatalf("1 GiB bound = %d", got)
	}
	if got := UnknownNameBound(1<<20, 0.03); got != minUnknownNames {
		t.Fatalf("a tiny limit must keep the floor, got %d", got)
	}
	if got := UnknownNameBound(0, 0.03); got < minUnknownNames {
		t.Fatalf("no limit read gave %d", got)
	}
}

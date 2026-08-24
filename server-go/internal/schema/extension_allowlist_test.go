package schema

import "testing"

// EXC-349: dblink / postgres_fdw / file_fdw turn a tenant DB into a network
// pivot (SSRF + cross-namespace reach on a flat network). They must not be
// installable even by a project owner. The allowlist is deny-by-default.
func TestIsExtensionAllowed(t *testing.T) {
	denied := []string{"dblink", "postgres_fdw", "file_fdw", "plpython3u", "plperlu", "DBLINK", "  dblink  "}
	for _, name := range denied {
		if IsExtensionAllowed(name) {
			t.Errorf("IsExtensionAllowed(%q) = true, want false (dangerous extension must be blocked)", name)
		}
	}
	allowed := []string{"uuid-ossp", "pgcrypto", "citext", "pg_trgm", "postgis", "vector"}
	for _, name := range allowed {
		if !IsExtensionAllowed(name) {
			t.Errorf("IsExtensionAllowed(%q) = false, want true (safe extension must be permitted)", name)
		}
	}
	if IsExtensionAllowed("") {
		t.Error("IsExtensionAllowed(\"\") = true, want false")
	}
}

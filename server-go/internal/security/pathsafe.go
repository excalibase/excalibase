package security

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafePathComponent returns name unchanged if it's a valid single path
// component (no separators, no traversal), or an error if it could escape
// the intended parent directory.
//
// Use this for any string that flows into a filesystem path:
//
//	name, err := security.SafePathComponent(userInput)
//	if err != nil { ... }
//	path := filepath.Join(baseDir, name)
//
// Even for sources that are "trusted" (server-generated IDs, ReadDir
// entries — the latter is always already a basename per Go's spec), call
// this at the file-IO boundary as defense-in-depth. SAST tools (Snyk,
// Sonar) flag any user-traceable string flowing into os.ReadFile/WriteFile
// without an explicit sanitizer; this is the canonical sanitizer.
//
// Rules (intentionally strict — buckets, project IDs, function IDs are
// all opaque IDs in our system; none should ever contain a separator):
//
//   - empty string → error
//   - "." or ".." → error
//   - contains '/' or '\' → error
//   - contains a NUL byte → error (defense against Go-specific tricks)
func SafePathComponent(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("path component is empty")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("path component is traversal: %q", name)
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("path component contains separator: %q", name)
	}
	// filepath.IsLocal (Go 1.20+) is the canonical "this path stays in
	// the current directory tree" check. Snyk + Sonar recognize it as
	// a sanitizer so this call also clears their data-flow taint
	// analysis at the call site.
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("path component is not local: %q", name)
	}
	return name, nil
}

package edgefn

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxCodeSize caps the bundled code at 512 KB (Deno runtime agrees).
const MaxCodeSize = 512 * 1024

// MaxFileCount caps files per function.
const MaxFileCount = 50

// validIDPattern — function IDs: 1-64 lowercase alphanumeric/hyphen/underscore.
var validIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]{0,63}$`)

// validPathPattern — file paths inside a function: relative, no traversal,
// lowercase letters, digits, hyphen, underscore, forward slash, dot.
// Max 128 chars. Used for index.ts, utils.ts, _shared/cors.ts, etc.
var validPathPattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9_\-./]{0,127}$`)

// ValidateID checks the function id is safe for filesystem, URL, and runtime use.
func ValidateID(id string) error {
	if !validIDPattern.MatchString(id) {
		return fmt.Errorf("invalid function id: must be 1-64 lowercase alphanumeric/hyphen/underscore starting with a letter or digit")
	}
	return nil
}

// ValidateProjectID reuses the same rules the platform generates (proj_<10 chars>),
// but is permissive enough to accept "default" or UUID-style ids too.
func ValidateProjectID(pid string) error {
	return validateProjectID(pid)
}

func validateProjectID(pid string) error {
	if pid == "" {
		return fmt.Errorf("project id is required")
	}
	if len(pid) > 64 {
		return fmt.Errorf("project id must be 64 characters or fewer")
	}
	if strings.ContainsAny(pid, "/\\..") {
		return fmt.Errorf("project id contains invalid characters")
	}
	return nil
}

// File is a single source file belonging to a function. Entry point is "index.ts".
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Function is a per-project edge function — Supabase-style, multi-file, HTTP handler.
type Function struct {
	ID          string `json:"id"`        // slug, unique per project
	ProjectID   string `json:"projectId"` // opaque project ref (e.g. proj_a3k9fx)
	Name        string `json:"name"`      // display name
	Description string `json:"description,omitempty"`
	Files       []File `json:"files"` // must contain index.ts
	// VerifyJwt: nil/missing → true (safe default).
	// Set to *false to opt out — public route lets unauthenticated traffic through
	// straight to the user handler. Use only for webhooks that do their own auth.
	VerifyJwt *bool     `json:"verifyJwt,omitempty"`
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// JwtVerificationRequired returns true unless the function has explicitly opted
// out via VerifyJwt=false. Default-on is the safer choice so that newly created
// functions are not accidentally exposed without auth.
func (f *Function) JwtVerificationRequired() bool {
	if f.VerifyJwt == nil {
		return true
	}
	return *f.VerifyJwt
}

// Validate checks the function is structurally sound. Also bundles files to
// confirm the bundle fits within MaxCodeSize before the function is persisted.
func (f *Function) Validate() error {
	if err := validateProjectID(f.ProjectID); err != nil {
		return err
	}
	if err := ValidateID(f.ID); err != nil {
		return err
	}
	if f.Name == "" || len(f.Name) > 200 {
		return fmt.Errorf("name must be 1-200 characters")
	}
	if len(f.Files) == 0 {
		return fmt.Errorf("function must have at least one of its files (index.ts required)")
	}
	if len(f.Files) > MaxFileCount {
		return fmt.Errorf("function has too many files (max %d)", MaxFileCount)
	}

	hasIndex := false
	for _, file := range f.Files {
		if !validPathPattern.MatchString(file.Path) {
			return fmt.Errorf("invalid file path %q: only lowercase alphanumerics, _, -, /, . allowed", file.Path)
		}
		if strings.Contains(file.Path, "..") {
			return fmt.Errorf("invalid file path %q: path traversal not allowed", file.Path)
		}
		if file.Path == "index.ts" {
			hasIndex = true
		}
		if len(file.Content) == 0 {
			return fmt.Errorf("file %q is empty", file.Path)
		}
	}
	if !hasIndex {
		return fmt.Errorf("function must contain an index.ts entry point")
	}

	if _, err := f.Bundle(); err != nil {
		return err
	}
	return nil
}

// RuntimeID is the id under which this function is registered in the shared
// Deno runtime. Prefixing with the project ref prevents cross-project collisions.
func (f *Function) RuntimeID() string {
	return f.ProjectID + "__" + f.ID
}

// Bundle produces the JS source shipped to the Deno runtime. Strategy:
//  1. Inline all non-index files first, in user-provided order.
//  2. Inline index.ts last.
//  3. Strip relative imports (assume inlined helpers are in the same scope).
//  4. Strip the `export` keyword from named exports (they become module-scoped vars).
//  5. Rewrite `export default X` into `globalThis.__excalibase_default = X`.
//
// Limitations (intentional for MVP):
//   - No TS type erasure — Deno Workers handle TS natively.
//   - No real ES module linking — users must not rely on module-scoped-only semantics.
//   - No `import * as X from ...` — named imports only (they get stripped).
func (f *Function) Bundle() (string, error) {
	var indexFile *File
	var shared []File
	for i := range f.Files {
		if f.Files[i].Path == "index.ts" {
			fx := f.Files[i]
			indexFile = &fx
		} else {
			shared = append(shared, f.Files[i])
		}
	}
	if indexFile == nil {
		return "", fmt.Errorf("function must contain an index.ts entry point")
	}

	var buf strings.Builder
	for _, sf := range shared {
		buf.WriteString("// --- file: ")
		buf.WriteString(sf.Path)
		buf.WriteString(" ---\n")
		buf.WriteString(transformSharedFile(sf.Content))
		buf.WriteString("\n")
	}
	buf.WriteString("// --- file: index.ts ---\n")
	buf.WriteString(transformIndexFile(indexFile.Content))
	buf.WriteString("\n")

	code := buf.String()
	if len(code) > MaxCodeSize {
		return "", fmt.Errorf("bundled code exceeds maximum size (%d KB)", MaxCodeSize/1024)
	}
	return code, nil
}

// relativeImportRE matches `import ... from './path'` and `import './path'`.
var relativeImportRE = regexp.MustCompile(`(?m)^import\s+(?:[^;\n'"]*?\s+from\s+)?['"]\.[^'"\n]*['"];?\s*$`)

// namedExportRE matches `export const|let|var|function|async function|class|interface|type` at line start.
var namedExportRE = regexp.MustCompile(`(?m)^export\s+(const|let|var|function|async\s+function|class|interface|type)\b`)

// defaultExportRE matches `export default X` (handler variant) across a line.
// Captures the value expression after `export default`.
var defaultExportRE = regexp.MustCompile(`(?m)^export\s+default\s+`)

func transformSharedFile(src string) string {
	// Drop relative imports (their exports are inlined)
	src = relativeImportRE.ReplaceAllString(src, "")
	// Strip `export ` from named exports
	src = namedExportRE.ReplaceAllString(src, "$1")
	// Shared files should not contain `export default`, but if they do, keep it harmless
	src = defaultExportRE.ReplaceAllString(src, "const __shared_default = ")
	return src
}

func transformIndexFile(src string) string {
	// Drop relative imports (helpers are already inlined above)
	src = relativeImportRE.ReplaceAllString(src, "")
	// Strip `export ` from named exports
	src = namedExportRE.ReplaceAllString(src, "$1")
	// Rewrite `export default X` → `globalThis.__excalibase_default = X`
	src = defaultExportRE.ReplaceAllString(src, "globalThis.__excalibase_default = ")
	return src
}

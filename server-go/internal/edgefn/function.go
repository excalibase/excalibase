package edgefn

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

// v2ShapePattern matches the tagged FunctionDef record produced by the
// excalibase SDK (kind: "query" | "mutation" | "action"). Whitespace
// around the colon is tolerated — esbuild emits `kind: "query"` (with
// space), and hand-written user code can omit the space. We deliberately
// don't try to verify args/handler here; the runtime does the structural
// check at dispatch time. This is a quick post-bundle marker, not a parser.
var v2ShapePattern = regexp.MustCompile(`kind\s*:\s*"(query|mutation|action)"`)

// RuntimeShape values written into Function.RuntimeShape after Bundle().
// Older persisted records may have an empty string — readers must treat
// "" as equivalent to "v1" (legacy Fetch handler shape).
const (
	RuntimeShapeV1 = "v1"
	RuntimeShapeV2 = "v2"
)

const defaultEntrypoint = "index.ts"


// MaxCodeSize caps the bundled code at 512 KB (Deno runtime agrees).
const MaxCodeSize = 512 * 1024

// MaxFileCount caps files per function.
const MaxFileCount = 50

// validIDPattern — function IDs: 1-64 lowercase alphanumeric/hyphen/underscore,
// optionally suffixed with `.<export>` to model the SDK's
// `db.functions.<module>.<export>` namespace. Exactly one dot is allowed and
// the export segment must itself be a non-empty alphanumeric/hyphen/underscore
// token. Leading dots, trailing dots, adjacent dots, and chained dots are
// rejected (TestValidateID_RejectsBadDotPlacement covers the matrix).
var validIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]*(?:\.[a-z0-9][a-z0-9_\-]*)?$`)

// validPathPattern — file paths inside a function: relative, no traversal,
// lowercase letters, digits, hyphen, underscore, forward slash, dot.
// Max 128 chars. Used for index.ts, utils.ts, _shared/cors.ts, etc.
var validPathPattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9_\-./]{0,127}$`)

// maxFunctionIDLength is enforced separately so the regex stays readable.
const maxFunctionIDLength = 64

// ValidateID checks the function id is safe for filesystem, URL, and runtime use.
func ValidateID(id string) error {
	if len(id) == 0 || len(id) > maxFunctionIDLength {
		return fmt.Errorf("invalid function id: must be 1-%d characters", maxFunctionIDLength)
	}
	if !validIDPattern.MatchString(id) {
		return fmt.Errorf("invalid function id: must be lowercase alphanumeric/hyphen/underscore with an optional single dot separator")
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

// File is a single source file belonging to a function. Entry point is defaultEntrypoint.
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
	VerifyJwt *bool `json:"verifyJwt,omitempty"`
	Active    bool  `json:"active"`
	Version   int   `json:"version"`
	// RuntimeShape — populated by Bundle() based on the emitted JS:
	//   "v1" → legacy default-export Fetch handler (req: Request) => Response
	//   "v2" → tagged FunctionDef with kind: "query" | "mutation" | "action"
	// Empty string on legacy persisted records is treated as "v1" by readers.
	RuntimeShape string `json:"runtimeShape,omitempty"`
	// ExportMetadata — opaque JSON array reported back by the Deno runtime
	// after a v2 worker boots and scans its loaded module for tagged
	// FunctionDef records. Shape is `[{name, kind, argsJsonSchema}]`. Stored
	// verbatim so the SDK codegen endpoint can return it without re-parsing.
	// `omitempty` keeps v1 records (and freshly created v2 records before
	// the runtime callback fires) clean on the wire.
	ExportMetadata json.RawMessage `json:"exportMetadata,omitempty"`
	// SchemaJSON — opaque JSON object emitted by the @excalibase/server lib
	// when the user's bundle calls `defineSchema(...)`. Captured by the
	// bundler via the globalThis.__excalibase_schema side-channel and stored
	// on the Function so the migrator can apply it on deploy. Empty/nil on
	// bundles that do not declare a schema.
	SchemaJSON json.RawMessage `json:"schemaJson,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
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
	if err := validateFileSet(f.Files); err != nil {
		return err
	}
	if _, err := f.Bundle(); err != nil {
		return err
	}
	return nil
}

// validateFileSet validates count, paths, content, and entry-point presence.
func validateFileSet(files []File) error {
	if len(files) == 0 {
		return fmt.Errorf("function must have at least one of its files (index.ts required)")
	}
	if len(files) > MaxFileCount {
		return fmt.Errorf("function has too many files (max %d)", MaxFileCount)
	}
	hasIndex := false
	for _, file := range files {
		if err := validateFilePath(file.Path); err != nil {
			return err
		}
		if file.Path == defaultEntrypoint {
			hasIndex = true
		}
		if len(file.Content) == 0 {
			return fmt.Errorf("file %q is empty", file.Path)
		}
	}
	if !hasIndex {
		return fmt.Errorf("function must contain an index.ts entry point")
	}
	return nil
}

// validateFilePath checks that a single file path is safe for use in the function.
func validateFilePath(p string) error {
	if !validPathPattern.MatchString(p) {
		return fmt.Errorf("invalid file path %q: only lowercase alphanumerics, _, -, /, . allowed", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("invalid file path %q: path traversal not allowed", p)
	}
	return nil
}

// RuntimeID is the id under which this function is registered in the shared
// Deno runtime. Prefixing with the project ref prevents cross-project collisions.
func (f *Function) RuntimeID() string {
	return f.ProjectID + "__" + f.ID
}

// Bundle produces the JS source shipped to the Deno runtime. Uses esbuild
// with a virtual filesystem plugin so the user's source files never touch
// disk. Entry point is always index.ts. Output format is IIFE assigned to
// a global, then a trailer assigns the default export to
// globalThis.__excalibase_default — which is the contract the worker
// template in deno-server/server.ts reads from.
//
// External imports (npm:, jsr:, node:, http:, https:) pass through to the
// Deno runtime unchanged — Deno resolves and fetches them at worker start.
// Relative imports (./utils.ts, ../shared/cors.ts) are resolved from the
// virtual filesystem provided in f.Files.
func (f *Function) Bundle() (string, error) {
	virtualFiles := make(map[string]string, len(f.Files))
	hasIndex := false
	for _, file := range f.Files {
		virtualFiles[file.Path] = file.Content
		if file.Path == defaultEntrypoint {
			hasIndex = true
		}
	}
	if !hasIndex {
		return "", fmt.Errorf("function must contain an index.ts entry point")
	}

	result := esbuild.Build(esbuild.BuildOptions{
		EntryPoints: []string{defaultEntrypoint},
		Bundle:      true,
		Format:      esbuild.FormatIIFE,
		GlobalName:  "__excalibase_bundle",
		Target:      esbuild.ES2022,
		Platform:    esbuild.PlatformNeutral,
		Write:       false,
		LogLevel:    esbuild.LogLevelSilent,
		Plugins:     []esbuild.Plugin{virtualFSPlugin(virtualFiles)},
	})
	if len(result.Errors) > 0 {
		return "", fmt.Errorf("bundle error: %s", result.Errors[0].Text)
	}
	if len(result.OutputFiles) == 0 {
		return "", fmt.Errorf("bundle produced no output")
	}

	// Append the handler hoist. esbuild IIFE with GlobalName emits:
	//   var __excalibase_bundle = (() => { ... return index_exports; })();
	// where `index_exports.default` is the user's default export.
	bundled := string(result.OutputFiles[0].Contents)
	final := bundled + "\nglobalThis.__excalibase_default = __excalibase_bundle && __excalibase_bundle.default;\n"
	// Metadata collector slot — the worker template reads
	// globalThis.__excalibase_export_metadata after module load and posts
	// it back to main. Initialising the slot here (rather than relying on
	// the worker template alone) lets the bundler tests assert the
	// contract end-to-end. Phase 3 codegen consumes the result via the
	// _metadata endpoint.
	final += "globalThis.__excalibase_export_metadata = globalThis.__excalibase_export_metadata || [];\n"

	if len(final) > MaxCodeSize {
		return "", fmt.Errorf("bundled code exceeds maximum size (%d KB)", MaxCodeSize/1024)
	}

	// Stamp the detected shape on the receiver. This is the only place that
	// writes RuntimeShape — persistence stores it, runtime codegen reads it.
	// Pragmatic substring/regex scan: esbuild's IIFE output is non-minified
	// and preserves the source object literal verbatim, so the kind marker
	// survives unchanged. Phase 3 codegen consumes this field.
	if v2ShapePattern.MatchString(final) {
		f.RuntimeShape = RuntimeShapeV2
	} else {
		f.RuntimeShape = RuntimeShapeV1
	}

	return final, nil
}

// virtualFSPlugin returns an esbuild plugin that resolves and loads modules
// from an in-memory file map. External protocols (npm:, jsr:, node:, http://,
// https://) pass through as external so Deno resolves them at worker start.
func virtualFSPlugin(files map[string]string) esbuild.Plugin {
	return esbuild.Plugin{
		Name: "excalibase-virtual-fs",
		Setup: func(build esbuild.PluginBuild) {
			build.OnResolve(esbuild.OnResolveOptions{Filter: `.*`},
				func(args esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
					// Let Deno handle remote and runtime modules.
					if isExternal(args.Path) {
						return esbuild.OnResolveResult{Path: args.Path, External: true}, nil
					}

					resolved := args.Path
					if args.Importer != "" && (strings.HasPrefix(args.Path, "./") || strings.HasPrefix(args.Path, "../")) {
						importerDir := path.Dir(args.Importer)
						resolved = path.Clean(path.Join(importerDir, args.Path))
					}
					resolved = strings.TrimPrefix(resolved, "./")

					if _, ok := files[resolved]; ok {
						return esbuild.OnResolveResult{Path: resolved, Namespace: "virtual"}, nil
					}
					return esbuild.OnResolveResult{}, fmt.Errorf("module %q not found in function bundle (importer=%q)", args.Path, args.Importer)
				})

			build.OnLoad(esbuild.OnLoadOptions{Filter: `.*`, Namespace: "virtual"},
				func(args esbuild.OnLoadArgs) (esbuild.OnLoadResult, error) {
					contents, ok := files[args.Path]
					if !ok {
						return esbuild.OnLoadResult{}, fmt.Errorf("file %q not in virtual fs", args.Path)
					}
					loader := loaderForExt(args.Path)
					return esbuild.OnLoadResult{
						Contents: &contents,
						Loader:   loader,
					}, nil
				})
		},
	}
}

func isExternal(p string) bool {
	return strings.HasPrefix(p, "npm:") ||
		strings.HasPrefix(p, "jsr:") ||
		strings.HasPrefix(p, "node:") ||
		strings.HasPrefix(p, "http://") ||
		strings.HasPrefix(p, "https://")
}

func loaderForExt(p string) esbuild.Loader {
	switch {
	case strings.HasSuffix(p, ".tsx"):
		return esbuild.LoaderTSX
	case strings.HasSuffix(p, ".jsx"):
		return esbuild.LoaderJSX
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"):
		return esbuild.LoaderJS
	default:
		return esbuild.LoaderTS
	}
}

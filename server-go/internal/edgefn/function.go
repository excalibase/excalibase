package edgefn

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/dop251/goja"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

// v2ShapePattern matches the tagged FunctionDef record produced by the
// excalibase SDK (kind: "query" | "mutation" | "action" | "httpAction" |
// "httpRouter"). Whitespace around the colon is tolerated — esbuild emits
// `kind: "query"` (with space), and hand-written user code can omit the
// space. We deliberately don't try to verify args/handler here; the
// runtime does the structural check at dispatch time. This is a quick
// post-bundle marker, not a parser.
var v2ShapePattern = regexp.MustCompile(`kind\s*:\s*"(query|mutation|action|httpAction|httpRouter)"`)

// httpKindPattern picks the specific kind for httpAction / httpRouter so
// Function.Kind reflects exactly what the bundle declared.
var httpKindPattern = regexp.MustCompile(`kind\s*:\s*"(httpAction|httpRouter)"`)

// httpRoutesPattern extracts the `__excalibase_routes: [...]` literal the
// httpRouter() helper emits at deploy time. We capture the array contents
// so the bundler can persist them on Function.HttpRoutes. Greedy match is
// intentional: routes are sourced from a single literal array, not from
// dynamic code, so the closing bracket nearest the opener is always the
// right one. The (?s) flag lets `.` match newlines for multi-line tables.
var httpRoutesPattern = regexp.MustCompile(`(?s)__excalibase_routes\s*:\s*(\[[^\[\]]*?\])`)

// cronJobsMarker is a cheap pre-check: if the bundled JS doesn't contain
// the side-channel slot string, we don't bother spinning up a goja VM.
// The slot is written by @excalibase/server's `cronJobs()` registry on
// every registration, and by user bundles that publish it directly.
const cronJobsMarker = "__excalibase_crons"

// validCronScheduleKinds bounds the schedule.kind values written into
// Function.CronJobs. The library's `cronJobs()` only emits these four
// kinds — anything else fails the bundle so the runner never receives
// an unknown shape.
var validCronScheduleKinds = map[string]bool{
	"cron":     true,
	"interval": true,
	"daily":    true,
	"hourly":   true,
}

// validHttpMethods bounds the method set written into Function.HttpRoutes.
// Anything outside the set fails the bundle so a malformed router can't slip
// through to the runtime — defence in depth on top of the lib's route()
// guard.
var validHttpMethods = map[string]bool{
	"GET":     true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"OPTIONS": true,
}

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
	// Kind — Phase 7 marker for httpAction / httpRouter exports. Empty for
	// the v1 Fetch handler shape AND for the v2 query/mutation/action
	// shapes (those discriminate via RuntimeShape + the worker-side scan
	// of the export's `kind` field). Only "httpAction" and "httpRouter"
	// land here so the Go gateway can route /functions/v1/{p}/http/* to
	// the right function without re-loading the bundle. `omitempty` keeps
	// legacy records byte-stable.
	Kind string `json:"kind,omitempty"`
	// HttpRoutes — JSON array of `{path, method, exportName}` rows
	// extracted from the bundle's `__excalibase_routes` side-channel when
	// the default export is an httpRouter. Used by the gateway's
	// path-matching dispatcher; empty/nil for any other kind.
	HttpRoutes json.RawMessage `json:"httpRoutes,omitempty"`
	// IsInternal — Phase 7 marker for internalQuery / internalMutation /
	// internalAction exports. The Go gateway returns 404 from PublicInvoke
	// when this is true (indistinguishable from a missing function — no
	// information leak). Internal functions remain reachable via the
	// trusted /internal/invoke route and via ctx.runX from sibling
	// functions. The flag is captured from the runtime-reported metadata
	// callback (Phase 2 flow) and persisted on the Function record.
	IsInternal bool `json:"isInternal,omitempty"`
	// CronJobs — Phase 8 cron registry table extracted from the bundle's
	// `globalThis.__excalibase_crons` side-channel at deploy time. JSON
	// array of `{name, schedule, fnRef, args}` rows. The Go cron runner
	// reads this slot to schedule each entry at its next due time without
	// re-evaluating the module graph. nil/omitempty for bundles that
	// don't call `cronJobs()`.
	CronJobs   json.RawMessage `json:"cronJobs,omitempty"`
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

	// Phase 7: detect httpAction / httpRouter and stamp Function.Kind. Both
	// kinds are part of the v2 shape family (RuntimeShape stays "v2") but
	// the gateway needs the precise discriminator to route requests.
	if m := httpKindPattern.FindStringSubmatch(final); m != nil {
		f.Kind = m[1]
		if f.Kind == "httpRouter" {
			routes, rerr := extractHttpRoutes(final)
			if rerr != nil {
				return "", rerr
			}
			f.HttpRoutes = routes
		}
	} else {
		// Clear stale Kind/HttpRoutes if this bundle isn't an http* shape
		// (e.g. redeploy that swapped the default export for a query).
		f.Kind = ""
		f.HttpRoutes = nil
	}

	// Phase 8: detect cron registry. Bundles that don't call cronJobs()
	// land here with no match — clear stale CronJobs so a redeploy that
	// dropped its crons.ts wipes the persisted table.
	cronJobs, cerr := extractCronJobs(final)
	if cerr != nil {
		return "", cerr
	}
	f.CronJobs = cronJobs

	return final, nil
}

// extractHttpRoutes pulls the __excalibase_routes array out of the bundled
// source and returns it as a json.RawMessage. The lib's httpRouter() emits
// the table as a JS array literal of `{path, method, exportName}` rows.
// esbuild preserves the literal verbatim with UNQUOTED keys (TS object-
// literal syntax), so the bundled output looks like:
//
//	[
//	  { path: "/webhook", method: "POST", exportName: "default" },
//	  ...
//	]
//
// We normalise to JSON by quoting the bare identifier keys, then decode
// with the standard library. On a non-parseable literal or any row with
// an unsupported method, the bundle fails so the bad shape never reaches
// the runtime.
func extractHttpRoutes(bundled string) (json.RawMessage, error) {
	m := httpRoutesPattern.FindStringSubmatch(bundled)
	if m == nil {
		return nil, fmt.Errorf("httpRouter detected but __excalibase_routes table missing")
	}
	normalised := quoteBareJSKeys(m[1])
	var routes []struct {
		Path       string `json:"path"`
		Method     string `json:"method"`
		ExportName string `json:"exportName"`
	}
	if err := json.Unmarshal([]byte(normalised), &routes); err != nil {
		return nil, fmt.Errorf("httpRouter routes are not valid: %w", err)
	}
	for _, r := range routes {
		if r.Path == "" || r.Path[0] != '/' {
			return nil, fmt.Errorf("route path must start with '/' (got %q)", r.Path)
		}
		if !validHttpMethods[r.Method] {
			return nil, fmt.Errorf("route method must be GET/POST/PUT/PATCH/DELETE/OPTIONS (got %q)", r.Method)
		}
	}
	// Re-encode through json.Marshal so the persisted blob is canonical
	// (no source-side whitespace, single canonical key order).
	out, err := json.Marshal(routes)
	if err != nil {
		return nil, fmt.Errorf("re-marshal routes: %w", err)
	}
	return json.RawMessage(out), nil
}

// extractCronJobs pulls the `globalThis.__excalibase_crons` value out of
// the bundled JS by evaluating it in an isolated goja VM (same pattern
// as ExtractSchema). Returns (nil, nil) when no registry was published,
// so callers can treat a missing crons slot as "no crons configured".
//
// Goja is used (rather than a regex over the bundled source) because the
// lib's `cronJobs()` registry publishes through an intermediate variable
// and the post-esbuild output binds the array to a local name first, then
// assigns it to globalThis. Evaluating the bundle is the most reliable
// way to capture the resulting table without re-implementing the lib's
// publish() side-effect in Go.
//
// Each entry is validated for shape: `name` (non-empty string), `schedule`
// (object with `kind` in {cron, interval, daily, hourly}), `fnRef`
// (object with non-empty `moduleName` + `exportName`), `args` (object).
// A bad row fails the bundle so a malformed registry can't slip through
// to the cron runner.
func extractCronJobs(bundled string) (json.RawMessage, error) {
	if !strings.Contains(bundled, cronJobsMarker) {
		return nil, nil
	}
	vm := goja.New()
	// Prime the slot so reading back gets a clean undefined when the
	// bundle declared the marker only via the lib's import side-effect.
	if _, err := vm.RunString("globalThis = globalThis || {};\nglobalThis.__excalibase_crons = null;\n"); err != nil {
		return nil, fmt.Errorf("init cron slot: %w", err)
	}
	if _, err := vm.RunString(bundled); err != nil {
		return nil, fmt.Errorf("evaluate bundle for cron extraction: %w", err)
	}
	val := vm.Get("__excalibase_crons")
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil, nil
	}
	raw, err := json.Marshal(val.Export())
	if err != nil {
		return nil, fmt.Errorf("marshal extracted cron jobs: %w", err)
	}
	if string(raw) == "null" || string(raw) == "[]" {
		return nil, nil
	}
	var jobs []struct {
		Name     string         `json:"name"`
		Schedule map[string]any `json:"schedule"`
		FnRef    map[string]any `json:"fnRef"`
		Args     map[string]any `json:"args"`
	}
	if err := json.Unmarshal(raw, &jobs); err != nil {
		return nil, fmt.Errorf("parse extracted cron jobs: %w", err)
	}
	for i, j := range jobs {
		if j.Name == "" {
			return nil, fmt.Errorf("cronJobs[%d]: cron job name is required", i)
		}
		if j.Schedule == nil {
			return nil, fmt.Errorf("cronJobs[%d] %q: schedule object is required", i, j.Name)
		}
		kind, _ := j.Schedule["kind"].(string)
		if !validCronScheduleKinds[kind] {
			return nil, fmt.Errorf("cronJobs[%d] %q: unknown schedule kind %q (want cron|interval|daily|hourly)", i, j.Name, kind)
		}
		if j.FnRef == nil {
			return nil, fmt.Errorf("cronJobs[%d] %q: fnRef is required", i, j.Name)
		}
		mod, _ := j.FnRef["moduleName"].(string)
		exp, _ := j.FnRef["exportName"].(string)
		if mod == "" || exp == "" {
			return nil, fmt.Errorf("cronJobs[%d] %q: fnRef.moduleName and fnRef.exportName must be non-empty", i, j.Name)
		}
	}
	out, err := json.Marshal(jobs)
	if err != nil {
		return nil, fmt.Errorf("re-marshal cronJobs: %w", err)
	}
	return json.RawMessage(out), nil
}

// bareJSKeyPattern matches an identifier-shaped object key that is NOT
// already quoted (i.e. `path:`, `method:`, `exportName:`). The negative
// lookbehind on `"` is faked with a class — Go regexp has no lookbehind —
// by requiring the preceding character to be `{` or `,` or whitespace.
// JSON keys must be quoted; this pass wraps each bare key in double quotes.
var bareJSKeyPattern = regexp.MustCompile(`([{,\s])([A-Za-z_][A-Za-z0-9_]*)\s*:`)

func quoteBareJSKeys(s string) string {
	return bareJSKeyPattern.ReplaceAllString(s, `$1"$2":`)
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

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

// v2NpmImportPattern matches the case where the user pulls the kind
// wrapper from `npm:@excalibase/server` rather than inlining the tagged
// record literal. After Phase 9b.F the bundler emits ESM with `npm:`
// imports preserved verbatim, so the call site (`mutation({...})`) is
// detectable by its import declaration alone — the wrapper's runtime
// behaviour is what stamps the actual `kind` field on the export at
// worker boot. We accept any of the v2 wrappers including their
// internal* variants.
//
// Without this fallback any function that uses `import { mutation }
// from "npm:@excalibase/server"` would be misclassified as v1, sending
// it down the legacy Fetch handler path and breaking dispatch.
var v2NpmImportPattern = regexp.MustCompile(
	`import[^;]*\b(?:query|mutation|action|httpAction|httpRouter|internalQuery|internalMutation|internalAction)\b[^;]*from\s*["']npm:@excalibase/server`,
)

// httpKindPattern picks the specific kind for httpAction / httpRouter so
// Function.Kind reflects exactly what the bundle declared.
var httpKindPattern = regexp.MustCompile(`kind\s*:\s*"(httpAction|httpRouter)"`)

// httpKindNpmImportPattern is the npm:@excalibase/server companion to
// httpKindPattern — when the bundle imports `httpAction` or `httpRouter`
// from the lib and never inlines a kind literal, we still need to stamp
// Function.Kind so the gateway can route /http/* requests at deploy
// time. Mirrors v2NpmImportPattern but only for the two HTTP kinds.
var httpKindNpmImportPattern = regexp.MustCompile(
	`import[^;]*\b(httpAction|httpRouter)\b[^;]*from\s*["']npm:@excalibase/server`,
)

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
	CronJobs  json.RawMessage `json:"cronJobs,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
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
	return f.ValidateWith(nil)
}

// ValidateWith is Validate with the project's shared modules available to the
// bundle step (EXC-334). Stores must use this — validation bundles the function,
// so without the shared files a `_shared/…` import fails to resolve and the
// function could never be saved.
func (f *Function) ValidateWith(shared []File) error {
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
	if _, err := f.BundleWith(shared); err != nil {
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

// sharedPrefix is the directory project-level shared modules live under, matching
// Supabase's convention. A function imports them as "../_shared/cors.ts".
const sharedPrefix = "_shared/"

// ValidateSharedPath checks a project-level shared module path (EXC-334). Same
// safety rules as function files, plus it must sit under _shared/ so shared code
// can never shadow a function's own index.ts or escape its namespace.
func ValidateSharedPath(p string) error {
	if err := validateFilePath(p); err != nil {
		return err
	}
	if !strings.HasPrefix(p, sharedPrefix) {
		return fmt.Errorf("invalid shared file path %q: must start with %q", p, sharedPrefix)
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
// disk. Entry point is always index.ts.
//
// Phase 9b.F — output format is ESM, not IIFE. IIFE synthesises a synchronous
// `__require()` stub for every external import; Deno workers have no
// synchronous require so the stub throws at worker boot for any function
// with an `npm:` / `jsr:` / `node:` import. ESM keeps the real `import`
// declarations and Deno honours them via its own resolver. The worker
// template loads the bundle through a Blob URL + `await import(...)`,
// which is the only ESM-safe boot path inside a Deno worker.
//
// External imports (npm:, jsr:, node:, http:, https:) pass through to the
// Deno runtime unchanged — Deno resolves and fetches them at worker start.
// Relative imports (./utils.ts, ../shared/cors.ts) are resolved from the
// virtual filesystem provided in f.Files.
func (f *Function) Bundle() (string, error) {
	return f.BundleWith(nil)
}

// BundleWith is Bundle plus the project's shared modules (EXC-334). Shared files
// are seeded into the virtual filesystem first so `../_shared/x.ts` resolves;
// the function's own files are applied after and therefore win any collision.
// esbuild tree-shakes, so unreferenced shared modules cost nothing in the output.
func (f *Function) BundleWith(shared []File) (string, error) {
	virtualFiles := make(map[string]string, len(f.Files)+len(shared))
	for _, file := range shared {
		if err := ValidateSharedPath(file.Path); err != nil {
			return "", err
		}
		virtualFiles[file.Path] = file.Content
	}
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
		// FormatESModule — see function-level comment. Replaces FormatIIFE
		// + GlobalName ("__excalibase_bundle") which the IIFE format
		// required to escape `index_exports.default` to the worker scope.
		// The worker now reads `userModule.default` from the awaited
		// dynamic import instead.
		Format:   esbuild.FormatESModule,
		Target:   esbuild.ES2022,
		Platform: esbuild.PlatformNeutral,
		Write:    false,
		LogLevel: esbuild.LogLevelSilent,
		Plugins:  []esbuild.Plugin{virtualFSPlugin(virtualFiles)},
	})
	if len(result.Errors) > 0 {
		return "", fmt.Errorf("bundle error: %s", result.Errors[0].Text)
	}
	if len(result.OutputFiles) == 0 {
		return "", fmt.Errorf("bundle produced no output")
	}

	// ESM bundle output ends with `export { ... as default }`. The worker
	// template imports the bundle as a module and reads `mod.default`;
	// no trailer-emitted globalThis assignment is needed.
	final := string(result.OutputFiles[0].Contents)
	// Metadata collector slot — the worker template reads
	// globalThis.__excalibase_export_metadata after module load and posts
	// it back to main. We append it as a top-level statement; in ESM,
	// expression statements after the imports are legal. Phase 3 codegen
	// consumes the result via the _metadata endpoint.
	final += "\nglobalThis.__excalibase_export_metadata = globalThis.__excalibase_export_metadata || [];\n"

	if len(final) > MaxCodeSize {
		return "", fmt.Errorf("bundled code exceeds maximum size (%d KB)", MaxCodeSize/1024)
	}

	// Stamp the detected shape on the receiver. This is the only place that
	// writes RuntimeShape — persistence stores it, runtime codegen reads it.
	f.stampRuntimeShape(final)

	// Phase 7: detect httpAction / httpRouter and stamp Function.Kind.
	if err := f.stampHTTPKind(final); err != nil {
		return "", err
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

// stampRuntimeShape sets f.RuntimeShape from a pragmatic substring/regex scan
// of the bundled source. esbuild's ESM output preserves the source object
// literal verbatim, so the inline `kind:` marker survives for hand-rolled
// records. For functions that wrap their def in a lib helper (`mutation()`,
// `query()`, etc.) the `import` declaration is the only reliable bundle-time
// signal. Phase 3 codegen consumes this field.
func (f *Function) stampRuntimeShape(final string) {
	if v2ShapePattern.MatchString(final) || v2NpmImportPattern.MatchString(final) {
		f.RuntimeShape = RuntimeShapeV2
	} else {
		f.RuntimeShape = RuntimeShapeV1
	}
}

// stampHTTPKind detects httpAction / httpRouter shapes and stamps Function.Kind
// + HttpRoutes. Both kinds belong to the v2 shape family but the gateway needs
// the precise discriminator to route requests. Inline `kind: "httpAction"` wins
// (most specific); the npm:-import pattern is the fallback for lib-wrapped
// exports. Returns an error only when an inline httpRouter's route table fails
// to parse.
func (f *Function) stampHTTPKind(final string) error {
	if m := httpKindPattern.FindStringSubmatch(final); m != nil {
		f.Kind = m[1]
		if f.Kind == "httpRouter" {
			routes, rerr := extractHttpRoutes(final)
			if rerr != nil {
				return rerr
			}
			f.HttpRoutes = routes
		}
		return nil
	}
	if m := httpKindNpmImportPattern.FindStringSubmatch(final); m != nil {
		f.Kind = m[1]
		if f.Kind == "httpRouter" {
			// httpRouter relies on the lib's `__excalibase_routes`
			// side-channel. Extraction stays best-effort: if the side channel
			// is missing we still stamp the kind but leave routes nil, and the
			// gateway will 404 individual paths until the runtime metadata
			// callback can re-populate.
			if routes, rerr := extractHttpRoutes(final); rerr == nil {
				f.HttpRoutes = routes
			} else {
				f.HttpRoutes = nil
			}
		}
		return nil
	}
	// Clear stale Kind/HttpRoutes if this bundle isn't an http* shape
	// (e.g. redeploy that swapped the default export for a query).
	f.Kind = ""
	f.HttpRoutes = nil
	return nil
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

// esmImportPattern matches the ESM `import ... from "specifier";` declarations
// at the start of an esbuild ESM bundle. Goja parses ES5 only and chokes on
// import/export keywords, so we strip them before passing the bundle through
// for side-channel extraction. Stripping is safe because the side channels
// (`__excalibase_crons`, `__excalibase_schema`) are populated by globalThis
// assignments emitted by the user's bundle body — they never depend on a
// resolved import value at extraction time. The lib code that runs in goja
// only needs the locally-defined object literals, which esbuild has already
// inlined into the bundle body. We deliberately match each `import ... ;`
// line independently rather than removing the whole prefix block, so an
// odd source-style with a blank line between imports doesn't slip an
// `export` declaration into the goja parse stream.
var esmImportPattern = regexp.MustCompile(`(?m)^\s*import[^;]*?;\s*$`)

// esmExportDefaultPattern strips the ESM `export { X as default };` /
// `export default <expr>;` declarations. Without this the goja parse fails
// on the `export` keyword. We replace `export default <expr>;` with
// `var __excalibase_default = <expr>;` so any code that relied on the
// global slot under the old IIFE flow keeps working inside goja — though
// nothing in the extraction path consumes it today.
var esmExportDefaultExprPattern = regexp.MustCompile(`(?m)^\s*export\s+default\s+`)
var esmExportNamedPattern = regexp.MustCompile(`(?ms)^\s*export\s*\{[^}]*\}\s*;?\s*$`)

// stripESMForGoja converts an ESM bundle into an ES5-eval-safe form so
// goja can scan it for globalThis side channels (`__excalibase_crons`,
// `__excalibase_schema`). Returns a string with import/export declarations
// removed; the rest of the bundle (var decls, object literals, globalThis
// assignments) is left verbatim.
func stripESMForGoja(bundled string) string {
	s := esmImportPattern.ReplaceAllString(bundled, "")
	s = esmExportNamedPattern.ReplaceAllString(s, "")
	// `export default <expr>;` → `var __excalibase_default = <expr>;`
	// We only need the assignment so the expression is still evaluated
	// for side effects.
	s = esmExportDefaultExprPattern.ReplaceAllString(s, "var __excalibase_default = ")
	return s
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
// Phase 9b.F: the bundle is now ESM. Goja parses ES5 only, so we strip
// `import` / `export` declarations before eval. The side-channel
// assignment runs in the bundle body which goja can still parse.
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
	stripped := stripESMForGoja(bundled)
	if _, err := vm.RunString(stripped); err != nil {
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
	var jobs []cronJobRecord
	if err := json.Unmarshal(raw, &jobs); err != nil {
		return nil, fmt.Errorf("parse extracted cron jobs: %w", err)
	}
	if err := validateCronJobs(jobs); err != nil {
		return nil, err
	}
	out, err := json.Marshal(jobs)
	if err != nil {
		return nil, fmt.Errorf("re-marshal cronJobs: %w", err)
	}
	return json.RawMessage(out), nil
}

// cronJobRecord is the decoded shape of a single registered cron job, used by
// extractCronJobs / validateCronJobs.
type cronJobRecord struct {
	Name     string         `json:"name"`
	Schedule map[string]any `json:"schedule"`
	FnRef    map[string]any `json:"fnRef"`
	Args     map[string]any `json:"args"`
}

// validateCronJobs enforces the per-job invariants the runtime relies on:
// a name, a schedule with a known kind, and a fnRef with module + export.
func validateCronJobs(jobs []cronJobRecord) error {
	for i, j := range jobs {
		if j.Name == "" {
			return fmt.Errorf("cronJobs[%d]: cron job name is required", i)
		}
		if j.Schedule == nil {
			return fmt.Errorf("cronJobs[%d] %q: schedule object is required", i, j.Name)
		}
		kind, _ := j.Schedule["kind"].(string)
		if !validCronScheduleKinds[kind] {
			return fmt.Errorf("cronJobs[%d] %q: unknown schedule kind %q (want cron|interval|daily|hourly)", i, j.Name, kind)
		}
		if j.FnRef == nil {
			return fmt.Errorf("cronJobs[%d] %q: fnRef is required", i, j.Name)
		}
		mod, _ := j.FnRef["moduleName"].(string)
		exp, _ := j.FnRef["exportName"].(string)
		if mod == "" || exp == "" {
			return fmt.Errorf("cronJobs[%d] %q: fnRef.moduleName and fnRef.exportName must be non-empty", i, j.Name)
		}
	}
	return nil
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

					// Supabase-style `../_shared/x.ts`: a function's files sit at the
					// bundle root here, so a parent-relative shared import cleans to a
					// path above the virtual root. Normalise it back into the shared
					// namespace so code written (or copied) against Supabase's layout
					// resolves. Only ever rewrites to a path that actually exists.
					if _, ok := files[resolved]; !ok {
						if idx := strings.Index(resolved, sharedPrefix); idx >= 0 {
							if _, ok := files[resolved[idx:]]; ok {
								resolved = resolved[idx:]
							}
						}
					}

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

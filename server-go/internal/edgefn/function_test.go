package edgefn

import (
	"strings"
	"testing"
)

const (
	testIndexTS       = "index.ts"
	testBundleFmt     = "Bundle: %v"
	testUtilsTS       = "utils.ts"
	testHoistMissing  = "default handler hoist missing"
	testDefaultHandler = "export default () => new Response('ok')"
)


// --- Function.Validate ---

func TestFunction_Validate_RequiresProjectID(t *testing.T) {
	fn := &Function{
		ID:    "hello",
		Name:  "Hello",
		Files: []File{{Path: testIndexTS, Content: testDefaultHandler}},
	}
	if err := fn.Validate(); err == nil || !strings.Contains(err.Error(), "project") {
		t.Errorf("expected project id error, got: %v", err)
	}
}

func TestFunction_Validate_RequiresValidID(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "bad id with space",
		Name:      "Bad",
		Files:     []File{{Path: testIndexTS, Content: testDefaultHandler}},
	}
	if err := fn.Validate(); err == nil {
		t.Error("expected invalid id error")
	}
}

// Phase 3 SDK compatibility: db.functions.<module>.<export>(args) lands on
// /functions/v1/{projectId}/{module}.{export}. The chi router treats that
// as a single path segment, so the store must accept a single dot inside
// the function id. Adjacent dots and leading/trailing dots remain invalid.
func TestValidateID_AcceptsSingleDotForModuleDotExport(t *testing.T) {
	cases := []string{"db.insert", "db.find", "db.insertmany", "auth.login"}
	for _, id := range cases {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q): unexpected error: %v", id, err)
		}
	}
}

func TestValidateID_RejectsBadDotPlacement(t *testing.T) {
	bad := []string{".db", "db.", "db..insert", ".", "..", "a.b.c"}
	for _, id := range bad {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q): expected error, got nil", id)
		}
	}
}

func TestFunction_Validate_RequiresName(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "hello",
		Files:     []File{{Path: testIndexTS, Content: testDefaultHandler}},
	}
	if err := fn.Validate(); err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected name error, got: %v", err)
	}
}

func TestFunction_Validate_RequiresFiles(t *testing.T) {
	fn := &Function{ProjectID: "proj_test0001", ID: "hi", Name: "Hi"}
	if err := fn.Validate(); err == nil || !strings.Contains(err.Error(), "files") {
		t.Errorf("expected files error, got: %v", err)
	}
}

func TestFunction_Validate_RequiresIndexEntry(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "noindex",
		Name:      "NoIndex",
		Files:     []File{{Path: "helper.ts", Content: "export const x = 1"}},
	}
	if err := fn.Validate(); err == nil || !strings.Contains(err.Error(), testIndexTS) {
		t.Errorf("expected index.ts error, got: %v", err)
	}
}

func TestFunction_Validate_RejectsBadFilePath(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "bad",
		Name:      "Bad",
		Files: []File{
			{Path: testIndexTS, Content: testDefaultHandler},
			{Path: "../escape.ts", Content: "export const x = 1"},
		},
	}
	if err := fn.Validate(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("expected path traversal error, got: %v", err)
	}
}

func TestFunction_Validate_RejectsEmptyFile(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "empty",
		Name:      "Empty",
		Files:     []File{{Path: testIndexTS, Content: ""}},
	}
	if err := fn.Validate(); err == nil {
		t.Error("expected empty file error")
	}
}

func TestFunction_Validate_RejectsOversizedBundle(t *testing.T) {
	huge := strings.Repeat("a", MaxCodeSize+1)
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "huge",
		Name:      "Huge",
		Files:     []File{{Path: testIndexTS, Content: huge}},
	}
	if err := fn.Validate(); err == nil {
		t.Error("expected oversized error")
	}
}

func TestFunction_Validate_Happy(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "hello",
		Name:      "Hello",
		Files:     []File{{Path: testIndexTS, Content: testDefaultHandler}},
	}
	if err := fn.Validate(); err != nil {
		t.Errorf("valid function should not fail validation: %v", err)
	}
}

// --- Function.Bundle ---

// Phase 9b.F — bundle output is ESM (esbuild FormatESModule). Previous IIFE
// contract hoisted the default export onto `globalThis.__excalibase_default`
// via a trailer; the new contract preserves `export { X as default }` so
// Deno workers can `await import(blobUrl)` and read `mod.default`. Tests
// here verify the ESM-style contract — the legacy global assertion would
// now incorrectly fail, so we replace the substring checks accordingly.

func TestFunction_Bundle_SingleFile(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "hello",
		Name:      "Hello",
		Files:     []File{{Path: testIndexTS, Content: "export default (req: Request) => new Response('hi')"}},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	// ESM bundle preserves a real `export ... default` form (either
	// `export default <expr>` or the more common `export { X as default }`
	// that esbuild emits for re-export hoisting).
	hasExportDefault := strings.Contains(code, "export default") ||
		strings.Contains(code, "as default")
	if !hasExportDefault {
		t.Errorf("ESM bundle missing default export, got:\n%s", code)
	}
}

func TestFunction_Bundle_MultiFileInlinedFromRelativeImport(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "greet",
		Name:      "Greet",
		Files: []File{
			{Path: testUtilsTS, Content: "export const greet = (n: string) => `Hi ${n}`"},
			{Path: testIndexTS, Content: "import { greet } from './utils.ts'\nexport default (req: Request) => new Response(greet('world'))"},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	// esbuild strips TS types and inlines the helper. Identifier must survive.
	if !strings.Contains(code, "greet") || !strings.Contains(code, "`Hi ${") {
		t.Errorf("utils.ts content should be inlined, got:\n%s", code)
	}
	// ESM default export must survive — either `export default` form
	// or esbuild's `export { ... as default }` re-export.
	if !(strings.Contains(code, "export default") || strings.Contains(code, "as default")) {
		t.Error(testHoistMissing)
	}
	if strings.Contains(code, "from \"./utils.ts\"") || strings.Contains(code, "from './utils.ts'") {
		t.Errorf("relative imports must be resolved away, got:\n%s", code)
	}
}

func TestFunction_Bundle_NamespaceImportResolves(t *testing.T) {
	// This form broke the old regex bundler. esbuild must handle it.
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "ns-import",
		Name:      "Namespace Import",
		Files: []File{
			{Path: testUtilsTS, Content: "export const greet = () => 'hi'\nexport const bye = () => 'bye'"},
			{Path: testIndexTS, Content: "import * as utils from './utils.ts'\nexport default () => new Response(utils.greet() + utils.bye())"},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if !(strings.Contains(code, "export default") || strings.Contains(code, "as default")) {
		t.Error(testHoistMissing)
	}
	// Both identifiers must be referenced in the bundled output.
	if !strings.Contains(code, "greet") || !strings.Contains(code, "bye") {
		t.Errorf("namespace members missing after bundling:\n%s", code)
	}
}

func TestFunction_Bundle_RenamedImportResolves(t *testing.T) {
	// `import { a as b }` form — regex bundler choked on this.
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "renamed",
		Name:      "Renamed Import",
		Files: []File{
			{Path: testUtilsTS, Content: "export const greet = () => 'hi'"},
			{Path: testIndexTS, Content: "import { greet as hello } from './utils.ts'\nexport default () => new Response(hello())"},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if !(strings.Contains(code, "export default") || strings.Contains(code, "as default")) {
		t.Error(testHoistMissing)
	}
	if !strings.Contains(code, "greet") {
		t.Errorf("original export identifier missing after rename:\n%s", code)
	}
}

func TestFunction_Bundle_PreservesRemoteImports(t *testing.T) {
	// Deno-style remote imports must pass through as external — esbuild
	// should not try to resolve npm:/jsr:/https: URLs. Actually use the
	// import so tree-shaking can't remove it.
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "remote",
		Name:      "Remote",
		Files: []File{
			{Path: testIndexTS, Content: "import { z } from 'npm:zod@3'\nexport default () => new Response(typeof z)"},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if !strings.Contains(code, "npm:zod") {
		t.Errorf("remote import should pass through to runtime:\n%s", code)
	}
}

func TestFunction_Bundle_MissingRelativeImportErrors(t *testing.T) {
	// Use the imported symbol so tree-shaking can't remove the import
	// before resolve fires.
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "broken",
		Name:      "Broken",
		Files: []File{
			{Path: testIndexTS, Content: "import { x } from './missing.ts'\nexport default () => new Response(String(x))"},
		},
	}
	if _, err := fn.Bundle(); err == nil {
		t.Error("expected error for missing relative import")
	}
}

// --- Function.Bundle: RuntimeShape detection (v2 shape) ---

func TestBundle_DetectsV2Shape_Query(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2query",
		Name:      "V2 Query",
		Files: []File{{Path: testIndexTS, Content: `
export default {
  kind: "query",
  args: { parse: (a: unknown) => a },
  handler: async (ctx: any, args: any) => ({ ok: true }),
}`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.RuntimeShape != "v2" {
		t.Errorf("RuntimeShape: got %q, want %q", fn.RuntimeShape, "v2")
	}
}

func TestBundle_DetectsV2Shape_Mutation(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2mut",
		Name:      "V2 Mutation",
		Files: []File{{Path: testIndexTS, Content: `
export default {
  kind: "mutation",
  args: { parse: (a: unknown) => a },
  handler: async (ctx: any, args: any) => ({ id: 1 }),
}`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.RuntimeShape != "v2" {
		t.Errorf("RuntimeShape: got %q, want %q", fn.RuntimeShape, "v2")
	}
}

func TestBundle_DetectsV2Shape_Action(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2act",
		Name:      "V2 Action",
		Files: []File{{Path: testIndexTS, Content: `
export default {
  kind: "action",
  args: { parse: (a: unknown) => a },
  handler: async (ctx: any, args: any) => "done",
}`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.RuntimeShape != "v2" {
		t.Errorf("RuntimeShape: got %q, want %q", fn.RuntimeShape, "v2")
	}
}

func TestBundle_FallsBackToV1Shape(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v1fetch",
		Name:      "V1 Fetch",
		Files: []File{{Path: testIndexTS, Content: `
export default (req: Request) => new Response("ok")`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.RuntimeShape != "v1" {
		t.Errorf("RuntimeShape: got %q, want %q", fn.RuntimeShape, "v1")
	}
}

func TestBundle_V1IsDefault_WhenNoBundleCalled(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "fresh",
		Name:      "Fresh",
		Files:     []File{{Path: testIndexTS, Content: testDefaultHandler}},
	}
	// Without an explicit Bundle, persistence-layer load on a legacy record
	// (zero value) should be treated as v1 by JwtVerificationRequired's sibling
	// helper. Direct field check here.
	if fn.RuntimeShape != "" && fn.RuntimeShape != "v1" {
		t.Errorf("zero-value RuntimeShape should be empty or v1, got %q", fn.RuntimeShape)
	}
}

// --- VerifyJwt default + serialization ---

func TestFunction_VerifyJwt_DefaultsTrue(t *testing.T) {
	// JSON unmarshal — when client doesn't send verifyJwt, default to true.
	// We cannot rely on Go zero-value (false), so the field is *bool.
	fn := &Function{}
	got := fn.JwtVerificationRequired()
	if !got {
		t.Errorf("default VerifyJwt should be true (safer), got %v", got)
	}
}

func TestFunction_VerifyJwt_ExplicitFalseDisables(t *testing.T) {
	f := false
	fn := &Function{VerifyJwt: &f}
	if fn.JwtVerificationRequired() {
		t.Error("VerifyJwt=false should disable verification")
	}
}

func TestFunction_VerifyJwt_ExplicitTrueEnables(t *testing.T) {
	tr := true
	fn := &Function{VerifyJwt: &tr}
	if !fn.JwtVerificationRequired() {
		t.Error("VerifyJwt=true should enable verification")
	}
}

// --- Runtime ID (per-project namespacing) ---

func TestFunction_RuntimeID(t *testing.T) {
	fn := &Function{ProjectID: "proj_abc123", ID: "hello"}
	got := fn.RuntimeID()
	want := "proj_abc123__hello"
	if got != want {
		t.Errorf("RuntimeID: got %q, want %q", got, want)
	}
}

// --- FunctionStore ---

func TestFunctionStore_SaveAndGet(t *testing.T) {
	dir := t.TempDir()
	store := NewFunctionStore(dir)
	fn := &Function{
		ProjectID: "proj_p1",
		ID:        "hello",
		Name:      "Hello",
		Files:     []File{{Path: testIndexTS, Content: testDefaultHandler}},
	}
	if err := store.Save(fn); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get("proj_p1", "hello")
	if err != nil || got == nil {
		t.Fatalf("Get: err=%v got=%v", err, got)
	}
	if got.Name != "Hello" {
		t.Errorf("name: %q", got.Name)
	}
	if got.Version != 1 {
		t.Errorf("version: got %d, want 1", got.Version)
	}
}

func TestFunctionStore_ScopedByProject(t *testing.T) {
	dir := t.TempDir()
	store := NewFunctionStore(dir)
	store.Save(&Function{ProjectID: "proj_p1", ID: "hello", Name: "P1 Hello",
		Files: []File{{Path: testIndexTS, Content: "export default () => new Response('p1')"}}})
	store.Save(&Function{ProjectID: "proj_p2", ID: "hello", Name: "P2 Hello",
		Files: []File{{Path: testIndexTS, Content: "export default () => new Response('p2')"}}})

	// Same ID in two projects must not collide
	p1, _ := store.Get("proj_p1", "hello")
	p2, _ := store.Get("proj_p2", "hello")
	if p1 == nil || p2 == nil {
		t.Fatal("both projects should have their own hello")
	}
	if p1.Name == p2.Name {
		t.Errorf("functions should not collide across projects")
	}

	// List p1 only
	list, _ := store.List("proj_p1")
	if len(list) != 1 || list[0].Name != "P1 Hello" {
		t.Errorf("list p1 should return only p1's function, got %d entries", len(list))
	}
}

func TestFunctionStore_Versioning(t *testing.T) {
	dir := t.TempDir()
	store := NewFunctionStore(dir)
	fn := &Function{ProjectID: "proj_p1", ID: "v", Name: "V1",
		Files: []File{{Path: testIndexTS, Content: "export default () => new Response('v1')"}}}
	store.Save(fn)
	fn2 := &Function{ProjectID: "proj_p1", ID: "v", Name: "V2",
		Files: []File{{Path: testIndexTS, Content: "export default () => new Response('v2')"}}}
	store.Save(fn2)
	got, _ := store.Get("proj_p1", "v")
	if got.Version != 2 {
		t.Errorf("version after update: got %d, want 2", got.Version)
	}
	if got.Name != "V2" {
		t.Errorf("name after update: %q", got.Name)
	}
}

func TestFunctionStore_Delete(t *testing.T) {
	dir := t.TempDir()
	store := NewFunctionStore(dir)
	store.Save(&Function{ProjectID: "proj_p1", ID: "bye", Name: "Bye",
		Files: []File{{Path: testIndexTS, Content: "export default () => new Response('bye')"}}})
	if err := store.Delete("proj_p1", "bye"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := store.Get("proj_p1", "bye")
	if got != nil {
		t.Error("should be nil after delete")
	}
}

func TestFunctionStore_LoadsFromDiskOnStart(t *testing.T) {
	dir := t.TempDir()
	store1 := NewFunctionStore(dir)
	store1.Save(&Function{ProjectID: "proj_p1", ID: "persisted", Name: "Persisted",
		Files: []File{{Path: testIndexTS, Content: "export default () => new Response('p')"}}})

	// Fresh store over same dir should see existing functions
	store2 := NewFunctionStore(dir)
	got, _ := store2.Get("proj_p1", "persisted")
	if got == nil {
		t.Fatal("should be loaded from disk")
	}
	if got.Name != "Persisted" {
		t.Errorf("name: %q", got.Name)
	}
}

// Phase 9b.F — fix the long-standing blocker that prevents any function with
// an `npm:` external import from ever booting in the Deno worker.
//
// Background: esbuild's IIFE format synthesises a `__require()` stub that
// throws "Dynamic require of \"npm:<pkg>\" is not supported" at runtime
// because Deno workers have no synchronous require. Every Phase 3 / 9b.D
// / 15d / 9b.E e2e suite silently yellow-shipped on top of this. The fix
// is to emit ESM and rely on the worker template's `await import()` of a
// Blob URL — Deno honours `npm:`/`node:`/`jsr:` external specifiers in
// real ESM imports just like at top-level.
//
// This test pins the contract: Bundle() of any user source with an `npm:`
// import MUST produce ESM (real `import` statements survive to the output,
// no `__require(` shim). A bundle that contains `__require(` would boot
// fail in the worker — we refuse to ship it.
func TestBundle_NpmImportProducesESM(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "esmnpm",
		Name:      "ESM NPM",
		Files: []File{{Path: testIndexTS, Content: `
import { mutation } from "npm:@excalibase/server@0.10.0";
import { z } from "npm:zod@3";
export default mutation({
  args: z.object({}),
  handler: async () => ({ ok: true }),
});`}},
	}
	out, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}

	// The IIFE __require shim is the actual crash site at worker boot.
	// If it appears anywhere in the output, the function will not run.
	if strings.Contains(out, "__require(") {
		t.Errorf("bundle contains IIFE __require() stub — workers will fail at boot:\n%s",
			truncForLog(out))
	}

	// ESM output preserves the external import declarations verbatim so
	// Deno resolves them via npm:/jsr:/node:/http(s): at module load time.
	// We accept either the bare specifier or the `from "npm:..."` clause.
	if !strings.Contains(out, `from "npm:@excalibase/server@0.10.0"`) {
		t.Errorf("bundle missing ESM import of npm:@excalibase/server@0.10.0:\n%s",
			truncForLog(out))
	}
	if !strings.Contains(out, `from "npm:zod@3"`) {
		t.Errorf("bundle missing ESM import of npm:zod@3:\n%s", truncForLog(out))
	}

	// The v2 shape detection has to keep working through the change —
	// the bundler stamp drives RuntimeShape and the runtime codegen path
	// reads it back. Regress this and Phase 3 SDK breaks silently.
	if fn.RuntimeShape != "v2" {
		t.Errorf("RuntimeShape: got %q, want %q", fn.RuntimeShape, "v2")
	}
}

// TestBundle_NoIIFERequireStubInLegacyShape — even bundles without npm:
// imports must not emit __require(). Catches accidental regressions where
// someone restores IIFE for "just the simple cases" — there is no simple
// case, IIFE is banned end-to-end now.
func TestBundle_NoIIFERequireStubInLegacyShape(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v1plain",
		Name:      "V1 Plain",
		Files: []File{{Path: testIndexTS, Content: `
export default (_req: Request) => new Response("ok");`}},
	}
	out, err := fn.Bundle()
	if err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if strings.Contains(out, "__require(") {
		t.Errorf("plain bundle still contains __require() stub:\n%s", truncForLog(out))
	}
}

func truncForLog(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "… [truncated]"
}

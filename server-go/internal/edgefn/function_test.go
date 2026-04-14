package edgefn

import (
	"strings"
	"testing"
)

// --- Function.Validate ---

func TestFunction_Validate_RequiresProjectID(t *testing.T) {
	fn := &Function{
		ID:    "hello",
		Name:  "Hello",
		Files: []File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
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
		Files:     []File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
	}
	if err := fn.Validate(); err == nil {
		t.Error("expected invalid id error")
	}
}

func TestFunction_Validate_RequiresName(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "hello",
		Files:     []File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
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
	if err := fn.Validate(); err == nil || !strings.Contains(err.Error(), "index.ts") {
		t.Errorf("expected index.ts error, got: %v", err)
	}
}

func TestFunction_Validate_RejectsBadFilePath(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "bad",
		Name:      "Bad",
		Files: []File{
			{Path: "index.ts", Content: "export default () => new Response('ok')"},
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
		Files:     []File{{Path: "index.ts", Content: ""}},
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
		Files:     []File{{Path: "index.ts", Content: huge}},
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
		Files:     []File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
	}
	if err := fn.Validate(); err != nil {
		t.Errorf("valid function should not fail validation: %v", err)
	}
}

// --- Function.Bundle ---

func TestFunction_Bundle_SingleFile(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "hello",
		Name:      "Hello",
		Files:     []File{{Path: "index.ts", Content: "export default (req: Request) => new Response('hi')"}},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if !strings.Contains(code, "globalThis.__excalibase_default") {
		t.Errorf("bundle should transform 'export default', got:\n%s", code)
	}
	if strings.Contains(code, "export default") {
		t.Errorf("bundle should strip 'export default' keyword, got:\n%s", code)
	}
}

func TestFunction_Bundle_MultiFileOrderedInline(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "greet",
		Name:      "Greet",
		Files: []File{
			{Path: "utils.ts", Content: "export const greet = (n: string) => `Hi ${n}`"},
			{Path: "index.ts", Content: "import { greet } from './utils.ts'\nexport default (req: Request) => new Response(greet('world'))"},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	utilsIdx := strings.Index(code, "greet = (n: string)")
	defaultIdx := strings.Index(code, "__excalibase_default")
	if utilsIdx == -1 || defaultIdx == -1 {
		t.Fatalf("bundle missing pieces: utils=%d default=%d\n%s", utilsIdx, defaultIdx, code)
	}
	if utilsIdx >= defaultIdx {
		t.Errorf("utils.ts should be inlined BEFORE index.ts: utils=%d default=%d", utilsIdx, defaultIdx)
	}
	if strings.Contains(code, "from './utils.ts'") {
		t.Errorf("relative imports must be stripped after inlining")
	}
}

func TestFunction_Bundle_StripsExportKeyword(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "named",
		Name:      "Named",
		Files: []File{
			{Path: "utils.ts", Content: "export const x = 1\nexport function helper() { return 2 }\nexport async function fetchThing() { return 3 }"},
			{Path: "index.ts", Content: "export default () => new Response('ok')"},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	// After bundling, 'export' keyword is stripped from non-default exports
	if strings.Contains(code, "export const x") || strings.Contains(code, "export function helper") || strings.Contains(code, "export async function fetchThing") {
		t.Errorf("non-default 'export' keyword should be stripped:\n%s", code)
	}
	// But the identifiers must remain
	if !strings.Contains(code, "const x = 1") {
		t.Error("const x should remain")
	}
	if !strings.Contains(code, "function helper()") {
		t.Error("function helper should remain")
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
		Files:     []File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
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
		Files: []File{{Path: "index.ts", Content: "export default () => new Response('p1')"}}})
	store.Save(&Function{ProjectID: "proj_p2", ID: "hello", Name: "P2 Hello",
		Files: []File{{Path: "index.ts", Content: "export default () => new Response('p2')"}}})

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
		Files: []File{{Path: "index.ts", Content: "export default () => new Response('v1')"}}}
	store.Save(fn)
	fn2 := &Function{ProjectID: "proj_p1", ID: "v", Name: "V2",
		Files: []File{{Path: "index.ts", Content: "export default () => new Response('v2')"}}}
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
		Files: []File{{Path: "index.ts", Content: "export default () => new Response('bye')"}}})
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
		Files: []File{{Path: "index.ts", Content: "export default () => new Response('p')"}}})

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

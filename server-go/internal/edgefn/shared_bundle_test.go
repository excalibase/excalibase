package edgefn

import (
	"strings"
	"testing"
)

func fnWithIndex(src string) *Function {
	return &Function{
		ID:        "fn1",
		ProjectID: "proj_test1",
		Name:      "fn1",
		Active:    true,
		Files:     []File{{Path: "index.ts", Content: src}},
	}
}

// EXC-334: a function can import project-level shared modules.
func TestBundleWith_resolvesSharedImport(t *testing.T) {
	shared := []File{{Path: "_shared/cors.ts", Content: `export const CORS = "shared-cors-value";`}}
	fn := fnWithIndex(`
import { CORS } from "./_shared/cors.ts";
export default () => new Response(CORS);
`)

	out, err := fn.BundleWith(shared)
	if err != nil {
		t.Fatalf("BundleWith: %v", err)
	}
	if !strings.Contains(out, "shared-cors-value") {
		t.Errorf("bundle should inline the shared module, got:\n%s", out)
	}
}

// Supabase writes `../_shared/x.ts` (functions sit in their own directory there).
// Our virtual FS is rooted at the function, so the resolver normalises it —
// otherwise code copied from Supabase fails to bundle.
func TestBundleWith_resolvesParentRelativeSharedImport(t *testing.T) {
	shared := []File{{Path: "_shared/util.ts", Content: `export const V = "parent-relative-ok";`}}
	fn := fnWithIndex(`
import { V } from "../_shared/util.ts";
export default () => new Response(V);
`)

	out, err := fn.BundleWith(shared)
	if err != nil {
		t.Fatalf("BundleWith (parent-relative): %v", err)
	}
	if !strings.Contains(out, "parent-relative-ok") {
		t.Errorf("parent-relative shared import should resolve, got:\n%s", out)
	}
}

// A function's own file must win over a shared file at the same path, so shared
// code can never shadow the entry point.
func TestBundleWith_functionFilesWinOverShared(t *testing.T) {
	shared := []File{{Path: "_shared/dup.ts", Content: `export const X = "from-shared";`}}
	fn := &Function{
		ID: "fn1", ProjectID: "proj_test1", Name: "fn1", Active: true,
		Files: []File{
			{Path: "index.ts", Content: `import { X } from "./_shared/dup.ts";
export default () => new Response(X);`},
			{Path: "_shared/dup.ts", Content: `export const X = "from-function";`},
		},
	}

	out, err := fn.BundleWith(shared)
	if err != nil {
		t.Fatalf("BundleWith: %v", err)
	}
	if !strings.Contains(out, "from-function") || strings.Contains(out, "from-shared") {
		t.Errorf("function file should take precedence, got:\n%s", out)
	}
}

// Unreferenced shared modules must not bloat every bundle (esbuild tree-shakes).
func TestBundleWith_unusedSharedIsTreeShaken(t *testing.T) {
	shared := []File{{Path: "_shared/unused.ts", Content: `export const NOPE = "must-not-appear";`}}
	fn := fnWithIndex(`export default () => new Response("hi");`)

	out, err := fn.BundleWith(shared)
	if err != nil {
		t.Fatalf("BundleWith: %v", err)
	}
	if strings.Contains(out, "must-not-appear") {
		t.Errorf("unused shared module should be tree-shaken, got:\n%s", out)
	}
}

func TestValidateSharedPath(t *testing.T) {
	if err := ValidateSharedPath("_shared/cors.ts"); err != nil {
		t.Errorf("valid shared path rejected: %v", err)
	}
	// Must live under _shared/ so it can't shadow a function's entry point.
	if err := ValidateSharedPath("index.ts"); err == nil {
		t.Error("expected non-_shared path to be rejected")
	}
	// Traversal stays blocked.
	if err := ValidateSharedPath("_shared/../../etc/passwd"); err == nil {
		t.Error("expected path traversal to be rejected")
	}
}

// Bundle() must keep working unchanged for functions with no shared modules.
func TestBundle_backCompatWithoutShared(t *testing.T) {
	fn := fnWithIndex(`export default () => new Response("plain");`)
	out, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if !strings.Contains(out, "plain") {
		t.Errorf("plain bundle broken, got:\n%s", out)
	}
}

package edgefn

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- Phase 7: httpAction / httpRouter bundle detection ---

// TestBundle_DetectsHttpAction confirms that a bundle whose default export is
// a tagged record { kind: "httpAction", handler: fn } is identified by the
// Bundle() scan and stamped as Function.Kind = "httpAction". Phase 7 routes
// the request through the worker's raw-fetch path instead of the {args}
// v2 dispatch, so the gateway needs to know up-front which mode to use.
func TestBundle_DetectsHttpAction(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2http",
		Name:      "V2 HttpAction",
		Files: []File{{Path: testIndexTS, Content: `
export default {
  kind: "httpAction",
  handler: async (_ctx: any, _req: Request) => new Response("ok"),
  __metadata: {},
}`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.Kind != "httpAction" {
		t.Errorf("Function.Kind: got %q, want %q", fn.Kind, "httpAction")
	}
	// httpAction is part of the v2 shape family — same runtime path family.
	if fn.RuntimeShape != "v2" {
		t.Errorf("RuntimeShape: got %q, want %q", fn.RuntimeShape, "v2")
	}
}

// TestBundle_DetectsHttpRouter_PersistsRoutes confirms that a bundle whose
// default export is the result of httpRouter()...route(...) is detected and
// that the route table is extracted to Function.HttpRoutes (JSON array of
// { path, method, exportName }). Detection keys off the brand emitted by
// router.ts plus the literal route table the user wires up.
func TestBundle_DetectsHttpRouter_PersistsRoutes(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2router",
		Name:      "V2 HttpRouter",
		// The bundler scans for a literal __excalibase_routes side-channel
		// produced by the lib's getRoutes() registration path. To keep the
		// Go-side scan free of a real JS engine, the lib emits a literal
		// JSON array under a stable key the bundler can substring-match
		// + parse. Phase 7 sets up exactly that contract.
		Files: []File{{Path: testIndexTS, Content: `
const router = {
  kind: "httpRouter",
  __excalibase_routes: [
    { path: "/webhook", method: "POST", exportName: "default" },
    { path: "/status",  method: "GET",  exportName: "default" },
  ],
  route: () => router,
  getRoutes: () => [],
};
export default router;
`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.Kind != "httpRouter" {
		t.Errorf("Function.Kind: got %q, want %q", fn.Kind, "httpRouter")
	}
	if len(fn.HttpRoutes) == 0 {
		t.Fatalf("Function.HttpRoutes: expected non-empty, got %q", string(fn.HttpRoutes))
	}
	var routes []struct {
		Path       string `json:"path"`
		Method     string `json:"method"`
		ExportName string `json:"exportName"`
	}
	if err := json.Unmarshal(fn.HttpRoutes, &routes); err != nil {
		t.Fatalf("HttpRoutes is not a JSON array: %v (raw=%q)", err, string(fn.HttpRoutes))
	}
	if len(routes) != 2 {
		t.Errorf("HttpRoutes: got %d entries, want 2", len(routes))
	}
	// Paths must arrive in source order so the gateway dispatch order matches
	// declaration order.
	if routes[0].Path != "/webhook" || routes[0].Method != "POST" {
		t.Errorf("first route: got {path=%s,method=%s}, want {/webhook,POST}",
			routes[0].Path, routes[0].Method)
	}
}

// TestBundle_V1_NoHttpKind keeps the legacy Fetch handler default-export path
// untouched — a bundle that doesn't carry a v2-style tagged record must
// leave Function.Kind empty so persistence stays byte-stable.
func TestBundle_V1_NoHttpKind(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "legacyfetch",
		Name:      "Legacy Fetch",
		Files: []File{{Path: testIndexTS, Content: `
export default (req: Request) => new Response("ok")`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.Kind != "" {
		t.Errorf("Function.Kind: got %q, want empty for v1 fetch handler", fn.Kind)
	}
	if fn.RuntimeShape != "v1" {
		t.Errorf("RuntimeShape: got %q, want v1", fn.RuntimeShape)
	}
}

// TestBundle_HttpRouter_RejectsBadMethod confirms that a router emitting a
// non-standard method in its route table is rejected at deploy time. Defense
// in depth — the lib's route() guard catches this earlier, but a hand-crafted
// bundle could try to slip a bad method through.
func TestBundle_HttpRouter_RejectsBadMethod(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "badmethod",
		Name:      "Bad Method",
		Files: []File{{Path: testIndexTS, Content: `
const router = {
  kind: "httpRouter",
  __excalibase_routes: [
    { path: "/x", method: "BREW", exportName: "default" },
  ],
  route: () => router,
  getRoutes: () => [],
};
export default router;
`}},
	}
	_, err := fn.Bundle()
	if err == nil {
		t.Fatal("expected error for non-standard HTTP method, got nil")
	}
	if !strings.Contains(err.Error(), "method") {
		t.Errorf("error message should mention method, got: %v", err)
	}
}

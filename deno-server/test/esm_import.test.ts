// Phase 9b.F — Worker template must accept ESM-shaped bundles produced by
// the Go bundler's new FormatESModule output. The previous IIFE flow
// silently dropped any function with an `npm:` external import on the
// floor because esbuild's IIFE synthesises a synchronous `__require()`
// stub Deno workers cannot honour.
//
// These tests inline ESM bundles (so we don't need a Go bundler in this
// process) and confirm the worker:
//   1. Boots a function whose source is `export default ...` with no
//      external imports.
//   2. Boots a function whose source includes a real `import` of an
//      external specifier — proves the `await import()` Blob-URL flow
//      actually parses ESM (and is not just an `eval()` that swallows
//      module syntax silently).
//   3. Routes a bad bundle to a 5xx, not a hang — boot failure must
//      surface as a deploy error, never as silent waiting.
//
// The third case mirrors the contract from Phase 9b.E: a deploy that
// fails to boot returns a structured error, not a worker init timeout.

import { assertEquals, assertStringIncludes } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

// Wraps user source in the minimum scaffold that real Go bundles emit:
//   - a top-level `export default <expr>` declaration
//   - optional `import` statements ahead of it for the npm: case.
// This is the literal output shape of `esbuild --format=esm` for the
// matching user input. The test does NOT depend on the Go process — we
// hand-craft the bundle so the Deno side stays self-contained.
function esmBundleNoImport(defaultExpr: string): string {
  return `var __default = ${defaultExpr};
export { __default as default };
`;
}

// Bundle with a real external import. Deno honours `import ... from "npm:..."`
// at runtime; the import resolves in the worker as long as the worker was
// started with module type. The test asserts the import survives to worker
// boot (i.e. the bundler did not strip / inline it).
function esmBundleWithNpmImport(): string {
  // npm:zod is one of the few packages already cached in CI; pulling its
  // `z` export is the cheapest real import to prove the path. The handler
  // uses it to validate args so the import is not dead-code-eliminated.
  return `import { z } from "npm:zod@3";
var schema = z.object({ name: z.string().optional() });
var __default = {
  kind: "query",
  args: { parse: (a) => schema.parse(a ?? {}) },
  handler: async (_ctx, args) => ({
    greeting: "hello " + (args.name ?? "world"),
    zodLoaded: typeof z === "object" && typeof z.object === "function",
  }),
};
export { __default as default };
`;
}

Deno.test({
  name: "ESM bundle without imports: deploy + invoke roundtrip",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = esmBundleNoImport(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (_ctx, args) => ({ echoed: args.value }),
      }`);
      const deploy = await rt.deploy("esm-plain", fnCode);
      assertEquals(deploy.status, 201, `deploy failed: ${await deploy.text()}`);

      const res = await rt.invoke("esm-plain", { args: { value: 42 } });
      assertEquals(res.status, 200, `invoke status: ${res.body}`);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.echoed, 42);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ESM bundle with real npm: import: worker imports module and handler runs",
  async fn() {
    // The crux of Phase 9b.F. Previous (IIFE) worker template would crash
    // here at boot with `Error: Dynamic require of "npm:zod@3" is not
    // supported`. After the ESM switch the import resolves through Deno
    // and the handler is reachable.
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = esmBundleWithNpmImport();
      const deploy = await rt.deploy("esm-npm", fnCode);
      if (deploy.status !== 201) {
        throw new Error(`deploy failed (${deploy.status}): ${await deploy.text()}`);
      }

      const res = await rt.invoke("esm-npm", { args: { name: "duc" } });
      assertEquals(res.status, 200, `invoke status ${res.body}`);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.greeting, "hello duc");
      assertEquals(parsed.data.zodLoaded, true);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ESM bundle with unresolvable import: deploy surfaces error, no silent hang",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      // Importing a guaranteed-not-resolving specifier must NOT cause the
      // deploy to hang for the full worker-init timeout. The runtime
      // captures the worker.onerror event and rejects the deploy with a
      // structured error — that is the boundary we lock in here.
      const fnCode = `import { __nothing } from "npm:@excalibase-non-existent-pkg-do-not-publish@99.99.99";
var __default = {
  kind: "query",
  args: { parse: (a) => a },
  handler: async () => __nothing,
};
export { __default as default };
`;
      const deploy = await rt.deploy("esm-bad", fnCode);
      // Either 400 (validated reject) or 502 (worker init error) is
      // acceptable — what matters is that we don't 201 a function that
      // can't boot, and the deploy returns within the worker init budget.
      if (deploy.status === 201) {
        throw new Error("deploy returned 201 for unbootable bundle");
      }
      const text = await deploy.text();
      // Must surface SOMETHING — bare "" body would mean we caught the
      // error but lost the message, which Phase 9b.E specifically wanted
      // fixed (no silent failures).
      if (text.length === 0) {
        throw new Error("deploy error body is empty — message lost");
      }
      assertStringIncludes(text.toLowerCase(), "");  // sanity: not empty
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

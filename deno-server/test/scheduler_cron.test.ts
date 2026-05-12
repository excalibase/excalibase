// Phase 8 — cron registry persistence tests.
//
// A deployed function that calls `cronJobs()` and registers jobs should
// have its job table extracted from the bundle and persisted on the
// Function record (Go-side, via the bundler scan). The runtime relies on
// the Go cron runner to translate the registry rows into pending entries
// on `excalibase_scheduled_functions` at each due time.
//
// These tests stay on the worker side — they confirm that:
//   * the worker can boot with a bundle that imports cronJobs() without
//     crashing (the lib's side-effect publish hits globalThis cleanly);
//   * the Phase 2 metadata callback continues to report the default
//     export's kind even when the bundle has a `globalThis.__excalibase_crons`
//     declaration (no regression on the metadata path);

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "cron registry side-channel coexists with a v2 default export",
  async fn() {
    // Simulate what @excalibase/server's cronJobs() registers — the global
    // slot is set BEFORE the default export gets stamped. The bundler later
    // reads the slot during esbuild's scan, but the worker just needs to
    // tolerate the slot existing without breaking dispatch.
    const code = `
      globalThis.__excalibase_crons = [
        { name: "noop",
          schedule: { kind: "daily", hourUTC: 9, minuteUTC: 0 },
          fnRef: { moduleName: "jobs", exportName: "sendDigest" },
          args: {} },
      ];
    ` + bundleDefault(`{
      kind: "mutation",
      args: { parse: (a) => a },
      handler: async (_ctx, _args) => ({ ok: true }),
      __metadata: { argsJsonSchema: { type: "object", properties: {} } },
    }`);

    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("proj_cron__noop", code);
      const res = await rt.invoke("proj_cron__noop", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { data: { ok: boolean } };
      assertEquals(parsed.data.ok, true);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "cron registry side-channel produces a metadata callback unaffected by crons",
  async fn() {
    // Stand up a tiny HTTP server to capture the runtime's metadata callback
    // and assert the export.kind reflects the user's default export, not
    // anything from the cron registry.
    type CapturedBody = { projectId: string; exports: Array<{ kind: string }> };
    // eslint-disable-next-line prefer-const
    let captured = null as CapturedBody | null;
    const ac = new AbortController();
    const port = await new Promise<number>((resolve) => {
      // deno-lint-ignore no-explicit-any
      Deno.serve({ port: 0, signal: ac.signal, onListen: ({ port }: any) => resolve(port) },
        async (req: Request) => {
          if (req.method === "POST" && new URL(req.url).pathname.endsWith("/metadata")) {
            captured = await req.json() as CapturedBody;
            return new Response("{}", { headers: { "content-type": "application/json" } });
          }
          return new Response("not found", { status: 404 });
        });
    });

    const rt = await startRuntime({
      v2Enabled: true,
      provisioningUrl: `http://127.0.0.1:${port}`,
    });
    try {
      const code = `
        globalThis.__excalibase_crons = [
          { name: "noop", schedule: { kind: "hourly", minuteUTC: 0 },
            fnRef: { moduleName: "jobs", exportName: "tick" }, args: {} },
        ];
      ` + bundleDefault(`{
        kind: "action",
        args: { parse: (a) => a },
        handler: async (_ctx, _args) => null,
        __metadata: { argsJsonSchema: { type: "object", properties: {} } },
      }`);
      await rt.deploy("proj_cron__meta", code);
      // Give the fire-and-forget callback a beat to land.
      await delay(300);
      assertExists(captured);
      const cap = captured as CapturedBody;
      assertEquals(cap.projectId, "proj_cron");
      assertEquals(cap.exports[0].kind, "action");
    } finally {
      await rt.stop();
      ac.abort();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

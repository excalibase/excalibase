// Phase 7 — httpAction dispatch.
//
// A bundle whose default export is { kind: "httpAction", handler } skips the
// {args} v2 parsing path and gets the raw Request straight to its handler.
// Response status, headers, and body all flow back to the caller.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "httpAction: handler receives raw Request, response preserved",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "httpAction",
        __metadata: {},
        handler: async (_ctx, req) => {
          const body = await req.text();
          return new Response(
            JSON.stringify({ echo: body, method: req.method }),
            {
              status: 201,
              headers: {
                "x-custom": "hello",
                "content-type": "application/json",
              },
            }
          );
        },
      }`);
      await rt.deploy("proj_e__webhook", fnCode);

      // Invoke with a raw string body — the runtime must NOT JSON-parse it.
      const res = await rt.invoke("proj_e__webhook", "raw-payload");
      assertEquals(res.status, 201);
      assertEquals(res.headers["x-custom"], "hello");
      const parsed = JSON.parse(res.body) as { echo: string; method: string };
      assertEquals(parsed.echo, "raw-payload");
      assertEquals(parsed.method, "POST");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "httpAction: ctx.db is null (action parity)",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "httpAction",
        __metadata: {},
        handler: async (ctx, _req) => {
          return new Response(
            JSON.stringify({ dbIsNull: ctx.db === null }),
            { status: 200, headers: { "content-type": "application/json" } }
          );
        },
      }`);
      await rt.deploy("proj_f__nodb", fnCode);
      const res = await rt.invoke("proj_f__nodb", "");
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { dbIsNull: boolean };
      assertEquals(parsed.dbIsNull, true);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "httpAction: non-Response return surfaces as 500",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "httpAction",
        __metadata: {},
        handler: async (_ctx, _req) => {
          // Wrong shape — must return a Response. Runtime must not crash.
          return { not: "a response" };
        },
      }`);
      await rt.deploy("proj_g__bad", fnCode);
      const res = await rt.invoke("proj_g__bad", "");
      // Either 500 from the worker or the response from the body — accept
      // any non-2xx so the runtime never silently succeeds on this misuse.
      if (res.status >= 200 && res.status < 300) {
        throw new Error("non-Response should not produce a 2xx, got " + res.status);
      }
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

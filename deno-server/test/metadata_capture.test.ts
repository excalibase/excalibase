// Metadata capture tests — after the worker loads a v2 module, it scans
// the module's exports for tagged FunctionDef records and posts a metadata
// message to main. Main forwards via HTTP back to the Go provisioning side.
//
// These tests intercept the metadata message on the runtime subprocess
// (via a mock provisioning HTTP server that the runtime is configured to
// call) and assert the shape of what gets reported.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  // Mirror the Go-side Bundle() preamble — the worker reads a sentinel
  // (globalThis.__excalibase_export_metadata is populated by the runtime
  // template). The Go side writes this preamble in front of the bundled
  // code, but tests bypass the Go bundler and inject the marker directly.
  return `
    globalThis.__excalibase_export_metadata = [];
    globalThis.__excalibase_default = ${expr};
  `;
}

// startMockProvisioning spins a tiny HTTP server that records every POST
// to /internal/runtime/functions/{id}/metadata. Used to assert the runtime
// forwards captured metadata back to the provisioning service.
interface CapturedCallback {
  fnId: string;
  body: unknown;
  authHeader: string | null;
}

async function startMockProvisioning(): Promise<{
  url: string;
  captured: CapturedCallback[];
  stop: () => Promise<void>;
}> {
  const captured: CapturedCallback[] = [];
  const ac = new AbortController();
  let server!: Deno.HttpServer;
  const ready = new Promise<number>((resolve) => {
    server = Deno.serve({
      port: 0,
      signal: ac.signal,
      onListen({ port }) {
        resolve(port);
      },
    }, async (req) => {
      const url = new URL(req.url);
      const m = url.pathname.match(/^\/internal\/runtime\/functions\/([^/]+)\/metadata$/);
      if (m && req.method === "POST") {
        const body = await req.json().catch(() => null);
        captured.push({
          fnId: m[1]!,
          body,
          authHeader: req.headers.get("X-Excalibase-Runtime-Token"),
        });
        return new Response(null, { status: 204 });
      }
      return new Response("not found", { status: 404 });
    });
  });
  const port = await ready;
  return {
    url: `http://127.0.0.1:${port}`,
    captured,
    stop: async () => {
      try { ac.abort(); } catch (_) { /* ignore */ }
      try { await server.shutdown(); } catch (_) { /* ignore */ }
    },
  };
}

Deno.test({
  name: "metadata capture: worker reports tagged v2 exports back to provisioning",
  async fn() {
    const mock = await startMockProvisioning();
    try {
      const rt = await startRuntime({
        v2Enabled: true,
        provisioningUrl: mock.url,
      });
      try {
        const fnCode = bundleDefault(`{
          kind: "query",
          args: { parse: (a) => a, _isJsonSchema: { type: "object", properties: { status: { type: "string" } } } },
          handler: async (_ctx, _args) => ({ ok: true }),
          __metadata: { argsJsonSchema: { type: "object", properties: { status: { type: "string" } } } },
        }`);
        // Use the same runtime-id format the Go side ships: `projectId__fnId`.
        // Forwarder splits on `__` to populate the callback URL + payload.
        const deploy = await rt.deploy("proj_test__metafn", fnCode);
        assertEquals(deploy.status, 201);

        // Give the runtime a beat to fire-and-forget the HTTP callback.
        for (let i = 0; i < 50 && mock.captured.length === 0; i++) {
          await delay(50);
        }
        if (mock.captured.length === 0) {
          throw new Error("runtime did not POST any metadata back to provisioning");
        }
        const cap = mock.captured[0]!;
        assertEquals(cap.fnId, "metafn");
        // deno-lint-ignore no-explicit-any
        const captured_body = cap.body as any;
        if (captured_body?.projectId !== "proj_test") {
          throw new Error(`projectId in body: ${captured_body?.projectId}`);
        }
        // The callback body must include an `exports` array shaped for the
        // Go side: [{ name, kind, argsJsonSchema }]. We're permissive about
        // the exact set of keys but the shape must match.
        // deno-lint-ignore no-explicit-any
        const body = cap.body as any;
        if (!Array.isArray(body?.exports)) {
          throw new Error(`expected body.exports to be an array, got ${JSON.stringify(body)}`);
        }
        if (body.exports.length === 0) {
          throw new Error("exports array was empty — collector did not pick up the v2 export");
        }
        const exp0 = body.exports[0];
        if (exp0.kind !== "query") {
          throw new Error(`exports[0].kind: ${exp0.kind}`);
        }
        if (typeof exp0.argsJsonSchema !== "object" || exp0.argsJsonSchema == null) {
          throw new Error(`exports[0].argsJsonSchema missing or not an object: ${JSON.stringify(exp0)}`);
        }
        // Runtime token header must be present so the Go side can authenticate.
        if (cap.authHeader == null || cap.authHeader.length === 0) {
          throw new Error("X-Excalibase-Runtime-Token header missing on callback");
        }
      } finally {
        await rt.stop();
      }
    } finally {
      await mock.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "metadata capture: v1 (legacy Fetch handler) deploys do NOT fire a callback",
  async fn() {
    const mock = await startMockProvisioning();
    try {
      // v1 path — flag off. Worker should not scan/report.
      const rt = await startRuntime({
        v2Enabled: false,
        provisioningUrl: mock.url,
      });
      try {
        const fnCode = `globalThis.__excalibase_default = () => new Response("ok");`;
        const deploy = await rt.deploy("v1plain", fnCode);
        assertEquals(deploy.status, 201);
        // Give it a beat. Should remain empty.
        await delay(200);
        if (mock.captured.length !== 0) {
          throw new Error(`expected zero callbacks for v1 deploy, got ${mock.captured.length}`);
        }
      } finally {
        await rt.stop();
      }
    } finally {
      await mock.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

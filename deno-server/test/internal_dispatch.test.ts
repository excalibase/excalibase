// Phase 7 — internal-only dispatch.
//
// The Deno runtime itself doesn't differentiate "internal" from "public" on
// the invoke path — that decision is made by the Go gateway. What the runtime
// MUST do is surface the `isInternal: true` tag in the metadata it forwards
// to provisioning, so the gateway can persist the flag and gate
// `PublicInvoke` on the way in.
//
// These tests assert two things:
//   1. A v2 export tagged `isInternal: true` reaches the metadata callback
//      with the flag intact (Phase 2 metadata-capture flow extended).
//   2. The /invoke/ admin path (used internally by provisioning + by the
//      runtime's own runX RPC) still executes the handler regardless of the
//      isInternal flag — internal functions are reachable from the internal
//      path; only the gateway's PublicInvoke route should 404 on them.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

interface MetadataPayload {
  projectId: string;
  exports: Array<{ name: string; kind: string; isInternal?: boolean; argsJsonSchema: unknown }>;
}

async function startMetadataSink(): Promise<{
  url: string;
  received: MetadataPayload[];
  stop: () => Promise<void>;
}> {
  const received: MetadataPayload[] = [];
  const ac = new AbortController();
  const server = Deno.serve({ port: 0, signal: ac.signal }, async (req) => {
    if (req.method === "POST") {
      const body = await req.json() as MetadataPayload;
      received.push(body);
      return new Response("ok", { status: 200 });
    }
    return new Response("nope", { status: 405 });
  });
  const addr = server.addr as { port: number };
  return {
    url: `http://127.0.0.1:${addr.port}`,
    received,
    stop: async () => {
      ac.abort();
      try { await server.finished; } catch (_) { /* ignore */ }
    },
  };
}

Deno.test({
  name: "internal v2 export: metadata callback carries isInternal=true",
  async fn() {
    const sink = await startMetadataSink();
    const rt = await startRuntime({ v2Enabled: true, provisioningUrl: sink.url });
    try {
      // Internal-only mutation — same shape as a regular mutation but tagged.
      const fnCode = bundleDefault(`{
        kind: "mutation",
        isInternal: true,
        args: { parse: (a) => a },
        handler: async (_ctx, args) => ({ ok: true, name: args.name }),
        __metadata: { argsJsonSchema: { type: "object" } },
      }`);
      // Use the projectId__fnId scheme so the runtime forwards metadata
      // with a parseable runtime id.
      const deploy = await rt.deploy("proj_x__cleanup", fnCode);
      assertEquals(deploy.status, 201);

      // Give the runtime up to 2s to forward metadata.
      const deadline = Date.now() + 2000;
      while (Date.now() < deadline && sink.received.length === 0) {
        await new Promise((r) => setTimeout(r, 50));
      }
      if (sink.received.length === 0) {
        throw new Error("metadata callback never fired");
      }
      const m = sink.received[0];
      assertEquals(m.projectId, "proj_x");
      assertEquals(m.exports[0].kind, "mutation");
      assertEquals(m.exports[0].isInternal, true);
    } finally {
      await rt.stop();
      await sink.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "internal v2 export: /invoke/ admin path still runs the handler",
  async fn() {
    // The runtime itself doesn't enforce internal-only — the gateway does.
    // Internal callers (cross-function ctx.runX, admin tools) must reach
    // the handler successfully via the /invoke/ admin path even when
    // isInternal=true. Otherwise ctx.runMutation(internal.foo, ...) breaks.
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "mutation",
        isInternal: true,
        args: { parse: (a) => a },
        handler: async (_ctx, args) => ({ ok: true, name: args.name }),
        __metadata: { argsJsonSchema: { type: "object" } },
      }`);
      const deploy = await rt.deploy("proj_y__internal", fnCode);
      assertEquals(deploy.status, 201);
      const res = await rt.invoke("proj_y__internal", { args: { name: "ada" } });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { data: { ok: boolean; name: string } };
      assertEquals(parsed.data.ok, true);
      assertEquals(parsed.data.name, "ada");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// Phase 10 — integration tests for ctx.storage end-to-end inside the
// Deno runtime. Stands up a mock provisioning HTTP server in-process,
// points the runtime at it via EXCALIBASE_PROVISIONING_URL, deploys a
// mutation that calls every ctx.storage method, and asserts the captured
// requests at the mock side.
//
// We don't run R2 here — provisioning's `/internal/storage/*` handler is
// the contract boundary the runtime depends on. The Go-side tests in
// server-go/internal/handler/storage_internal_test.go exercise the real
// signed-URL minting and metadata persistence against the existing
// storage handler.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

const RUNTIME_SECRET = "test-runtime-secret";

interface CapturedRequest {
  method: string;
  path: string;
  headers: Record<string, string>;
  body: string;
}

interface MockProvisioning {
  url: string;
  requests: CapturedRequest[];
  stop: () => Promise<void>;
}

// startMockProvisioning brings up a tiny HTTP server that pretends to be
// the Go provisioning service for the runtime. Captures every request and
// replies with canned data keyed off (method, path-pattern).
async function startMockProvisioning(
  routes: Record<string, (req: Request) => Response | Promise<Response>>,
): Promise<MockProvisioning> {
  const captured: CapturedRequest[] = [];
  const ac = new AbortController();
  const server = Deno.serve({ port: 0, signal: ac.signal }, async (req) => {
    const url = new URL(req.url);
    const bodyText = req.body ? await req.text() : "";
    const headers: Record<string, string> = {};
    req.headers.forEach((v, k) => { headers[k] = v; });
    captured.push({
      method: req.method,
      path: url.pathname,
      headers,
      body: bodyText,
    });

    // Match against the longest-prefix route key. Patterns are exact
    // pathnames or `{prefix}:*` to match anything starting with prefix.
    for (const [key, fn] of Object.entries(routes)) {
      const [m, pat] = key.split(" ");
      if (m !== req.method) continue;
      if (pat.endsWith(":*")) {
        const prefix = pat.slice(0, -2);
        if (url.pathname.startsWith(prefix)) {
          // Rebuild request so the handler can re-read the body if needed.
          return fn(new Request(req.url, { method: req.method, headers: req.headers, body: bodyText || undefined }));
        }
      } else if (pat === url.pathname) {
        return fn(new Request(req.url, { method: req.method, headers: req.headers, body: bodyText || undefined }));
      }
    }
    return new Response("not found in mock", { status: 404 });
  });
  // Server returns a structure with the bound address when listening.
  // For Deno.serve we read it via server.addr after the first request OR
  // by waiting for the `listening` event. Deno's API exposes `.addr`
  // synchronously on the returned handle.
  const addr = server.addr as Deno.NetAddr;
  return {
    url: `http://127.0.0.1:${addr.port}`,
    requests: captured,
    stop: async () => {
      ac.abort();
      try { await server.finished; } catch (_) { /* aborted */ }
    },
  };
}

Deno.test({
  name: "ctx.storage.generateUploadUrl posts to provisioning with runtime-token and returns the signed URL",
  async fn() {
    const mock = await startMockProvisioning({
      "POST /internal/storage/proj_storeA/upload-url": () =>
        new Response(
          JSON.stringify({
            url: "https://r2.test/PUT/abc?sig=zzz",
            storageId: "kg2_minted",
            method: "PUT",
          }),
          { headers: { "content-type": "application/json" } },
        ),
    });

    try {
      const rt = await startRuntime({
        v2Enabled: true,
        provisioningUrl: mock.url,
      });
      try {
        const handlerCode = `globalThis.__excalibase_default = {
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const url = await ctx.storage.generateUploadUrl();
            return { url };
          },
        };`;
        await rt.deploy("proj_storeA__caller", handlerCode);
        const res = await rt.invoke("proj_storeA__caller", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as { data: { url: string } };
        assertEquals(parsed.data.url, "https://r2.test/PUT/abc?sig=zzz");

        // Verify the mock saw exactly one upload-url call carrying the
        // runtime-token header.
        const calls = mock.requests.filter(
          (c) => c.path === "/internal/storage/proj_storeA/upload-url",
        );
        assertEquals(calls.length, 1);
        assertEquals(calls[0].method, "POST");
        // Header keys come through lower-cased on Deno's fetch side.
        const token = calls[0].headers["x-excalibase-runtime-token"];
        assertEquals(token, RUNTIME_SECRET);
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
  name: "ctx.storage.getUrl returns null when provisioning replies 404",
  async fn() {
    const mock = await startMockProvisioning({
      "POST /internal/storage/proj_storeB/download-url": () =>
        new Response("not found", { status: 404 }),
    });

    try {
      const rt = await startRuntime({
        v2Enabled: true,
        provisioningUrl: mock.url,
      });
      try {
        const handlerCode = `globalThis.__excalibase_default = {
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const url = await ctx.storage.getUrl("kg2_missing");
            return { url };
          },
        };`;
        await rt.deploy("proj_storeB__caller", handlerCode);
        const res = await rt.invoke("proj_storeB__caller", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as { data: { url: string | null } };
        assertEquals(parsed.data.url, null);
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
  name: "ctx.storage.getMetadata returns the metadata row when provisioning has one",
  async fn() {
    const mock = await startMockProvisioning({
      "GET /internal/storage/proj_storeC/metadata/kg2_abc": () =>
        new Response(
          JSON.stringify({
            storageId: "kg2_abc",
            sha256: "ff",
            size: 7,
            contentType: "text/plain",
          }),
          { headers: { "content-type": "application/json" } },
        ),
    });

    try {
      const rt = await startRuntime({
        v2Enabled: true,
        provisioningUrl: mock.url,
      });
      try {
        const handlerCode = `globalThis.__excalibase_default = {
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            return ctx.storage.getMetadata("kg2_abc");
          },
        };`;
        await rt.deploy("proj_storeC__caller", handlerCode);
        const res = await rt.invoke("proj_storeC__caller", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as {
          data: { storageId: string; size: number; contentType: string };
        };
        assertEquals(parsed.data.storageId, "kg2_abc");
        assertEquals(parsed.data.size, 7);
        assertEquals(parsed.data.contentType, "text/plain");
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
  name: "ctx.storage.delete posts DELETE and resolves to undefined",
  async fn() {
    const mock = await startMockProvisioning({
      "DELETE /internal/storage/proj_storeD/kg2_x:*": () =>
        new Response(null, { status: 204 }),
    });

    try {
      const rt = await startRuntime({
        v2Enabled: true,
        provisioningUrl: mock.url,
      });
      try {
        const handlerCode = `globalThis.__excalibase_default = {
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            await ctx.storage.delete("kg2_x");
            return { ok: true };
          },
        };`;
        await rt.deploy("proj_storeD__caller", handlerCode);
        const res = await rt.invoke("proj_storeD__caller", { args: {} });
        assertEquals(res.status, 200);

        const deleteCalls = mock.requests.filter((c) => c.method === "DELETE");
        assertEquals(deleteCalls.length, 1);
        assertEquals(
          deleteCalls[0].path,
          "/internal/storage/proj_storeD/kg2_x",
        );
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
  name: "ctx.storage on a QueryCtx exposes only the reader surface (delete absent)",
  async fn() {
    // Build a query that asks for ctx.storage.delete — should throw at
    // runtime because the worker wires StorageReader on query ctx.
    const mock = await startMockProvisioning({});

    try {
      const rt = await startRuntime({
        v2Enabled: true,
        provisioningUrl: mock.url,
      });
      try {
        const handlerCode = `globalThis.__excalibase_default = {
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            // Read-only — delete should be undefined on a QueryCtx.
            const hasDelete = typeof ctx.storage.delete === "function";
            const hasGetUrl = typeof ctx.storage.getUrl === "function";
            return { hasDelete, hasGetUrl };
          },
        };`;
        await rt.deploy("proj_storeE__caller", handlerCode);
        const res = await rt.invoke("proj_storeE__caller", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as {
          data: { hasDelete: boolean; hasGetUrl: boolean };
        };
        assertEquals(parsed.data.hasDelete, false);
        assertEquals(parsed.data.hasGetUrl, true);
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

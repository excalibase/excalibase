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
            uploadId: "upl_a",
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
            const minted = await ctx.storage.generateUploadUrl({ contentType: "image/png", size: 10 });
            return { url: minted.url };
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

// The upload protocol stages bytes before they become an object, so both the
// mint and the confirmation carry fields the runtime has to send. These pin
// the exact JSON the runtime puts on the wire against provisioning's internal
// routes: change either side and this test says so.
Deno.test({
  name: "ctx.storage.store declares its size and type, PUTs exactly them, and confirms by upload id",
  async fn() {
    const putHeaders: Record<string, string> = {};
    let putURL = "";
    const mock = await startMockProvisioning({
      "POST /internal/storage/proj_storeD/upload-url": () =>
        new Response(
          JSON.stringify({
            url: putURL,
            storageId: "kg2_stored",
            uploadId: "upl_staged",
            method: "PUT",
            headers: { "Content-Type": "text/plain", "Content-Length": "5" },
          }),
          { headers: { "content-type": "application/json" } },
        ),
      "PUT /staged": (req) => {
        req.headers.forEach((v, k) => { putHeaders[k] = v; });
        return new Response(null, { status: 200 });
      },
      "POST /internal/storage/proj_storeD/confirm-upload": () =>
        new Response(null, { status: 204 }),
    });

    putURL = `${mock.url}/staged`;
    try {
      const rt = await startRuntime({ v2Enabled: true, provisioningUrl: mock.url });
      try {
        const handlerCode = `globalThis.__excalibase_default = {
          kind: "action",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const blob = new Blob(["hello"], { type: "text/plain" });
            return { id: await ctx.storage.store(blob) };
          },
        };`;
        await rt.deploy("proj_storeD__caller", handlerCode);
        const res = await rt.invoke("proj_storeD__caller", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as { data: { id: string } };
        assertEquals(parsed.data.id, "kg2_stored");

        const mint = mock.requests.find((c) => c.path.endsWith("/upload-url"));
        if (!mint) throw new Error("no upload-url call was made");
        const mintBody = JSON.parse(mint.body) as { contentType: string; size: number };
        // Both are bound into the signature, so both must be declared.
        assertEquals(mintBody.contentType, "text/plain");
        assertEquals(mintBody.size, 5);

        const confirm = mock.requests.find((c) => c.path.endsWith("/confirm-upload"));
        if (!confirm) throw new Error("no confirm-upload call was made");
        const confirmBody = JSON.parse(confirm.body) as Record<string, unknown>;
        // The confirmation names the staged upload. Size and content type are
        // read back from the object store, so sending them would be ignored.
        assertEquals(confirmBody.storageId, "kg2_stored");
        assertEquals(confirmBody.uploadId, "upl_staged");
        assertEquals(confirmBody.size, undefined);
        assertEquals(confirmBody.contentType, undefined);

        // The PUT carried exactly what was declared; anything else is a 403
        // from the object store, because the signature covers both headers.
        assertEquals(putHeaders["content-type"], "text/plain");
        assertEquals(putHeaders["content-length"], "5");
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

// The direct-upload flow hands the browser a URL and the two ids it will need
// to turn the upload into an object.
Deno.test({
  name: "ctx.storage.generateUploadUrl declares the upload and returns url, storageId and uploadId",
  async fn() {
    const mock = await startMockProvisioning({
      "POST /internal/storage/proj_storeE/upload-url": () =>
        new Response(
          JSON.stringify({
            url: "https://r2.test/.staging/upl_1?sig=zzz",
            storageId: "kg2_minted",
            uploadId: "upl_1",
            method: "PUT",
          }),
          { headers: { "content-type": "application/json" } },
        ),
      "POST /internal/storage/proj_storeE/confirm-upload": () =>
        new Response(null, { status: 204 }),
    });

    try {
      const rt = await startRuntime({ v2Enabled: true, provisioningUrl: mock.url });
      try {
        const handlerCode = `globalThis.__excalibase_default = {
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const minted = await ctx.storage.generateUploadUrl({ contentType: "image/png", size: 99 });
            const confirmed = await ctx.storage.completeUpload({
              storageId: minted.storageId, uploadId: minted.uploadId,
            });
            return { ...minted, confirmed };
          },
        };`;
        await rt.deploy("proj_storeE__caller", handlerCode);
        const res = await rt.invoke("proj_storeE__caller", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body) as {
          data: { url: string; storageId: string; uploadId: string; confirmed: string };
        };
        assertEquals(parsed.data.url, "https://r2.test/.staging/upl_1?sig=zzz");
        assertEquals(parsed.data.storageId, "kg2_minted");
        assertEquals(parsed.data.uploadId, "upl_1");
        assertEquals(parsed.data.confirmed, "kg2_minted");

        const mint = mock.requests.find((c) => c.path.endsWith("/upload-url"));
        if (!mint) throw new Error("no upload-url call was made");
        assertEquals(JSON.parse(mint.body), { contentType: "image/png", size: 99 });

        const confirm = mock.requests.find((c) => c.path.endsWith("/confirm-upload"));
        if (!confirm) throw new Error("no confirm-upload call was made");
        assertEquals(JSON.parse(confirm.body), { storageId: "kg2_minted", uploadId: "upl_1" });
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

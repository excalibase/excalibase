// EXC-348 — per-project outbound allowlist.
//
// A worker's `net` permission is the union of the runtime-wide ALLOWED_HOSTS
// env (the per-project pod on k8s, or an operator baseline on the shared
// docker runtime) and the `allowedHosts` list carried on the deploy payload
// (how the shared runtime scopes egress per project). Anything outside that
// union must be refused by Deno's permission check — never by DNS or a
// connection failure. Two local listeners stand in for "allowed" and
// "not allowed" so the test never touches the real network.

import { assertEquals, assertStringIncludes } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { type RuntimeHandle, startRuntime } from "./harness.ts";

// The handler fetches whatever URL the invoke body names and reports the
// outcome, so one deployed function can probe both listeners.
const FETCHER = `globalThis.__excalibase_default = async (req) => {
  const url = await req.text();
  try {
    const r = await fetch(url);
    return new Response("ok:" + r.status);
  } catch (e) {
    return new Response("denied:" + e.name, { status: 403 });
  }
};`;

function listener(): { url: string; stop: () => Promise<void> } {
  const ac = new AbortController();
  const server = Deno.serve({ port: 0, signal: ac.signal, onListen() {} }, () => new Response("hello"));
  const port = (server.addr as Deno.NetAddr).port;
  return {
    url: `http://127.0.0.1:${port}/`,
    stop: async () => { ac.abort(); await server.finished; },
  };
}

function hostOf(url: string): string {
  return new URL(url).host;
}

// deployOk deploys and drains the response so the leak sanitizer stays quiet.
async function deployOk(rt: RuntimeHandle, id: string, allowedHosts?: string[]) {
  const res = await rt.raw("/deploy", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Runtime-Secret": rt.secret },
    body: JSON.stringify({ id, code: FETCHER, allowedHosts }),
  });
  await res.body?.cancel();
  assertEquals(res.status, 201);
}

Deno.test({
  name: "egress: ALLOWED_HOSTS env grants exactly those hosts to the worker",
  async fn() {
    const allowed = listener();
    const other = listener();
    const rt = await startRuntime({ allowedHosts: hostOf(allowed.url) });
    try {
      await deployOk(rt, "fetcher");

      const ok = await rt.invoke("fetcher", allowed.url);
      assertEquals(ok.status, 200);
      assertEquals(ok.body, "ok:200");

      const denied = await rt.invoke("fetcher", other.url);
      assertEquals(denied.status, 403);
      assertStringIncludes(denied.body, "denied:NotCapable");
    } finally {
      await rt.stop();
      await allowed.stop();
      await other.stop();
    }
  },
  // The harness's stop() races child exit against a 5s timer; like the other
  // runtime tests, the op sanitizer is off so the pending timer is not a leak.
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "egress: no env and no deploy allowlist means no network at all",
  async fn() {
    const target = listener();
    const rt = await startRuntime();
    try {
      await deployOk(rt, "fetcher");
      const denied = await rt.invoke("fetcher", target.url);
      assertEquals(denied.status, 403);
      assertStringIncludes(denied.body, "denied:NotCapable");
    } finally {
      await rt.stop();
      await target.stop();
    }
  },
  // The harness's stop() races child exit against a 5s timer; like the other
  // runtime tests, the op sanitizer is off so the pending timer is not a leak.
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "egress: deploy payload allowedHosts scopes egress per function on a shared runtime",
  async fn() {
    const allowed = listener();
    const other = listener();
    const rt = await startRuntime();
    try {
      await deployOk(rt, "scoped", [hostOf(allowed.url)]);
      await deployOk(rt, "unscoped");

      assertEquals((await rt.invoke("scoped", allowed.url)).body, "ok:200");
      assertStringIncludes((await rt.invoke("scoped", other.url)).body, "denied:NotCapable");
      // A sibling deployed without the list gets nothing — the grant is per worker.
      assertStringIncludes((await rt.invoke("unscoped", allowed.url)).body, "denied:NotCapable");
    } finally {
      await rt.stop();
      await allowed.stop();
      await other.stop();
    }
  },
  // The harness's stop() races child exit against a 5s timer; like the other
  // runtime tests, the op sanitizer is off so the pending timer is not a leak.
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "egress: env and deploy allowlists are unioned",
  async fn() {
    const fromEnv = listener();
    const fromDeploy = listener();
    const rt = await startRuntime({ allowedHosts: hostOf(fromEnv.url) });
    try {
      await deployOk(rt, "both", [hostOf(fromDeploy.url)]);
      assertEquals((await rt.invoke("both", fromEnv.url)).body, "ok:200");
      assertEquals((await rt.invoke("both", fromDeploy.url)).body, "ok:200");
    } finally {
      await rt.stop();
      await fromEnv.stop();
      await fromDeploy.stop();
    }
  },
  // The harness's stop() races child exit against a 5s timer; like the other
  // runtime tests, the op sanitizer is off so the pending timer is not a leak.
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "egress: malformed allowedHosts is rejected at deploy, never widened",
  async fn() {
    const rt = await startRuntime();
    try {
      const bad = [
        "not-an-array",
        ["*"],
        [""],
        ["a.example.com b.example.com"],
        ["a.example.com,b.example.com"],
        ["https://a.example.com"],
        [42],
        Array.from({ length: 65 }, (_, i) => `h${i}.example.com`),
      ];
      for (const allowedHosts of bad) {
        const res = await rt.raw("/deploy", {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-Runtime-Secret": rt.secret },
          body: JSON.stringify({ id: "bad", code: FETCHER, allowedHosts }),
        });
        await res.body?.cancel();
        assertEquals(res.status, 400, `expected 400 for ${JSON.stringify(allowedHosts)}`);
      }
    } finally {
      await rt.stop();
    }
  },
  // The harness's stop() races child exit against a 5s timer; like the other
  // runtime tests, the op sanitizer is off so the pending timer is not a leak.
  sanitizeOps: false,
  sanitizeResources: false,
});

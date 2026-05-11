// Phase 7 — httpRouter dispatch.
//
// A bundle whose default export is a Router (produced by httpRouter()) carries
// a route table at `__excalibase_routes`. The runtime examines the incoming
// request's URL path + method, looks up the matching route, and runs that
// route's handler with the raw Request. Misses 404. Method mismatches 404.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

// Bundle template that mirrors what the lib's httpRouter() emits at deploy:
// a Router-tagged object carrying both `__excalibase_routes` (extracted by
// the bundler) and a `__excalibase_route_handlers` map keyed by
// "METHOD path" so the runtime can resolve a hit to its handler closure.
const ROUTER_BUNDLE = `{
  kind: "httpRouter",
  __excalibase_routes: [
    { path: "/hello",  method: "GET",  exportName: "default" },
    { path: "/hello",  method: "POST", exportName: "default" },
    { path: "/status", method: "GET",  exportName: "default" },
  ],
  __excalibase_route_handlers: {
    "GET /hello": async (_ctx, _req) =>
      new Response(JSON.stringify({ route: "GET /hello" }),
        { status: 200, headers: { "content-type": "application/json" } }),
    "POST /hello": async (_ctx, req) =>
      new Response(JSON.stringify({ route: "POST /hello", body: await req.text() }),
        { status: 201, headers: { "content-type": "application/json" } }),
    "GET /status": async (_ctx, _req) =>
      new Response("ok", { status: 200 }),
  },
  route: () => null,
  getRoutes: () => [],
}`;

Deno.test({
  name: "httpRouter: GET /hello matches and runs the right handler",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("proj_r__router", bundleDefault(ROUTER_BUNDLE));
      // The invoke envelope routes us into the bundled router via the URL
      // path the gateway forwards. The runtime inspects this URL path,
      // strips the "/functions/v1/{project}/http" prefix (or, in tests,
      // accepts a relative /hello directly), and matches against the route
      // table.
      const res = await fetch(`${rt.baseUrl}/invoke/proj_r__router`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Runtime-Secret": rt.secret,
        },
        body: JSON.stringify({
          method: "GET",
          url: "/hello",
          headers: {},
          body: "",
        }),
      });
      const env = await res.json() as { status: number; body: string };
      assertEquals(env.status, 200);
      const data = JSON.parse(env.body) as { route: string };
      assertEquals(data.route, "GET /hello");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "httpRouter: POST /hello matches its own row (method-sensitive)",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("proj_r2__router", bundleDefault(ROUTER_BUNDLE));
      const res = await fetch(`${rt.baseUrl}/invoke/proj_r2__router`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Runtime-Secret": rt.secret,
        },
        body: JSON.stringify({
          method: "POST",
          url: "/hello",
          headers: {},
          body: "hi",
        }),
      });
      const env = await res.json() as { status: number; body: string };
      assertEquals(env.status, 201);
      const data = JSON.parse(env.body) as { route: string; body: string };
      assertEquals(data.route, "POST /hello");
      assertEquals(data.body, "hi");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "httpRouter: unmatched path returns 404",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("proj_r3__router", bundleDefault(ROUTER_BUNDLE));
      const res = await fetch(`${rt.baseUrl}/invoke/proj_r3__router`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Runtime-Secret": rt.secret,
        },
        body: JSON.stringify({
          method: "GET",
          url: "/unknown",
          headers: {},
          body: "",
        }),
      });
      const env = await res.json() as { status: number };
      assertEquals(env.status, 404);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "httpRouter: method mismatch on a known path returns 404",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("proj_r4__router", bundleDefault(ROUTER_BUNDLE));
      // /status is only declared GET; sending DELETE must 404 (Convex parity:
      // method mismatch is also a 404, not 405).
      const res = await fetch(`${rt.baseUrl}/invoke/proj_r4__router`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Runtime-Secret": rt.secret,
        },
        body: JSON.stringify({
          method: "DELETE",
          url: "/status",
          headers: {},
          body: "",
        }),
      });
      const env = await res.json() as { status: number };
      assertEquals(env.status, 404);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// EXC-359 — a BYOC database reaches the runtime only as the address
// provisioning validated. The deploy secrets carry BYOC_PINNED=1, a DSN whose
// authority is an IP literal, and EXCALIBASE_DB_HOST for TLS SNI. The worker's
// net grant is that ip:port plus the project's egress allowlist (EXC-348) and
// nothing else; an unpinned BYOC DSN is refused.

import {
  assertEquals,
  assertStringIncludes,
  assertThrows,
} from "https://deno.land/std@0.224.0/assert/mod.ts";
import { pinnedTarget, poolConnectOptions, workerNetGrant } from "../runtime/pin.ts";
import { startRuntime } from "./harness.ts";

const PINNED_V4 = "postgres://u:p@203.0.113.5:5432/app?sslmode=require";

Deno.test("pinnedTarget accepts an IPv4 authority", () => {
  assertEquals(pinnedTarget(PINNED_V4), { host: "203.0.113.5", port: 5432, hostPort: "203.0.113.5:5432" });
});

Deno.test("pinnedTarget keeps IPv6 bracketed and defaults the port", () => {
  assertEquals(pinnedTarget("postgres://u:p@[2001:db8::10]/app"), {
    host: "2001:db8::10",
    port: 5432,
    hostPort: "[2001:db8::10]:5432",
  });
});

Deno.test("pinnedTarget refuses a hostname, a multi-host list and garbage", () => {
  for (const url of [
    "postgres://u:p@db.example.com:5432/app",
    "postgres://u:p@203.0.113.5:5432,203.0.113.6:5432/app",
    "not a url",
    "",
  ]) {
    assertThrows(() => pinnedTarget(url), Error, "pinned");
  }
});

Deno.test("workerNetGrant is the pinned address plus the egress allowlist for a pinned deploy", () => {
  const pinned = { BYOC_PINNED: "1", EXCALIBASE_DB_URL: PINNED_V4 };
  assertEquals(workerNetGrant(pinned, []), ["203.0.113.5:5432"]);
  assertEquals(workerNetGrant(pinned, ["api.stripe.com", "203.0.113.5:5432"]), [
    "203.0.113.5:5432",
    "api.stripe.com",
  ]);
});

Deno.test("workerNetGrant is the egress allowlist alone when not pinned", () => {
  assertEquals(workerNetGrant({}, ["a:1", "b:2"]), ["a:1", "b:2"]);
  assertEquals(workerNetGrant({ EXCALIBASE_DB_URL: "postgres://u:p@db.example.com/app" }, []), false);
});

Deno.test("workerNetGrant refuses a pinned deploy whose DSN is not an IP literal", () => {
  assertThrows(
    () => workerNetGrant({ BYOC_PINNED: "1", EXCALIBASE_DB_URL: "postgres://u:p@db.example.com/app" }, []),
    Error,
    "pinned",
  );
  assertThrows(() => workerNetGrant({ BYOC_PINNED: "1" }, []), Error, "pinned");
});

Deno.test("poolConnectOptions carries the hostname as TLS servername when pinned", () => {
  assertEquals(
    poolConnectOptions({ url: PINNED_V4, pinned: true, hostName: "db.example.com" }),
    { ssl: { rejectUnauthorized: false, servername: "db.example.com" } },
  );
  assertEquals(
    poolConnectOptions({
      url: "postgres://u:p@203.0.113.5:5432/app?sslmode=verify-full",
      pinned: true,
      hostName: "db.example.com",
    }),
    { ssl: { servername: "db.example.com" } },
  );
});

Deno.test("poolConnectOptions leaves managed connections untouched", () => {
  assertEquals(poolConnectOptions({ url: "postgres://u:p@pg.ns.svc:5432/app", pinned: false }), {});
});

Deno.test("poolConnectOptions refuses an unpinned BYOC url and a pin without a hostname", () => {
  assertThrows(
    () => poolConnectOptions({ url: "postgres://u:p@db.example.com/app", pinned: true, hostName: "db.example.com" }),
    Error,
    "pinned",
  );
  assertThrows(() => poolConnectOptions({ url: PINNED_V4, pinned: true }), Error, "EXCALIBASE_DB_HOST");
});

// Local listeners stand in for a database, an allowlisted API and a stranger.
// The worker may connect to the address its DSN was pinned to and to the
// egress allowlist, and to nothing else.
function listen(): { port: number; close: () => void } {
  const listener = Deno.listen({ hostname: "127.0.0.1", port: 0 });
  (async () => {
    for await (const conn of listener) conn.close();
  })().catch(() => {});
  return { port: (listener.addr as Deno.NetAddr).port, close: () => listener.close() };
}

const CONNECT_FN = `
export default async (req) => {
  const { port } = await req.json();
  try {
    const conn = await Deno.connect({ hostname: "127.0.0.1", port });
    conn.close();
    return new Response("connected");
  } catch (e) {
    return new Response("refused:" + e.name);
  }
};`;

Deno.test({
  name: "worker connects to the pinned address and the egress allowlist only",
  sanitizeOps: false,
  sanitizeResources: false,
  async fn() {
    const pinned = listen();
    const allowlisted = listen();
    const wrong = listen();
    const rt = await startRuntime({ allowedHosts: `127.0.0.1:${allowlisted.port}` });
    try {
      const deployed = await rt.deploy("byoc-fn", CONNECT_FN, {
        BYOC_PINNED: "1",
        EXCALIBASE_DB_HOST: "db.example.com",
        EXCALIBASE_DB_URL: `postgres://u:p@127.0.0.1:${pinned.port}/app?sslmode=require`,
      });
      assertEquals(deployed.status, 201, await deployed.text());

      assertEquals((await rt.invoke("byoc-fn", { port: pinned.port })).body, "connected");
      assertEquals((await rt.invoke("byoc-fn", { port: allowlisted.port })).body, "connected");

      const denied = await rt.invoke("byoc-fn", { port: wrong.port });
      assertStringIncludes(denied.body, "refused:NotCapable");
    } finally {
      await rt.stop();
      pinned.close();
      allowlisted.close();
      wrong.close();
    }
  },
});

Deno.test({
  name: "deploy refuses a BYOC DSN that is not pinned",
  sanitizeOps: false,
  sanitizeResources: false,
  async fn() {
    const rt = await startRuntime();
    try {
      const res = await rt.deploy("unpinned", CONNECT_FN, {
        BYOC_PINNED: "1",
        EXCALIBASE_DB_URL: "postgres://u:p@db.example.com:5432/app?sslmode=require",
      });
      const text = await res.text();
      assertEquals(res.status, 400, text);
      assertStringIncludes(text, "pinned");
      const health = await (await fetch(`${rt.baseUrl}/health`)).json();
      assertEquals(health.scripts, 0);
    } finally {
      await rt.stop();
    }
  },
});

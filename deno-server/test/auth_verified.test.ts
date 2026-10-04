// ctx.auth is built only from a token the gateway verified (EXC-518). The
// gateway marks a verified token with X-Excalibase-Auth-Verified: 1 and drops
// any marker a caller sent; a function that does not verify tokens still sees
// the Authorization header itself, but ctx.auth stays empty, so a role check
// in it cannot be satisfied by an unsigned token.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { makeUnsignedJwt, startRuntime } from "./harness.ts";

const identityQuery = `globalThis.__excalibase_default = {
  kind: "query",
  args: { parse: (a) => a },
  handler: async (ctx, _args) => ({ claims: ctx.auth.claims, id: await ctx.auth.getUserIdentity() }),
};`;

const identityHttpAction = `globalThis.__excalibase_default = {
  kind: "httpAction",
  handler: async (ctx, _req) => new Response(JSON.stringify({ claims: ctx.auth.claims }),
    { headers: { "content-type": "application/json" } }),
};`;

Deno.test({
  name: "an unverified token gives no identity",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("unverified", identityQuery);
      const jwt = makeUnsignedJwt({ sub: "mallory", role: "staff" });
      const res = await rt.invoke("unverified", { args: {} }, { Authorization: "Bearer " + jwt });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.claims, null);
      assertEquals(parsed.data.id, null);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "a verified token gives the identity",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("verified", identityQuery);
      const jwt = makeUnsignedJwt({ sub: "sam", role: "staff" });
      const res = await rt.invoke("verified", { args: {} }, {
        Authorization: "Bearer " + jwt,
        "X-Excalibase-Auth-Verified": "1",
      });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.claims.role, "staff");
      assertEquals(parsed.data.id.subject, "sam");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "an httpAction gets claims only from a verified token",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("httpid", identityHttpAction);
      const jwt = makeUnsignedJwt({ sub: "mallory", role: "staff" });
      const unverified = await rt.invoke("httpid", {}, { Authorization: "Bearer " + jwt });
      assertEquals(JSON.parse(unverified.body).claims, null);
      const verified = await rt.invoke("httpid", {}, { Authorization: "Bearer " + jwt, "X-Excalibase-Auth-Verified": "1" });
      assertEquals(JSON.parse(verified.body).claims.sub, "mallory");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// Phase 12 — runtime wiring for `ctx.auth.getUserIdentity()`.
//
// The Excalibase Deno worker decodes the Authorization Bearer JWT and
// must expose it to handler code via `ctx.auth.getUserIdentity()` in
// addition to the raw `ctx.auth.claims`. Shape mirrors Convex's
// `UserIdentity` interface
// (https://docs.convex.dev/api/interfaces/server.UserIdentity):
//   - `tokenIdentifier` = `${iss}|${sub}`
//   - `subject` = `sub`
//   - `issuer` = `iss`
//   - standard optional slots (name, email, email_verified, picture, …)
//   - custom claims pass through unchanged
// When no Authorization header is present, `getUserIdentity()` resolves
// to `null`.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { makeUnsignedJwt, startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "ctx.auth.getUserIdentity returns Convex-shape UserIdentity from JWT",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const id = await ctx.auth.getUserIdentity();
          return { id };
        },
      }`);
      await rt.deploy("v2identity", fnCode);

      const jwt = makeUnsignedJwt({
        sub: "abc",
        iss: "https://issuer.example",
        email: "a@b",
        email_verified: true,
        name: "Alice",
        picture: "https://p/alice.png",
        // Custom claim — must pass through via the index signature.
        tenant: "x",
      });
      const res = await rt.invoke("v2identity", { args: {} }, {
        Authorization: "Bearer " + jwt,
      });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.id.tokenIdentifier, "https://issuer.example|abc");
      assertEquals(parsed.data.id.subject, "abc");
      assertEquals(parsed.data.id.issuer, "https://issuer.example");
      assertEquals(parsed.data.id.email, "a@b");
      assertEquals(parsed.data.id.emailVerified, true);
      assertEquals(parsed.data.id.name, "Alice");
      assertEquals(parsed.data.id.pictureUrl, "https://p/alice.png");
      assertEquals(parsed.data.id.tenant, "x");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.auth.getUserIdentity returns null when Authorization header is absent",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const id = await ctx.auth.getUserIdentity();
          return { id };
        },
      }`);
      await rt.deploy("v2identityNull", fnCode);
      const res = await rt.invoke("v2identityNull", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.id, null);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.auth.getUserIdentity exposed on action ctx (httpAction)",
  async fn() {
    // Convex parity: actions and httpActions also see the typed identity.
    // We exercise the v2 action dispatch path here; the httpAction path
    // shares the same worker shim and is covered structurally.
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "action",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const id = await ctx.auth.getUserIdentity();
          return { sub: id ? id.subject : null };
        },
      }`);
      await rt.deploy("v2actId", fnCode);
      const jwt = makeUnsignedJwt({ sub: "u9", iss: "https://i" });
      const res = await rt.invoke("v2actId", { args: {} }, {
        Authorization: "Bearer " + jwt,
      });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.sub, "u9");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.auth.getUserIdentity falls back to empty issuer/subject when missing",
  async fn() {
    // Tokens missing `iss` or `sub` shouldn't crash — Convex's shape
    // documents both as strings, so we default to "" rather than
    // undefined to keep call sites tidy.
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const id = await ctx.auth.getUserIdentity();
          return { id };
        },
      }`);
      await rt.deploy("v2idFallback", fnCode);
      const jwt = makeUnsignedJwt({ role: "anon" });
      const res = await rt.invoke("v2idFallback", { args: {} }, {
        Authorization: "Bearer " + jwt,
      });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.id.tokenIdentifier, "|");
      assertEquals(parsed.data.id.subject, "");
      assertEquals(parsed.data.id.issuer, "");
      // Custom claim still surfaces.
      assertEquals(parsed.data.id.role, "anon");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

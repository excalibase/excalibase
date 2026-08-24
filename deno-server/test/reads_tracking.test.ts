// Phase 15b — Reads tracking envelope. The HTTP invoke response carries a
// `reads: string[]` field alongside the handler's `result` when the caller
// opts in via `X-Excalibase-Envelope: v1`. Without the header, the response
// shape is unchanged (legacy `{data: ...}` body) so existing callers stay
// untouched.
//
// The reads set is populated by the per-request ActiveTxn entry as the
// handler issues db read ops. Phase 9b.A already wires the bookkeeping for
// `writes`; this test exercises the symmetric `reads` path and the new
// envelope shape graphql 15a consumes to switch from
// "re-invoke on ANY project commit" to "re-invoke only if commit.table is in
// the sub's tracked reads".
//
// Edge cases covered:
//  - Function reads from one collection, writes to another → reads contains
//    only the read collection.
//  - Function reads AND writes the same collection → reads contains the
//    collection (writes do not subtract from reads).
//  - Function writes only (no reads) → reads is an empty array.
//  - Caller omits the envelope header → response body is the raw `{data:...}`
//    shape (no `result`/`reads` keys).

import { assert, assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

const ENVELOPE_HEADER = "X-Excalibase-Envelope";
const ENVELOPE_VERSION = "v1";

Deno.test({
  name: "phase15b: envelope opt-in returns {result, reads} with read collection",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "posts");
      await createCollection(pg.url, "audit_log");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const projectId = "proj_envelope";
        // Function reads from `posts` and writes to `audit_log`.
        await rt.deploy(`${projectId}__readWrite`, bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const docs = await ctx.db.query("posts").collect();
            await ctx.db.collection("audit_log").insert({ count: docs.length });
            return { read: docs.length, handler: "ok" };
          },
        }`));

        // With envelope header — body MUST be {result, reads} and reads MUST
        // contain "posts" (the only read collection — audit_log is a write).
        const withEnv = await rt.invoke(
          `${projectId}__readWrite`,
          { args: {} },
          { [ENVELOPE_HEADER]: ENVELOPE_VERSION },
        );
        assertEquals(withEnv.status, 200, `body=${withEnv.body}`);
        const wrapped = JSON.parse(withEnv.body) as {
          result?: unknown;
          reads?: string[];
          data?: unknown;
        };
        assertExists(wrapped.result, "envelope must have a top-level `result` key");
        assert(Array.isArray(wrapped.reads), "envelope must have a `reads` array");
        assertEquals(
          wrapped.reads?.includes("posts"),
          true,
          `reads should include "posts", got ${JSON.stringify(wrapped.reads)}`,
        );
        // Writes MUST NOT pollute reads. audit_log was only written.
        assertEquals(
          wrapped.reads?.includes("audit_log"),
          false,
          `reads should not include write-only "audit_log", got ${JSON.stringify(wrapped.reads)}`,
        );
        // The legacy `data` key MUST NOT appear at the top level — graphql
        // 15a switches the body parse to `body.path("result")` so leaking
        // both keys would confuse the precise-dep filter.
        assertEquals(
          (wrapped as { data?: unknown }).data,
          undefined,
          "envelope must not carry legacy `data` alongside `result`",
        );
        // The handler's return value is reproduced verbatim inside `result`.
        const resultObj = wrapped.result as { read: number; handler: string };
        assertEquals(resultObj.handler, "ok");

        // Without the header — body MUST be the legacy `{data: ...}` shape so
        // pre-15b callers (graphql 14, SDK direct invokes, ad-hoc curl) stay
        // bit-for-bit compatible.
        const legacy = await rt.invoke(`${projectId}__readWrite`, { args: {} });
        assertEquals(legacy.status, 200, `body=${legacy.body}`);
        const legacyBody = JSON.parse(legacy.body) as {
          data?: unknown;
          result?: unknown;
          reads?: unknown;
        };
        assertExists(legacyBody.data, "legacy body must keep the `data` field");
        assertEquals(
          legacyBody.result,
          undefined,
          "legacy body must NOT carry `result` when caller didn't opt in",
        );
        assertEquals(
          legacyBody.reads,
          undefined,
          "legacy body must NOT carry `reads` when caller didn't opt in",
        );
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "phase15b: read-then-write on same collection records read",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "counter");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const projectId = "proj_envelope_rw";
        await rt.deploy(`${projectId}__sameCollection`, bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            await ctx.db.collection("counter").find({});
            return await ctx.db.collection("counter").insert({ tick: 1 });
          },
        }`));

        const res = await rt.invoke(
          `${projectId}__sameCollection`,
          { args: {} },
          { [ENVELOPE_HEADER]: ENVELOPE_VERSION },
        );
        assertEquals(res.status, 200, `body=${res.body}`);
        const parsed = JSON.parse(res.body) as { reads?: string[] };
        assert(Array.isArray(parsed.reads));
        assertEquals(
          parsed.reads?.includes("counter"),
          true,
          "read then write on same collection: reads must include it",
        );
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "phase15b: write-only function returns empty reads array",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "events");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const projectId = "proj_envelope_w";
        await rt.deploy(`${projectId}__writeOnly`, bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            return await ctx.db.collection("events").insert({ k: 1 });
          },
        }`));

        const res = await rt.invoke(
          `${projectId}__writeOnly`,
          { args: {} },
          { [ENVELOPE_HEADER]: ENVELOPE_VERSION },
        );
        assertEquals(res.status, 200, `body=${res.body}`);
        const parsed = JSON.parse(res.body) as { reads?: string[]; result?: unknown };
        assertExists(parsed.result);
        assert(Array.isArray(parsed.reads));
        assertEquals(
          parsed.reads?.length,
          0,
          `write-only handler must produce empty reads, got ${JSON.stringify(parsed.reads)}`,
        );
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

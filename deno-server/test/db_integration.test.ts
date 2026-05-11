// ctx.db integration tests — end-to-end through a real Postgres + deployed
// function. Each test boots its own Postgres container and runtime so they
// can run concurrently without leaking state.
//
// The function under test calls ctx.db.collection("users").insert(...) and
// returns the result. We then directly query the database to assert that
// the row landed correctly, matching what the Java service would write.

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "ctx.db.collection('users').insert returns the inserted doc with _id + _creationTime",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "users");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const inserted = await ctx.db.collection("users").insert({ name: args.name, status: "active" });
            return inserted;
          },
        }`);
        await rt.deploy("dbi-insert", code);

        const res = await rt.invoke("dbi-insert", { args: { name: "alice" } });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        assertExists(parsed.data._id, "_id should be populated");
        assertEquals(parsed.data.name, "alice");
        assertEquals(parsed.data.status, "active");
        assertEquals(typeof parsed.data._creationTime, "number");
        // Convex-shape id: 30-char base32 lowercase letters+digits.
        if (typeof parsed.data._id !== "string" || !/^[a-z0-9]{30}$/.test(parsed.data._id)) {
          throw new Error(`_id is not 30-char base32 lowercase: ${parsed.data._id}`);
        }
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
  name: "ctx.db.collection().find with equality filter returns inserted rows",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "posts");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const c = ctx.db.collection("posts");
            await c.insert({ title: "first", status: "draft" });
            await c.insert({ title: "second", status: "published" });
            await c.insert({ title: "third", status: "draft" });
            return await c.find({ status: "draft" });
          },
        }`);
        await rt.deploy("dbi-find", code);
        const res = await rt.invoke("dbi-find", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        assertEquals(Array.isArray(parsed.data), true);
        assertEquals(parsed.data.length, 2);
        const titles = parsed.data.map((d: { title: string }) => d.title).sort();
        assertEquals(titles, ["first", "third"]);
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
  name: "ctx.db.collection().getById returns the row, or null when missing",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "items");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("items");
            const inserted = await c.insert({ name: "thing" });
            const found = await c.getById(inserted._id);
            const missing = await c.getById("0123456789abcdefghijklmnopqrst");
            return { found, missing };
          },
        }`);
        await rt.deploy("dbi-getById", code);
        const res = await rt.invoke("dbi-getById", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data.found.name, "thing");
        assertEquals(parsed.data.missing, null);
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
  name: "ctx.db is null for action handlers (Convex parity)",
  async fn() {
    const pg = await startPostgres();
    try {
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = bundle(`{
          kind: "action",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => ({ dbIsNull: ctx.db === null }),
        }`);
        await rt.deploy("dbi-action", code);
        const res = await rt.invoke("dbi-action", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data.dbIsNull, true);
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

// Full-text search integration test — exercises the new `search` op on
// `ctx.db.collection(name).search()` against a real Postgres container
// with a `search_text` tsvector column (generated from data->>'body').
//
// Schema parity: matches what `CollectionSchemaManager.addSearchColumn`
// creates on the Java side — generated `search_text tsvector` column over
// `to_tsvector('english', data->>'body')` plus a GIN index. No extensions
// are required because tsvector ships in core Postgres 16.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

async function createSearchableCollection(pgUrl: string, collection: string): Promise<void> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    await sql`CREATE SCHEMA IF NOT EXISTS nosql`;
    const quoted = `nosql."${collection.replace(/"/g, '""')}"`;
    // Phase 5b: schema mirrors Phase 5a's ApplySchema output —
    // `_id text PRIMARY KEY`, `_creation_time double precision`, and a
    // `doc jsonb` column for the user payload. The search_text column
    // generates over `doc->>'body'` (was `data->>'body'` pre-5b).
    await sql.unsafe(
      `CREATE TABLE IF NOT EXISTS ${quoted} (
         _id text PRIMARY KEY,
         _creation_time double precision NOT NULL,
         doc jsonb NOT NULL DEFAULT '{}'::jsonb,
         search_text tsvector GENERATED ALWAYS AS (
           to_tsvector('english', coalesce(doc->>'body', ''))
         ) STORED
       )`,
    );
    await sql.unsafe(
      `CREATE INDEX IF NOT EXISTS idx_${collection}_search
         ON ${quoted} USING gin(search_text)`,
    );
  } finally {
    await sql.end({ timeout: 1 });
  }
}

Deno.test({
  name: "search returns matches ordered by ts_rank descending",
  async fn() {
    const pg = await startPostgres();
    try {
      await createSearchableCollection(pg.url, "articles");
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
            const c = ctx.db.collection("articles");
            await c.insertMany([
              { title: "postgres fts", body: "PostgreSQL tsvector and tsquery power full-text search" },
              { title: "mysql",        body: "MySQL has its own full-text search implementation" },
              { title: "general",      body: "Search engines index documents for fast retrieval" },
              { title: "postgres only",body: "PostgreSQL is a powerful relational database" },
              { title: "unrelated",    body: "Cooking recipes for pasta" },
            ]);
            const top = await c.search("tsvector tsquery");
            return top.map((d) => d.title);
          },
        }`);
        await rt.deploy("dbi-search", code);
        const res = await rt.invoke("dbi-search", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        if (!Array.isArray(parsed.data) || parsed.data.length === 0) {
          throw new Error(`expected non-empty array, got: ${JSON.stringify(parsed)}`);
        }
        // "postgres fts" is the only doc that mentions both terms; it must rank first.
        assertEquals(parsed.data[0], "postgres fts");
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
  name: "search with no matches returns an empty array",
  async fn() {
    const pg = await startPostgres();
    try {
      await createSearchableCollection(pg.url, "blog");
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
            const c = ctx.db.collection("blog");
            await c.insert({ title: "hello", body: "hello world" });
            return await c.search("nonexistentwordxyz");
          },
        }`);
        await rt.deploy("dbi-search-empty", code);
        const res = await rt.invoke("dbi-search-empty", { args: {} });
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data, []);
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
  name: "search respects the limit option",
  async fn() {
    const pg = await startPostgres();
    try {
      await createSearchableCollection(pg.url, "manydocs");
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
            const c = ctx.db.collection("manydocs");
            await c.insertMany([
              { body: "search engine alpha" },
              { body: "search engine bravo" },
              { body: "search engine charlie" },
              { body: "search engine delta" },
              { body: "search engine echo" },
            ]);
            return await c.search("search", { limit: 2 });
          },
        }`);
        await rt.deploy("dbi-search-limit", code);
        const res = await rt.invoke("dbi-search-limit", { args: {} });
        const parsed = JSON.parse(res.body);
        assertEquals(Array.isArray(parsed.data), true);
        if (parsed.data.length !== 2) {
          throw new Error(`expected limit:2 to cap result count, got ${parsed.data.length}`);
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
  name: "search on a collection without search_text column surfaces a clear error",
  async fn() {
    const pg = await startPostgres();
    try {
      // The "no_search" collection has the Convex-shape `_id`/`doc` columns
      // but lacks the generated `search_text` tsvector. We expect the user
      // to see a clear error from search() rather than a silent zero-row.
      const postgres = (await import("npm:postgres@3.4.4")).default;
      const sql = postgres(pg.url, { onnotice: () => {} });
      try {
        await sql`CREATE SCHEMA IF NOT EXISTS nosql`;
        await sql.unsafe(
          `CREATE TABLE IF NOT EXISTS nosql.no_search (
             _id text PRIMARY KEY,
             _creation_time double precision NOT NULL,
             doc jsonb NOT NULL DEFAULT '{}'::jsonb
           )`,
        );
      } finally {
        await sql.end({ timeout: 1 });
      }

      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = bundle(`{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            try {
              await ctx.db.collection("no_search").search("anything");
              return { ok: true };
            } catch (e) {
              return { error: String(e && e.message || e) };
            }
          },
        }`);
        await rt.deploy("dbi-search-noschema", code);
        const res = await rt.invoke("dbi-search-noschema", { args: {} });
        const parsed = JSON.parse(res.body);
        // We don't pin the exact wording — postgres.js + the runtime will
        // surface the underlying SQLSTATE 42703 (undefined column). What
        // matters is the user sees an error, not a silent empty result.
        if (parsed.data.ok === true) {
          throw new Error("expected search on collection without search_text to fail");
        }
        assertEquals(typeof parsed.data.error, "string");
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

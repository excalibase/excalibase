// pgvector k-NN search integration test — exercises the new
// `vectorSearch` op on `ctx.db.collection(name).vectorSearch()` against
// a real Postgres container that has the pgvector extension loaded.
//
// Schema parity: matches `CollectionSchemaManager.addVectorColumn` on
// the Java side — `embedding vector(N)` column + HNSW index using
// `vector_cosine_ops`. Cosine distance (`<=>`) is the operator class
// chosen by the Java compiler in `compileVectorSearch`, so we match it
// here to keep cross-runtime ordering identical.
//
// pgvector is required: the container image `pgvector/pgvector:pg16` ships
// the extension preinstalled and the test runs `CREATE EXTENSION` before
// using it. Phase 4 must ensure project Postgres images include pgvector
// when collections declare a `vector` field.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { startPostgres } from "./pg_harness.ts";

interface VecPgHandle {
  url: string;
  host: string;
  port: number;
  stop: () => Promise<void>;
}

// Spin up a pgvector-enabled Postgres through the shared harness, so this
// test gets the same readiness contract (a real SQL handshake over TCP —
// TCP-listen + pg_isready is not enough, see pg_harness.ts) and the same
// teardown as every other DB-backed test. Only the image differs:
// pgvector/pgvector:pg16 ships the extension so CREATE EXTENSION succeeds.
async function startPgVector(): Promise<VecPgHandle> {
  const pg = await startPostgres({ image: "pgvector/pgvector:pg16" });
  return { url: pg.url, host: pg.host, port: pg.port, stop: pg.stop };
}

async function createVectorCollection(pgUrl: string, collection: string, dims: number): Promise<void> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    await sql.unsafe(`CREATE EXTENSION IF NOT EXISTS vector`);
    await sql`CREATE SCHEMA IF NOT EXISTS nosql`;
    const quoted = `nosql."${collection.replace(/"/g, '""')}"`;
    // Phase 5b: same Convex-shape columns as the rest of the suite — the
    // embedding column is added alongside.
    await sql.unsafe(
      `CREATE TABLE IF NOT EXISTS ${quoted} (
         _id text PRIMARY KEY,
         _creation_time double precision NOT NULL,
         doc jsonb NOT NULL DEFAULT '{}'::jsonb,
         embedding vector(${dims})
       )`,
    );
    await sql.unsafe(
      `CREATE INDEX IF NOT EXISTS idx_${collection}_vector
         ON ${quoted} USING hnsw(embedding vector_cosine_ops)`,
    );
  } finally {
    await sql.end({ timeout: 1 });
  }
}

// Tiny Convex-format id generator for seed rows — keep the algorithm
// inline to avoid depending on the runtime's `runtime/ids.ts` from a test
// fixture (which would create a tight coupling). The runtime asserts a
// 30-char lowercase base32 shape; matching it here keeps the seed rows
// indistinguishable from runtime-minted ones.
function seedId(): string {
  const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz";
  let out = "";
  const bytes = new Uint8Array(30);
  crypto.getRandomValues(bytes);
  for (let i = 0; i < 30; i++) out += alphabet[bytes[i] % alphabet.length];
  return out;
}

// Seed embeddings directly via SQL — production wires this through
// `compileSetEmbedding` but for the search-path test we only need the
// vectors present.
async function seedVectors(
  pgUrl: string,
  collection: string,
  rows: Array<{ title: string; embedding: number[] }>,
): Promise<void> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    const quoted = `nosql."${collection.replace(/"/g, '""')}"`;
    const table = sql.unsafe(quoted);
    for (const r of rows) {
      // Use `sql.json(...)` so postgres.js binds the JS object as a
      // JSONB OBJECT rather than a JSONB STRING. Passing a pre-stringified
      // value through `$1::jsonb` would double-encode (the runtime treats
      // the string itself as the JSONB scalar).
      const vec = `[${r.embedding.join(",")}]`;
      await sql`INSERT INTO ${table} (_id, _creation_time, doc, embedding)
                VALUES (${seedId()}, ${Date.now()}, ${sql.json({ title: r.title })}, ${vec}::vector)`;
    }
  } finally {
    await sql.end({ timeout: 1 });
  }
}

Deno.test({
  name: "vectorSearch orders rows by cosine distance from the query embedding",
  async fn() {
    const pg = await startPgVector();
    try {
      await createVectorCollection(pg.url, "docs", 3);
      await seedVectors(pg.url, "docs", [
        { title: "origin", embedding: [1, 0, 0] },
        { title: "near",   embedding: [0.9, 0.1, 0] },
        { title: "other",  embedding: [0, 1, 0] },
        { title: "far",    embedding: [0, 0, 1] },
      ]);
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = `globalThis.__excalibase_default = {
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("docs");
            const top = await c.vectorSearch([1, 0, 0]);
            return top.map((d) => d.title);
          },
        };`;
        await rt.deploy("dbi-vec", code);
        const res = await rt.invoke("dbi-vec", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        // Cosine distance [1,0,0] vs each row:
        //   origin → 0 (best), near → ~0.0055, other → 1, far → 1.
        if (!Array.isArray(parsed.data) || parsed.data.length < 2) {
          throw new Error(`expected array of titles, got: ${JSON.stringify(parsed)}`);
        }
        assertEquals(parsed.data[0], "origin");
        assertEquals(parsed.data[1], "near");
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
  name: "vectorSearch respects the topK option",
  async fn() {
    const pg = await startPgVector();
    try {
      await createVectorCollection(pg.url, "vectors", 3);
      await seedVectors(pg.url, "vectors", [
        { title: "a", embedding: [1, 0, 0] },
        { title: "b", embedding: [0, 1, 0] },
        { title: "c", embedding: [0, 0, 1] },
        { title: "d", embedding: [0.5, 0.5, 0] },
      ]);
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = `globalThis.__excalibase_default = {
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("vectors");
            return await c.vectorSearch([1, 0, 0], { topK: 2 });
          },
        };`;
        await rt.deploy("dbi-topk", code);
        const res = await rt.invoke("dbi-topk", { args: {} });
        const parsed = JSON.parse(res.body);
        if (!Array.isArray(parsed.data) || parsed.data.length !== 2) {
          throw new Error(`expected exactly 2 results, got ${JSON.stringify(parsed.data)}`);
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
  name: "vectorSearch with an additional filter AND-combines predicates",
  async fn() {
    const pg = await startPgVector();
    try {
      await createVectorCollection(pg.url, "items", 3);
      await seedVectors(pg.url, "items", [
        { title: "alpha-keep", embedding: [1, 0, 0] },
        { title: "beta-skip",  embedding: [0.95, 0.05, 0] },
        { title: "gamma-keep", embedding: [0.5, 0.5, 0] },
        { title: "delta-skip", embedding: [0.5, 0.5, 0] },
      ]);
      // Tag the rows we want to keep by inserting `category=keep` into doc.
      const postgres = (await import("npm:postgres@3.4.4")).default;
      const sql = postgres(pg.url, { onnotice: () => {} });
      try {
        await sql.unsafe(
          `UPDATE nosql.items SET doc = doc || '{"category":"keep"}'::jsonb
             WHERE (doc->>'title') LIKE '%-keep'`,
        );
        await sql.unsafe(
          `UPDATE nosql.items SET doc = doc || '{"category":"skip"}'::jsonb
             WHERE (doc->>'title') LIKE '%-skip'`,
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
        const code = `globalThis.__excalibase_default = {
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("items");
            const hits = await c.vectorSearch([1, 0, 0], {
              topK: 5,
              filter: { category: "keep" },
            });
            return hits.map((d) => d.title);
          },
        };`;
        await rt.deploy("dbi-vec-filter", code);
        const res = await rt.invoke("dbi-vec-filter", { args: {} });
        const parsed = JSON.parse(res.body);
        if (!Array.isArray(parsed.data)) {
          throw new Error(`expected array, got ${JSON.stringify(parsed)}`);
        }
        // Both skips must be filtered out, both keeps must be present.
        const set = new Set(parsed.data);
        if (!set.has("alpha-keep") || !set.has("gamma-keep") || set.has("beta-skip") || set.has("delta-skip")) {
          throw new Error("filter result unexpected: " + JSON.stringify(parsed.data));
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

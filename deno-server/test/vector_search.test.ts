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
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime } from "./harness.ts";

interface VecPgHandle {
  url: string;
  host: string;
  port: number;
  stop: () => Promise<void>;
}

async function findFreePort(): Promise<number> {
  const l = Deno.listen({ port: 0 });
  const p = (l.addr as Deno.NetAddr).port;
  l.close();
  await delay(10);
  return p;
}

async function isReady(host: string, port: number): Promise<boolean> {
  try {
    const conn = await Deno.connect({ hostname: host, port });
    conn.close();
    return true;
  } catch (_) {
    return false;
  }
}

// Spin up a pgvector-enabled Postgres. Mirrors pg_harness.startPostgres but
// uses the pgvector/pgvector image so CREATE EXTENSION succeeds without
// adding compile-time deps to plain postgres:16-alpine.
async function startPgVector(): Promise<VecPgHandle> {
  const port = await findFreePort();
  const host = "127.0.0.1";
  const pass = "test-pass";
  const db = "excalibase_test";
  const name = `excalibase-pgvtest-${crypto.randomUUID().slice(0, 8)}`;

  const run = new Deno.Command("docker", {
    args: [
      "run",
      "-d",
      "--rm",
      "--name", name,
      "-e", `POSTGRES_PASSWORD=${pass}`,
      "-e", `POSTGRES_DB=${db}`,
      "-p", `${port}:5432`,
      "pgvector/pgvector:pg16",
    ],
    stdout: "piped",
    stderr: "piped",
  });
  const { code, stderr } = await run.output();
  if (code !== 0) {
    throw new Error(`docker run failed: ${new TextDecoder().decode(stderr)}`);
  }

  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    if (await isReady(host, port)) break;
    await delay(200);
  }
  // pg_isready loop — TCP up != accepting queries.
  while (Date.now() < deadline) {
    const probe = await new Deno.Command("docker", {
      args: ["exec", name, "pg_isready", "-U", "postgres", "-d", db],
      stdout: "null",
      stderr: "null",
    }).output();
    if (probe.code === 0) break;
    await delay(300);
  }

  const stop = async () => {
    try {
      await new Deno.Command("docker", {
        args: ["kill", name],
        stdout: "null",
        stderr: "null",
      }).output();
    } catch (_) { /* already gone */ }
  };

  return {
    url: `postgresql://postgres:${pass}@${host}:${port}/${db}`,
    host, port, stop,
  };
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

// Postgres test harness — shells out to `docker run -d postgres:16-alpine`,
// waits for the database to accept connections, and returns the JDBC-style
// URL plus a teardown handle. Used by the ctx.db integration tests.
//
// Why Docker shell-out rather than `npm:testcontainers`? Testcontainers
// targets Node; running it inside Deno requires polyfilling several Node
// APIs and pulling a hefty dependency tree. A direct `docker run` is one
// shell call and Deno can poll TCP readiness with `Deno.connect`. The
// trade-off: we don't get the Ryuk reaper, so we name the container and
// kill it explicitly in the teardown.

import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";

export interface PgHandle {
  /** postgres:// URL for connecting from the host machine. */
  url: string;
  /** Host the worker should be allowed to net-connect to. */
  host: string;
  /** Port the worker should be allowed to net-connect to. */
  port: number;
  containerId: string;
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

/**
 * Start a Postgres container and wait until it accepts connections.
 * The caller is responsible for calling `handle.stop()` in a `finally`.
 */
export async function startPostgres(): Promise<PgHandle> {
  const port = await findFreePort();
  const host = "127.0.0.1";
  const pass = "test-pass";
  const db = "excalibase_test";
  const name = `excalibase-pgtest-${crypto.randomUUID().slice(0, 8)}`;

  const run = new Deno.Command("docker", {
    args: [
      "run",
      "-d",
      "--rm",
      "--name",
      name,
      "-e",
      `POSTGRES_PASSWORD=${pass}`,
      "-e",
      `POSTGRES_DB=${db}`,
      "-p",
      `${port}:5432`,
      "postgres:16-alpine",
    ],
    stdout: "piped",
    stderr: "piped",
  });
  const { code, stdout, stderr } = await run.output();
  if (code !== 0) {
    throw new Error(`docker run failed: ${new TextDecoder().decode(stderr)}`);
  }
  const containerId = new TextDecoder().decode(stdout).trim();

  // Wait for TCP listen + ready-to-accept by trying actual SQL.
  const deadline = Date.now() + 30_000;
  let listening = false;
  while (Date.now() < deadline) {
    if (await isReady(host, port)) {
      listening = true;
      break;
    }
    await delay(200);
  }
  if (!listening) {
    await stopContainer(name);
    throw new Error(`postgres did not start on :${port} within 30s`);
  }

  // TCP listening != accepting queries — Postgres listens before it finishes
  // initdb. Poll `pg_isready` inside the container until it returns 0.
  while (Date.now() < deadline) {
    const probe = await new Deno.Command("docker", {
      args: ["exec", name, "pg_isready", "-U", "postgres", "-d", db],
      stdout: "null",
      stderr: "null",
    }).output();
    if (probe.code === 0) break;
    await delay(300);
  }

  return {
    url: `postgresql://postgres:${pass}@${host}:${port}/${db}`,
    host,
    port,
    containerId,
    stop: () => stopContainer(name),
  };
}

async function stopContainer(name: string): Promise<void> {
  try {
    await new Deno.Command("docker", {
      args: ["kill", name],
      stdout: "null",
      stderr: "null",
    }).output();
  } catch (_) {
    // If kill fails the container is already gone (--rm) — fine.
  }
}

/**
 * Apply the Convex-shape NoSQL schema migration mirroring Phase 5a's
 * `ApplySchema` in server-go. Creates a `nosql` schema and a single
 * collection table with `_id`, `_creation_time`, and `doc` columns. This
 * replaces the pre-5b `(id uuid, data jsonb, created_at, updated_at)` shape
 * so the deno runtime can address the same columns the Go migrator emits.
 */
export async function createCollection(pgUrl: string, collection: string): Promise<void> {
  // Use a one-shot Postgres connection from the host via npm:postgres to
  // avoid bringing in psql/jdbc just for migrations.
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    await sql`CREATE SCHEMA IF NOT EXISTS nosql`;
    // Identifier is fully test-controlled and validated by the test author —
    // we still avoid raw interpolation by wrapping in quoted identifiers.
    const quoted = `nosql."${collection.replace(/"/g, '""')}"`;
    await sql.unsafe(
      `CREATE TABLE IF NOT EXISTS ${quoted} (
         _id text PRIMARY KEY,
         _creation_time double precision NOT NULL,
         doc jsonb NOT NULL DEFAULT '{}'::jsonb
       )`,
    );
  } finally {
    await sql.end({ timeout: 1 });
  }
}

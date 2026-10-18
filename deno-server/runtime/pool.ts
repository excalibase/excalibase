// Postgres connection pool — main-thread singleton.
//
// `postgres.js` (npm) maintains its own internal pool, so we just hold one
// instance keyed on EXCALIBASE_DB_URL and reuse it across all worker
// RPC calls. The pool is constructed lazily on first use so a runtime
// boot without DB credentials (e.g. legacy/Fetch-only deploys) doesn't
// fail to start.

import postgres from "npm:postgres@3.4.4";

// deno-lint-ignore no-explicit-any -- postgres.js types are heavy; the API
// surface we use is small enough that `any` is the pragmatic call.
export type Sql = any;

let cached: Sql | null = null;

/**
 * Return the lazy singleton postgres.js connection.
 * Throws if EXCALIBASE_DB_URL is not set — the caller must surface this as
 * a 500 to the function invocation, not crash the process.
 */
export function getPool(): Sql {
  if (cached) return cached;
  const url = Deno.env.get("EXCALIBASE_DB_URL");
  if (!url) {
    throw new Error("EXCALIBASE_DB_URL is not set — ctx.db is unavailable");
  }
  cached = postgres(url, poolOptions(url));
  return cached;
}

/**
 * Every option postgres.js would otherwise look up in PG* environment
 * variables. The image grants the runtime only its own variables, and an
 * unlisted read throws, so leaving one out fails every ctx.db call (EXC-625).
 * `ssl` is left to the URL's sslmode when it names one.
 */
export function poolOptions(url: string): Record<string, unknown> {
  const options: Record<string, unknown> = {
    onnotice: () => {}, // quieten Postgres NOTICEs in logs
    max: 10,
    idle_timeout: 30,
    connect_timeout: 10,
    // postgres.js's own default: a connection lives 30 to 60 minutes.
    max_lifetime: () => 60 * (30 + unitJitter() * 30),
    max_pipeline: 100,
    backoff: (retries: number) => (0.5 + unitJitter() / 2) * Math.min(3 ** retries / 100, 20),
    keep_alive: 60,
    prepare: true,
    debug: false,
    fetch_types: true,
    publications: "alltables",
    // ctx.db writes, so a read-only server is refused rather than used.
    target_session_attrs: "read-write",
  };
  if (!new URL(url).searchParams.has("sslmode")) options.ssl = false;
  return options;
}

/** A number in [0, 1) that spreads reconnects and recycling over time. */
function unitJitter(): number {
  return crypto.getRandomValues(new Uint32Array(1))[0] / 2 ** 32;
}

/** Close the cached pool. Called from server shutdown / test teardown. */
export async function closePool(): Promise<void> {
  if (cached) {
    const sql = cached;
    cached = null;
    await sql.end({ timeout: 5 });
  }
}

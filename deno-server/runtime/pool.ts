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
  cached = postgres(url, {
    onnotice: () => {}, // quieten Postgres NOTICEs in logs
    max: 10,
    idle_timeout: 30,
    connect_timeout: 10,
  });
  return cached;
}

/** Close the cached pool. Called from server shutdown / test teardown. */
export async function closePool(): Promise<void> {
  if (cached) {
    const sql = cached;
    cached = null;
    await sql.end({ timeout: 5 });
  }
}

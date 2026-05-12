// JSON Schema validator — ajv-backed, per-collection schema registry.
//
// Ports `JsonSchemaValidator.java`: pulls each collection's schema once from
// the database (lazy, cached) and reuses the compiled validator for every
// write. Errors are surfaced as `{path, message}` issues so the worker can
// translate them into a 400 `{error: "validation", issues: [...]}` response
// matching the Java REST API.
//
// Schema source: a `nosql.collection_schema(collection text, schema jsonb)`
// table when present. If the table does not exist, validation is a no-op —
// this matches the Java behaviour where collections without a registered
// schema accept arbitrary documents.

// ajv@8 ships its 2020-draft entry as an ES module with a CJS-style default.
// Deno reports the namespace as not-constructable; we re-cast via the
// Ajv2020 named binding which it does expose as a class type.
import { Ajv2020 } from "npm:ajv@8.17.1/dist/2020.js";
import type { ErrorObject, ValidateFunction } from "npm:ajv@8.17.1";
import type { Sql } from "./pool.ts";

export interface ValidationIssue {
  readonly path: string;
  readonly message: string;
}

interface CachedValidator {
  /** null means: the schema was looked up and confirmed absent. */
  readonly fn: ValidateFunction | null;
}

const ajv = new Ajv2020({ allErrors: true, strict: false });

// Per-request cache. The collection_schema table is small but we still want
// to avoid hitting it for every write inside the same handler invocation.
export interface ValidatorCache {
  readonly compiled: Map<string, CachedValidator>;
}

export function newCache(): ValidatorCache {
  return { compiled: new Map() };
}

async function loadSchemaFromDb(sql: Sql, collection: string): Promise<unknown | null> {
  // Phase 8.5 caveat: when this runs inside a shared mutation txn, a
  // raw error from the SELECT (e.g. "relation does not exist") aborts
  // the whole transaction — any subsequent statement fails with
  // "current transaction is aborted". To stay safe we pre-flight via
  // information_schema (which always exists) so the path is single
  // round-trip when the table is present and never poisons the txn
  // when it isn't. The pg_catalog read is itself a regular SQL query
  // that crosses the parameter boundary.
  try {
    const presence = await sql`
      SELECT 1 FROM information_schema.tables
       WHERE table_schema = 'nosql' AND table_name = 'collection_schema'
       LIMIT 1
    `;
    if (presence.length === 0) return null;
    const rows = await sql`
      SELECT schema FROM nosql.collection_schema WHERE collection = ${collection} LIMIT 1
    `;
    if (rows.length === 0) return null;
    return rows[0].schema;
  } catch (_e) {
    return null;
  }
}

async function getValidator(
  sql: Sql,
  cache: ValidatorCache,
  collection: string,
): Promise<ValidateFunction | null> {
  const cached = cache.compiled.get(collection);
  if (cached) return cached.fn;
  const schema = await loadSchemaFromDb(sql, collection);
  if (!schema) {
    cache.compiled.set(collection, { fn: null });
    return null;
  }
  // ajv.compile may throw on a malformed schema; surface it as a no-op so a
  // broken declaration doesn't take the function down.
  let fn: ValidateFunction | null = null;
  try {
    fn = ajv.compile(schema as object);
  } catch (_e) {
    fn = null;
  }
  cache.compiled.set(collection, { fn });
  return fn;
}

function toIssues(errors: ErrorObject[] | null | undefined): ValidationIssue[] {
  if (!errors) return [];
  return errors.map((e) => ({
    path: e.instancePath || "/",
    message: e.message ?? "validation failed",
  }));
}

/**
 * Validate a single document. Returns an empty array when valid OR when
 * no schema is registered for the collection.
 */
export async function validateDoc(
  sql: Sql,
  cache: ValidatorCache,
  collection: string,
  doc: Readonly<Record<string, unknown>>,
): Promise<ValidationIssue[]> {
  const fn = await getValidator(sql, cache, collection);
  if (!fn) return [];
  const ok = fn(doc);
  if (ok) return [];
  return toIssues(fn.errors);
}

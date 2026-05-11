// Schema-config visibility helper for the worker side.
//
// Phase 5a's Go bundler captures `globalThis.__excalibase_schema` (set by
// `@excalibase/server::defineSchema`) at build time and persists it to
// `Function.SchemaJSON`. Phase 5b's deploy preamble re-injects that payload
// into the worker bundle as
//   `globalThis.__excalibase_function_metadata = { schemaJson: <json> }`
// so this module can read it without any RPC.
//
// When the user's bundle did not call `defineSchema`, the slot is absent
// — `getSchema()` returns null and callers fall back to permissive mode
// (any collection name OK; search/vector throw the underlying Postgres
// 42703 instead of a friendly preflight error).
//
// IMPORTANT: this module runs INSIDE the Deno worker, so it has no DB
// access. The schema is read once at module-import time and cached for the
// worker's lifetime. Redeploys discard the worker and start a new one, so
// the cache stays consistent with the deployed bundle.

export interface IndexDef {
  readonly name: string;
  readonly fields: readonly string[];
}

export interface SearchIndexDef {
  readonly name: string;
  readonly searchField: string;
  readonly filterFields: readonly string[];
}

export interface VectorIndexDef {
  readonly name: string;
  readonly vectorField: string;
  readonly dimensions: number;
  readonly filterFields: readonly string[];
}

export interface TableDef {
  // Validator JSON Schema is preserved verbatim for the Phase 6 query
  // builder; this module doesn't introspect it.
  readonly validator: Record<string, unknown>;
  readonly indexes: readonly IndexDef[];
  readonly searchIndexes: readonly SearchIndexDef[];
  readonly vectorIndexes: readonly VectorIndexDef[];
}

export interface Schema {
  readonly tables: Readonly<Record<string, TableDef>>;
}

interface FunctionMetadata {
  readonly schemaJson?: unknown;
}

interface GlobalSlot {
  __excalibase_function_metadata?: FunctionMetadata;
}

/**
 * Read the bundler-emitted schema from the worker preamble. Returns null
 * when the bundle had no `defineSchema` call (permissive mode).
 *
 * The result is intentionally NOT cached at module scope — tests stub the
 * global between cases, and the worker only imports this module once
 * anyway so the lookup is cheap.
 */
export function getSchema(): Schema | null {
  const g = globalThis as unknown as GlobalSlot;
  const meta = g.__excalibase_function_metadata;
  if (!meta || meta.schemaJson === undefined || meta.schemaJson === null) {
    return null;
  }
  const raw = meta.schemaJson;
  if (typeof raw !== "object") return null;
  const tables = (raw as { tables?: unknown }).tables;
  if (!tables || typeof tables !== "object") return null;
  return { tables: tables as Record<string, TableDef> };
}

/**
 * Look up one table's metadata by collection name. Returns `undefined`
 * when the schema is present but the table isn't declared, and also when
 * no schema is set at all. Callers that need to distinguish "no schema"
 * from "table missing" should call `getSchema()` first.
 */
export function getTableDef(name: string): TableDef | undefined {
  const schema = getSchema();
  if (!schema) return undefined;
  return schema.tables[name];
}

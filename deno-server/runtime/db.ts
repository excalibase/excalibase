// ctx.db handler — main-thread side of the worker RPC.
//
// The worker holds a thin facade (built in server.ts via buildWorkerCode)
// that posts `{type: "db", op, ...}` messages. This module owns the actual
// SQL composition and execution, using postgres.js tagged templates so
// every user-supplied value crosses a parameter boundary — never string
// interpolation. The collection name is the only piece that ever lands in
// the SQL text, and it's regex-validated against `[a-zA-Z_]\w{0,62}` so
// quote/identifier injection is impossible.
//
// The on-disk shape mirrors what `DocumentExecutionService` returns: rows
// are `{ id, createdAt, updatedAt, ...data }`. Mutations also include row
// counts (`matched`/`modified`/`deleted`) so the worker can return
// `UpdateResult` / `DeleteResult` shapes from @excalibase/server.

import type { Sql } from "./pool.ts";
import { newCache, validateDoc, type ValidatorCache } from "./validator.ts";

const COLLECTION_NAME_RE = /^[a-zA-Z_]\w{0,62}$/;

function safeCollection(name: string): string {
  if (typeof name !== "string" || !COLLECTION_NAME_RE.test(name)) {
    throw new Error(`Invalid collection name: ${String(name).slice(0, 64)}`);
  }
  return name;
}

function safeIdent(name: string, kind: string): string {
  if (typeof name !== "string" || !COLLECTION_NAME_RE.test(name)) {
    throw new Error(`Invalid ${kind}: must match [a-zA-Z_]\\w{0,62}`);
  }
  return name;
}

export interface DbOp {
  readonly op:
    | "insert"
    | "insertMany"
    | "find"
    | "findOne"
    | "getById"
    | "update"
    | "delete"
    | "count";
  readonly collection: string;
  readonly doc?: Readonly<Record<string, unknown>>;
  readonly docs?: ReadonlyArray<Readonly<Record<string, unknown>>>;
  readonly id?: string;
  readonly filter?: Readonly<Record<string, unknown>>;
  readonly patch?: Readonly<Record<string, unknown>>;
  readonly options?: {
    readonly limit?: number;
    readonly offset?: number;
    readonly sort?: Readonly<Record<string, 1 | -1>>;
  };
}

export interface DbResultOk {
  readonly ok: true;
  readonly data: unknown;
}

export interface DbResultErr {
  readonly ok: false;
  readonly error: string;
  readonly issues?: ReadonlyArray<{ readonly path: string; readonly message: string }>;
}

export type DbResult = DbResultOk | DbResultErr;

function rowToDoc(row: { id: string; data: unknown; created_at: Date | string; updated_at: Date | string }): Record<string, unknown> {
  // postgres.js parses JSONB to a plain JS value by default when binding
  // through tagged templates. We still parse defensively in case a future
  // code path returns a string.
  let data: Record<string, unknown> = {};
  if (typeof row.data === "string") {
    try { data = JSON.parse(row.data); } catch (_) { data = {}; }
  } else if (row.data && typeof row.data === "object") {
    data = row.data as Record<string, unknown>;
  }
  const createdAt = row.created_at instanceof Date
    ? row.created_at.toISOString()
    : String(row.created_at);
  const updatedAt = row.updated_at instanceof Date
    ? row.updated_at.toISOString()
    : String(row.updated_at);
  return { ...data, id: row.id, createdAt, updatedAt };
}

// Build a single condition fragment for one (field, op, value) triple, as a
// postgres.js fragment. The fragment uses the right cast for the value type
// (numeric / boolean / text). The `field` is regex-validated by safeIdent;
// the `value` always crosses a parameter boundary so it can't inject SQL.
function buildCondition(
  sql: Sql,
  field: string,
  sqlOp: string,
  value: unknown,
): unknown /* postgres.Fragment */ {
  const key = safeIdent(field, "filter field");
  const colText = sql.unsafe(`(data->>'${key}')`);
  const colNum = sql.unsafe(`((data->>'${key}')::numeric)`);
  const colBool = sql.unsafe(`((data->>'${key}')::boolean)`);
  const opFrag = sql.unsafe(sqlOp);
  if (typeof value === "number") {
    return sql`${colNum} ${opFrag} ${value}`;
  }
  if (typeof value === "boolean") {
    return sql`${colBool} ${opFrag} ${value}`;
  }
  return sql`${colText} ${opFrag} ${String(value)}`;
}

const OPERATORS: Record<string, string> = {
  $eq: "=",
  $ne: "!=",
  $gt: ">",
  $gte: ">=",
  $lt: "<",
  $lte: "<=",
};

/**
 * Compile a Mongo-style filter into an array of postgres.js fragments. The
 * caller joins them with `AND`. Returns `null` to signal "no filter applied".
 */
function compileWhere(
  sql: Sql,
  filter: Readonly<Record<string, unknown>> | undefined,
): unknown[] | null {
  if (!filter || Object.keys(filter).length === 0) {
    return null;
  }
  const out: unknown[] = [];
  for (const [field, raw] of Object.entries(filter)) {
    if (raw !== null && typeof raw === "object" && !Array.isArray(raw)) {
      const opMap = raw as Record<string, unknown>;
      for (const [opName, opVal] of Object.entries(opMap)) {
        if (opName === "$in") {
          if (!Array.isArray(opVal) || opVal.length === 0) {
            throw new Error("$in requires a non-empty array");
          }
          const key = safeIdent(field, "filter field");
          const vals = opVal.map((v) => String(v));
          out.push(sql`(data->>${sql.unsafe(`'${key}'`)}) IN ${sql(vals)}`);
        } else if (Object.prototype.hasOwnProperty.call(OPERATORS, opName)) {
          out.push(buildCondition(sql, field, OPERATORS[opName], opVal));
        } else {
          throw new Error(`Unsupported operator: ${opName}`);
        }
      }
    } else {
      out.push(buildCondition(sql, field, "=", raw));
    }
  }
  return out;
}

function compileOrderBy(
  sql: Sql,
  sort: Readonly<Record<string, 1 | -1>> | undefined,
): unknown | null {
  if (!sort || Object.keys(sort).length === 0) return null;
  const parts = Object.entries(sort).map(([field, dir]) => {
    const key = safeIdent(field, "sort field");
    return `(data->>'${key}') ${dir === -1 ? "DESC" : "ASC"}`;
  });
  return sql.unsafe(`ORDER BY ${parts.join(", ")}`);
}

function clampLimit(limit: number | undefined): number {
  if (typeof limit !== "number" || !Number.isFinite(limit) || limit <= 0) return 30;
  if (limit > 1000) return 1000;
  return Math.floor(limit);
}

function clampOffset(offset: number | undefined): number {
  if (typeof offset !== "number" || !Number.isFinite(offset) || offset < 0) return 0;
  return Math.floor(offset);
}

/**
 * Join an array of fragments with " AND " — equivalent to
 * `[f1, f2, f3] -> f1 AND f2 AND f3` as a postgres.js fragment.
 */
function joinAnd(sql: Sql, fragments: unknown[]): unknown {
  if (fragments.length === 1) return fragments[0];
  // postgres.js doesn't expose a public Fragment join; we build via reduce.
  let result = fragments[0];
  for (let i = 1; i < fragments.length; i++) {
    result = sql`${result} AND ${fragments[i]}`;
  }
  return result;
}

/**
 * Execute one RPC op from a worker. Each op is one Postgres round-trip
 * (or two for update — patch + select-back happens via RETURNING).
 */
export async function executeDbOp(
  sql: Sql,
  cache: ValidatorCache,
  op: DbOp,
): Promise<DbResult> {
  try {
    const collection = safeCollection(op.collection);
    // The schema-qualified table identifier. Collection name is regex-
    // validated above so this string is safe to embed.
    const table = sql.unsafe(`nosql."${collection}"`);
    switch (op.op) {
      case "insert": {
        if (!op.doc || typeof op.doc !== "object") {
          return { ok: false, error: "doc must be an object" };
        }
        const issues = await validateDoc(sql, cache, collection, op.doc);
        if (issues.length > 0) {
          return { ok: false, error: "validation", issues };
        }
        const rows = await sql`
          INSERT INTO ${table} (data)
          VALUES (${sql.json(op.doc)})
          RETURNING id, data, created_at, updated_at
        `;
        return { ok: true, data: rowToDoc(rows[0]) };
      }
      case "insertMany": {
        if (!Array.isArray(op.docs) || op.docs.length === 0) {
          return { ok: false, error: "docs must be a non-empty array" };
        }
        for (const d of op.docs) {
          if (!d || typeof d !== "object") {
            return { ok: false, error: "docs entries must be objects" };
          }
          const issues = await validateDoc(sql, cache, collection, d);
          if (issues.length > 0) {
            return { ok: false, error: "validation", issues };
          }
        }
        // postgres.js multi-row insert: pass `sql(arrayOfRows, ...columns)`
        // and it expands to `("data1"), ("data2"), ...` with each value
        // parameterised. We wrap each doc through sql.json() ahead of time.
        const rowsPayload = op.docs.map((d) => ({ data: sql.json(d) }));
        const rows = await sql`
          INSERT INTO ${table} ${sql(rowsPayload, "data")}
          RETURNING id, data, created_at, updated_at
        `;
        return { ok: true, data: rows.map(rowToDoc) };
      }
      case "getById": {
        if (typeof op.id !== "string" || op.id.length === 0) {
          return { ok: false, error: "id required" };
        }
        const rows = await sql`
          SELECT id, data, created_at, updated_at
          FROM ${table}
          WHERE id = ${op.id}::uuid
          LIMIT 1
        `;
        return { ok: true, data: rows.length === 0 ? null : rowToDoc(rows[0]) };
      }
      case "find": {
        const conds = compileWhere(sql, op.filter);
        const order = compileOrderBy(sql, op.options?.sort);
        const limit = clampLimit(op.options?.limit);
        const offset = clampOffset(op.options?.offset);
        const rows = conds === null
          ? (order
              ? await sql`
                  SELECT id, data, created_at, updated_at FROM ${table}
                  ${order} LIMIT ${limit} OFFSET ${offset}
                `
              : await sql`
                  SELECT id, data, created_at, updated_at FROM ${table}
                  LIMIT ${limit} OFFSET ${offset}
                `)
          : (order
              ? await sql`
                  SELECT id, data, created_at, updated_at FROM ${table}
                  WHERE ${joinAnd(sql, conds)}
                  ${order} LIMIT ${limit} OFFSET ${offset}
                `
              : await sql`
                  SELECT id, data, created_at, updated_at FROM ${table}
                  WHERE ${joinAnd(sql, conds)}
                  LIMIT ${limit} OFFSET ${offset}
                `);
        return { ok: true, data: rows.map(rowToDoc) };
      }
      case "findOne": {
        const conds = compileWhere(sql, op.filter);
        const rows = conds === null
          ? await sql`SELECT id, data, created_at, updated_at FROM ${table} LIMIT 1`
          : await sql`SELECT id, data, created_at, updated_at FROM ${table} WHERE ${joinAnd(sql, conds)} LIMIT 1`;
        return { ok: true, data: rows.length === 0 ? null : rowToDoc(rows[0]) };
      }
      case "update": {
        const patch = op.patch && typeof op.patch === "object" && "$set" in op.patch
          ? (op.patch as { $set?: Record<string, unknown> }).$set ?? {}
          : op.patch ?? {};
        if (!patch || typeof patch !== "object") {
          return { ok: false, error: "patch must be an object or {$set: object}" };
        }
        const conds = compileWhere(sql, op.filter);
        if (conds === null) {
          return { ok: false, error: "update requires a filter" };
        }
        const issues = await validateDoc(sql, cache, collection, patch);
        if (issues.length > 0) {
          return { ok: false, error: "validation", issues };
        }
        const rows = await sql`
          UPDATE ${table}
          SET data = data || ${sql.json(patch)}, updated_at = clock_timestamp()
          WHERE ${joinAnd(sql, conds)}
          RETURNING id, data, created_at, updated_at
        `;
        const docs = rows.map(rowToDoc);
        return {
          ok: true,
          data: { matched: rows.length, modified: rows.length, docs },
        };
      }
      case "delete": {
        const conds = compileWhere(sql, op.filter);
        if (conds === null) {
          return { ok: false, error: "delete requires a filter" };
        }
        const rows = await sql`
          DELETE FROM ${table}
          WHERE ${joinAnd(sql, conds)}
          RETURNING id, data, created_at, updated_at
        `;
        const docs = rows.map(rowToDoc);
        return { ok: true, data: { deleted: rows.length, docs } };
      }
      case "count": {
        const conds = compileWhere(sql, op.filter);
        const rows = conds === null
          ? await sql`SELECT COUNT(*)::bigint AS c FROM ${table}`
          : await sql`SELECT COUNT(*)::bigint AS c FROM ${table} WHERE ${joinAnd(sql, conds)}`;
        return { ok: true, data: Number(rows[0].c) };
      }
      default: {
        return { ok: false, error: `unknown op: ${(op as { op: string }).op}` };
      }
    }
  } catch (e) {
    return { ok: false, error: String((e instanceof Error ? e.message : e) ?? "db error") };
  }
}

export { newCache };

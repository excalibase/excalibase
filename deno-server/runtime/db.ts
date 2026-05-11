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
// Phase 5b on-disk shape: every collection table has the Convex-shape
// columns `_id text PRIMARY KEY`, `_creation_time double precision NOT
// NULL`, and `doc jsonb NOT NULL`. Reads project all three; rows are
// mapped to `{ _id, _creationTime, ...doc }` so user code sees the Convex
// `Doc<T>` shape. Inserts generate `_id` via the runtime/ids generator
// (30-char base32) and `_creation_time` via `Date.now()` (millisecond
// epoch as a float). The legacy `(id uuid, data jsonb, created_at,
// updated_at)` shape is no longer addressed anywhere in this module — it
// was last addressed in the pre-5b runtime.

import type { Sql } from "./pool.ts";
import { decodeCursor, encodeCursor } from "./cursor.ts";
import { newId } from "./ids.ts";
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
    | "count"
    | "search"
    | "vectorSearch";
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
    readonly cursor?: string | null;
    readonly cursorMode?: boolean;
    readonly topK?: number;
  };
  /** Free-text query for the `search` op. */
  readonly query?: string;
  /** Numeric vector for the `vectorSearch` op. */
  readonly embedding?: ReadonlyArray<number>;
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

interface DbRow {
  _id: string;
  _creation_time: number | string;
  doc: unknown;
}

function rowToDoc(row: DbRow): Record<string, unknown> {
  // postgres.js parses JSONB to a plain JS value by default when binding
  // through tagged templates. We still parse defensively in case a future
  // code path returns a string.
  let doc: Record<string, unknown> = {};
  if (typeof row.doc === "string") {
    try { doc = JSON.parse(row.doc); } catch (_) { doc = {}; }
  } else if (row.doc && typeof row.doc === "object") {
    doc = row.doc as Record<string, unknown>;
  }
  // `_creation_time` is stored as `double precision` so postgres.js binds
  // it as a JS number; defensively coerce strings (some pooler configs
  // emit numerics as text).
  const creationTime = typeof row._creation_time === "number"
    ? row._creation_time
    : Number(row._creation_time);
  // System fields are placed AFTER the doc spread so a malicious doc
  // payload (e.g. `{ _id: "spoofed" }`) cannot override the canonical
  // values pulled from the table.
  return { ...doc, _id: row._id, _creationTime: creationTime };
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
  const colText = sql.unsafe(`(doc->>'${key}')`);
  const colNum = sql.unsafe(`((doc->>'${key}')::numeric)`);
  const colBool = sql.unsafe(`((doc->>'${key}')::boolean)`);
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
          out.push(sql`(doc->>${sql.unsafe(`'${key}'`)}) IN ${sql(vals)}`);
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
    return `(doc->>'${key}') ${dir === -1 ? "DESC" : "ASC"}`;
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

// Clamp the vector-search neighbour count. We share clampLimit's upper
// bound (1000) so a runaway client can't ask for 1M nearest neighbours
// and pull the entire collection.
function clampTopK(topK: number | undefined): number {
  if (typeof topK !== "number" || !Number.isFinite(topK) || topK <= 0) return 30;
  if (topK > 1000) return 1000;
  return Math.floor(topK);
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
        const id = newId();
        const creationTime = Date.now();
        const rows = await sql`
          INSERT INTO ${table} (_id, _creation_time, doc)
          VALUES (${id}, ${creationTime}, ${sql.json(op.doc)})
          RETURNING _id, _creation_time, doc
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
        // and it expands to `("id1","ct1","doc1"), ...` with each value
        // parameterised. We pre-compute `_id` and `_creation_time` per
        // row so the runtime fully owns system-field generation.
        const rowsPayload = op.docs.map((d) => ({
          _id: newId(),
          _creation_time: Date.now(),
          doc: sql.json(d),
        }));
        const rows = await sql`
          INSERT INTO ${table} ${sql(rowsPayload, "_id", "_creation_time", "doc")}
          RETURNING _id, _creation_time, doc
        `;
        return { ok: true, data: rows.map(rowToDoc) };
      }
      case "getById": {
        if (typeof op.id !== "string" || op.id.length === 0) {
          return { ok: false, error: "id required" };
        }
        const rows = await sql`
          SELECT _id, _creation_time, doc
          FROM ${table}
          WHERE _id = ${op.id}
          LIMIT 1
        `;
        return { ok: true, data: rows.length === 0 ? null : rowToDoc(rows[0]) };
      }
      case "find": {
        const conds = compileWhere(sql, op.filter);
        const limit = clampLimit(op.options?.limit);

        // Cursor-mode path mirrors `DocumentQueryCompiler.compileFind` on
        // the Java side: keyset over `(_creation_time DESC, _id DESC)`
        // with an optional `(_creation_time, _id) < (cursorTs, cursorId)`
        // cutoff. We fetch one extra row so we can decide whether
        // `nextCursor` should be null (no more rows) or the encoded
        // keyset of the last visible row (more rows available).
        if (op.options?.cursorMode === true) {
          const cursorToken = op.options?.cursor ?? null;
          const fetchLimit = limit + 1;
          let rows: Array<DbRow>;
          if (cursorToken && typeof cursorToken === "string") {
            const cur = decodeCursor(cursorToken);
            const baseConds = conds ?? [];
            const keysetFrag = sql`(_creation_time, _id) < (${cur.creationTime}, ${cur.id})`;
            const combined = baseConds.length === 0
              ? keysetFrag
              : joinAnd(sql, [...baseConds, keysetFrag]);
            rows = await sql`
              SELECT _id, _creation_time, doc
              FROM ${table}
              WHERE ${combined}
              ORDER BY _creation_time DESC, _id DESC
              LIMIT ${fetchLimit}
            `;
          } else if (conds === null) {
            rows = await sql`
              SELECT _id, _creation_time, doc
              FROM ${table}
              ORDER BY _creation_time DESC, _id DESC
              LIMIT ${fetchLimit}
            `;
          } else {
            rows = await sql`
              SELECT _id, _creation_time, doc
              FROM ${table}
              WHERE ${joinAnd(sql, conds)}
              ORDER BY _creation_time DESC, _id DESC
              LIMIT ${fetchLimit}
            `;
          }
          const hasMore = rows.length > limit;
          const visible = hasMore ? rows.slice(0, limit) : rows;
          let nextCursor: string | null = null;
          if (hasMore && visible.length > 0) {
            const last = visible[visible.length - 1];
            const creationTime = typeof last._creation_time === "number"
              ? last._creation_time
              : Number(last._creation_time);
            nextCursor = encodeCursor({ creationTime, id: last._id });
          }
          return { ok: true, data: { docs: visible.map(rowToDoc), nextCursor } };
        }

        // Non-cursor path — classic offset/limit with optional sort. No
        // change in shape: callers still get a plain `Doc[]`.
        const order = compileOrderBy(sql, op.options?.sort);
        const offset = clampOffset(op.options?.offset);
        const rows = conds === null
          ? (order
              ? await sql`
                  SELECT _id, _creation_time, doc FROM ${table}
                  ${order} LIMIT ${limit} OFFSET ${offset}
                `
              : await sql`
                  SELECT _id, _creation_time, doc FROM ${table}
                  LIMIT ${limit} OFFSET ${offset}
                `)
          : (order
              ? await sql`
                  SELECT _id, _creation_time, doc FROM ${table}
                  WHERE ${joinAnd(sql, conds)}
                  ${order} LIMIT ${limit} OFFSET ${offset}
                `
              : await sql`
                  SELECT _id, _creation_time, doc FROM ${table}
                  WHERE ${joinAnd(sql, conds)}
                  LIMIT ${limit} OFFSET ${offset}
                `);
        return { ok: true, data: rows.map(rowToDoc) };
      }
      case "findOne": {
        const conds = compileWhere(sql, op.filter);
        const rows = conds === null
          ? await sql`SELECT _id, _creation_time, doc FROM ${table} LIMIT 1`
          : await sql`SELECT _id, _creation_time, doc FROM ${table} WHERE ${joinAnd(sql, conds)} LIMIT 1`;
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
        // `_creation_time` stays immutable: only `doc` is patched. We do
        // NOT touch `_id` either — Convex semantics say the primary key
        // is permanent.
        const rows = await sql`
          UPDATE ${table}
          SET doc = doc || ${sql.json(patch)}
          WHERE ${joinAnd(sql, conds)}
          RETURNING _id, _creation_time, doc
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
          RETURNING _id, _creation_time, doc
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
      case "search": {
        // Full-text search over the generated `search_text` tsvector
        // column. Phase 5b parity: projection now selects `_id`,
        // `_creation_time`, `doc` (Convex shape).
        const queryText = op.query;
        if (typeof queryText !== "string" || queryText.length === 0) {
          return { ok: false, error: "search query must be a non-empty string" };
        }
        const limit = clampLimit(op.options?.limit);
        const rows = await sql`
          SELECT _id, _creation_time, doc
          FROM ${table}
          WHERE search_text @@ websearch_to_tsquery(${queryText})
          ORDER BY ts_rank(search_text, websearch_to_tsquery(${queryText})) DESC
          LIMIT ${limit}
        `;
        return { ok: true, data: rows.map(rowToDoc) };
      }
      case "vectorSearch": {
        // k-NN over `embedding vector(N)` via the cosine-distance
        // operator `<=>`. Projection updated to the Convex shape.
        const embedding = op.embedding;
        if (!Array.isArray(embedding) || embedding.length === 0) {
          return { ok: false, error: "embedding must be a non-empty numeric array" };
        }
        for (const v of embedding) {
          if (typeof v !== "number" || !Number.isFinite(v)) {
            return { ok: false, error: "embedding values must be finite numbers" };
          }
        }
        const topK = clampTopK(op.options?.topK);
        const vecLiteral = `[${embedding.join(",")}]`;
        const conds = compileWhere(sql, op.filter);
        const rows = conds === null
          ? await sql`
              SELECT _id, _creation_time, doc
              FROM ${table}
              ORDER BY embedding <=> ${vecLiteral}::vector
              LIMIT ${topK}
            `
          : await sql`
              SELECT _id, _creation_time, doc
              FROM ${table}
              WHERE ${joinAnd(sql, conds)}
              ORDER BY embedding <=> ${vecLiteral}::vector
              LIMIT ${topK}
            `;
        return { ok: true, data: rows.map(rowToDoc) };
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

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

// ---------------------------------------------------------------------------
// Convex-shape query builder — plan compiler.
//
// Phase 6 introduces `ctx.db.query(name)`. The worker shim accumulates the
// chained calls into a `QueryPlan` (defined here, shared verbatim with
// @excalibase/server's `QueryPlan`) and routes it through `executeDbOp`
// with `op: "query"`. The main thread executes the plan via
// `executeQueryPlan` and returns the right shape per terminal.
//
// API contract is documented in @excalibase/server@0.5.0 — these types
// MUST stay shape-compatible with that package, since the worker bundles
// the lib's runtime callback that hands a plan over RPC.
// ---------------------------------------------------------------------------

export type FilterExpr =
  | { readonly kind: "field"; readonly name: string }
  | { readonly kind: "eq" | "neq" | "gt" | "gte" | "lt" | "lte"; readonly left: FilterExpr; readonly right: unknown }
  | { readonly kind: "and" | "or"; readonly args: ReadonlyArray<FilterExpr> }
  | { readonly kind: "not"; readonly arg: FilterExpr };

export interface IndexBound {
  readonly field: string;
  readonly op: "eq" | "gt" | "gte" | "lt" | "lte";
  readonly value: unknown;
}

export interface IndexHint {
  readonly name: string;
  readonly bounds: ReadonlyArray<IndexBound>;
}

export interface SearchIndexHint {
  readonly name: string;
  readonly field: string;
  readonly query: string;
  readonly filters: ReadonlyArray<{ readonly field: string; readonly value: unknown }>;
}

export interface VectorIndexHint {
  readonly name: string;
  readonly embedding: ReadonlyArray<number>;
  readonly k: number;
  readonly filters: ReadonlyArray<{ readonly field: string; readonly value: unknown }>;
}

export interface QueryPlan {
  readonly collection: string;
  readonly index?: IndexHint;
  readonly search?: SearchIndexHint;
  readonly vector?: VectorIndexHint;
  readonly filter?: FilterExpr;
  readonly order?: "asc" | "desc";
  /**
   * Phase 6.5 — name of the index chosen by `selectIndex()` when the
   * worker performed automatic selection. Empty/undefined means the
   * user passed an explicit `.withIndex(...)` hint, or no index was
   * selected at all. The main thread reads this on the `query` op to
   * label the `excalibase_query_index_selected_total` counter.
   */
  readonly autoSelectedIndex?: string;
}

export type QueryTerminal = "first" | "unique" | "collect" | "take" | "paginate";

export interface PaginationOptions {
  readonly cursor: string | null;
  readonly numItems: number;
}

export interface PaginationResult {
  readonly page: ReadonlyArray<Record<string, unknown>>;
  readonly isDone: boolean;
  readonly continueCursor: string;
}

/**
 * Default cap on `collect()` result size. Mirrors Convex's 16k cap. Override
 * via the `EXCALIBASE_QUERY_MAX_RESULTS` env var; the value is read on each
 * call so tests can flip it without restarting the runtime.
 */
const DEFAULT_QUERY_MAX_RESULTS = 16384;

function queryMaxResults(): number {
  try {
    const raw = Deno.env.get("EXCALIBASE_QUERY_MAX_RESULTS");
    if (raw === undefined || raw === null || raw === "") return DEFAULT_QUERY_MAX_RESULTS;
    const n = Number(raw);
    if (!Number.isFinite(n) || n <= 0) return DEFAULT_QUERY_MAX_RESULTS;
    return Math.floor(n);
  } catch (_) {
    return DEFAULT_QUERY_MAX_RESULTS;
  }
}

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
    | "vectorSearch"
    | "query";
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
  /** Compiled `QueryPlan` for the `query` op (Phase 6 chainable builder). */
  readonly plan?: QueryPlan;
  /** Terminal for the `query` op (one of `first`/`unique`/`collect`/`take`/`paginate`). */
  readonly terminal?: QueryTerminal;
  /** Extra payload for the `query` op (e.g. `take(n)`'s `n`, `paginate(opts)`'s opts). */
  readonly extra?: unknown;
}

export interface DbResultOk {
  readonly ok: true;
  readonly data: unknown;
}

export interface DbResultErr {
  readonly ok: false;
  readonly error: string;
  readonly issues?: ReadonlyArray<{ readonly path: string; readonly message: string }>;
  /**
   * Phase 9a: SQLSTATE attached when the underlying error originated from
   * postgres.js (`PostgresError.code`). The dispatcher consults this to
   * detect retryable conflicts (40001 = serialization_failure, 40P01 =
   * deadlock_detected) without needing to re-throw and lose typing.
   */
  readonly sqlState?: string;
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

// ---------------------------------------------------------------------------
// Query plan → SQL compiler.
//
// The compiler walks the plan tree and emits postgres.js fragments. Every
// user-supplied value crosses a parameter boundary; identifiers (field
// names, index field names, collection name) are regex-validated via
// `safeIdent` before they ever appear in SQL text. The Convex-shape
// projection is constant — `_id`, `_creation_time`, `doc` — so the
// terminal-specific bits are only the WHERE/ORDER/LIMIT shape.

function compileFilterExpr(sql: Sql, expr: FilterExpr): unknown /* fragment */ {
  switch (expr.kind) {
    case "eq":
    case "neq":
    case "gt":
    case "gte":
    case "lt":
    case "lte": {
      if (expr.left.kind !== "field") {
        throw new Error("filter comparison left must be a field reference");
      }
      const op = expr.kind === "eq" ? "="
        : expr.kind === "neq" ? "!="
        : expr.kind === "gt" ? ">"
        : expr.kind === "gte" ? ">="
        : expr.kind === "lt" ? "<"
        : "<=";
      return buildCondition(sql, expr.left.name, op, expr.right);
    }
    case "and":
    case "or": {
      if (!Array.isArray(expr.args) || expr.args.length === 0) {
        throw new Error(`filter ${expr.kind} requires non-empty args`);
      }
      const parts = expr.args.map((a) => compileFilterExpr(sql, a));
      let combined = parts[0];
      const joiner = expr.kind === "and" ? sql.unsafe(" AND ") : sql.unsafe(" OR ");
      for (let i = 1; i < parts.length; i++) {
        combined = sql`(${combined}${joiner}${parts[i]})`;
      }
      return combined;
    }
    case "not": {
      const inner = compileFilterExpr(sql, expr.arg);
      return sql`(NOT (${inner}))`;
    }
    case "field":
      throw new Error("filter 'field' is only valid as the left side of a comparison");
    default: {
      // exhaustive guard — narrow `never` is enough for the type checker.
      const k = (expr as { kind: string }).kind;
      throw new Error(`unsupported filter kind: ${k}`);
    }
  }
}

function compileIndexBounds(sql: Sql, hint: IndexHint): unknown {
  if (!Array.isArray(hint.bounds) || hint.bounds.length === 0) return null;
  const parts = hint.bounds.map((b) => {
    const op = b.op === "eq" ? "="
      : b.op === "gt" ? ">"
      : b.op === "gte" ? ">="
      : b.op === "lt" ? "<"
      : "<=";
    return buildCondition(sql, b.field, op, b.value);
  });
  let combined = parts[0];
  for (let i = 1; i < parts.length; i++) {
    combined = sql`${combined} AND ${parts[i]}`;
  }
  return combined;
}

// Run a SELECT-style query against the compiled plan and return the raw
// rows. Terminal methods compose on top of this.
async function runPlanSelect(
  sql: Sql,
  plan: QueryPlan,
  limit: number | null,
): Promise<Array<DbRow>> {
  const collection = safeCollection(plan.collection);
  const table = sql.unsafe(`nosql."${collection}"`);

  // Build WHERE fragments — collect from index bounds, filter, and any
  // search/vector filterFields.
  const wherePieces: unknown[] = [];
  if (plan.index) {
    const idxName = safeIdent(plan.index.name, "index name");
    void idxName; // identifier validation — Postgres planner picks the actual index.
    const idx = compileIndexBounds(sql, plan.index);
    if (idx !== null) wherePieces.push(idx);
  }
  if (plan.filter) {
    wherePieces.push(compileFilterExpr(sql, plan.filter));
  }
  if (plan.search) {
    // Validate every identifier we touch.
    safeIdent(plan.search.name, "search index name");
    const field = safeIdent(plan.search.field, "search field");
    const colName = sql.unsafe(`search_${field}`);
    wherePieces.push(sql`${colName} @@ websearch_to_tsquery(${plan.search.query})`);
    for (const f of plan.search.filters) {
      wherePieces.push(buildCondition(sql, f.field, "=", f.value));
    }
  }
  if (plan.vector) {
    safeIdent(plan.vector.name, "vector index name");
    for (const f of plan.vector.filters) {
      wherePieces.push(buildCondition(sql, f.field, "=", f.value));
    }
  }

  // ORDER BY:
  //   search:  ORDER BY ts_rank(...) DESC
  //   vector:  ORDER BY embedding <=> $vec::vector
  //   else:    ORDER BY _creation_time {ASC|DESC}, _id {ASC|DESC}
  let orderFrag: unknown;
  if (plan.search) {
    const field = safeIdent(plan.search.field, "search field");
    const colName = sql.unsafe(`search_${field}`);
    orderFrag = sql`ORDER BY ts_rank(${colName}, websearch_to_tsquery(${plan.search.query})) DESC`;
  } else if (plan.vector) {
    const vecLiteral = `[${plan.vector.embedding.join(",")}]`;
    orderFrag = sql`ORDER BY embedding <=> ${vecLiteral}::vector`;
  } else {
    const direction = (plan.order ?? "asc") === "desc" ? "DESC" : "ASC";
    orderFrag = sql.unsafe(`ORDER BY _creation_time ${direction}, _id ${direction}`);
  }

  // Combine WHERE fragments.
  let whereFrag: unknown | null = null;
  if (wherePieces.length > 0) {
    whereFrag = wherePieces[0];
    for (let i = 1; i < wherePieces.length; i++) {
      whereFrag = sql`${whereFrag} AND ${wherePieces[i]}`;
    }
  }

  // For vector terminals the k is the LIMIT; for everything else the
  // caller-supplied limit governs.
  let effectiveLimit: number | null = limit;
  if (plan.vector && (limit === null || limit > plan.vector.k)) {
    effectiveLimit = plan.vector.k;
  }
  const limitFrag = effectiveLimit === null ? sql.unsafe("") : sql`LIMIT ${effectiveLimit}`;

  if (whereFrag === null) {
    return await sql`
      SELECT _id, _creation_time, doc
      FROM ${table}
      ${orderFrag}
      ${limitFrag}
    `;
  }
  return await sql`
    SELECT _id, _creation_time, doc
    FROM ${table}
    WHERE ${whereFrag}
    ${orderFrag}
    ${limitFrag}
  `;
}

/**
 * Compile and execute a `QueryPlan` against the pool, picking the right
 * SQL shape for each terminal.
 *
 * Throws on:
 *   - invalid collection / field / index identifiers,
 *   - unique() with 0 or >1 matches,
 *   - collect() exceeding `EXCALIBASE_QUERY_MAX_RESULTS`,
 *   - unsupported filter kinds.
 *
 * Returns:
 *   - first:    `Record<string, unknown> | null`
 *   - unique:   `Record<string, unknown>`
 *   - collect:  `ReadonlyArray<Record<string, unknown>>`
 *   - take:     `ReadonlyArray<Record<string, unknown>>` (capped at `extra` as number)
 *   - paginate: `{ page, isDone, continueCursor }`
 */
export async function executeQueryPlan(
  sql: Sql,
  plan: QueryPlan,
  terminal: QueryTerminal,
  extra?: unknown,
): Promise<unknown> {
  switch (terminal) {
    case "first": {
      const rows = await runPlanSelect(sql, plan, 1);
      return rows.length === 0 ? null : rowToDoc(rows[0]);
    }
    case "unique": {
      const rows = await runPlanSelect(sql, plan, 2);
      if (rows.length === 0) throw new Error("expected unique, got 0 docs");
      if (rows.length > 1) throw new Error(`expected unique, got more than one doc`);
      return rowToDoc(rows[0]);
    }
    case "collect": {
      const cap = queryMaxResults();
      // Fetch one extra so we can detect "exceeded" deterministically.
      const rows = await runPlanSelect(sql, plan, cap + 1);
      if (rows.length > cap) {
        throw new Error(`collect() exceeded max results (${cap}); refine the filter or use paginate()`);
      }
      return rows.map(rowToDoc);
    }
    case "take": {
      const n = typeof extra === "number" && extra > 0 ? Math.floor(extra) : 30;
      const rows = await runPlanSelect(sql, plan, n);
      return rows.map(rowToDoc);
    }
    case "paginate": {
      const opts = (extra ?? {}) as Partial<PaginationOptions>;
      const numItems = typeof opts.numItems === "number" && opts.numItems > 0
        ? Math.floor(opts.numItems)
        : 30;
      const direction = plan.order ?? "asc";

      // Phase 14 — snapshot consistency watermark.
      //
      // The cursor blob carries a `snapshotTs` captured on the first page
      // of a pagination chain (cursor=null). Subsequent pages add
      // `_creation_time <= snapshotTs` to the WHERE clause so any row
      // inserted AFTER the snapshot is invisible to that pagination
      // session — the SKIP bug. We keep the keyset on
      // `(_creation_time, _id)` as before; the snapshot only prunes the
      // search space.
      //
      // Back-compat: pre-14 cursors decode with snapshotTs=undefined.
      // The first time we see one we capture a fresh snapshot (current
      // clock) — degraded first call only; every subsequent call inside
      // the same chain is snapshot-consistent.
      //
      // UPDATE-shifts that mutate `_creation_time` would still let a
      // row dup-appear, but `update` op preserves system columns by
      // construction (it patches `doc` only). Documenting the divergence
      // for explicit `_creation_time` writes lives in
      // `docs/functions-query.md`.
      let decodedCursor: ReturnType<typeof decodeCursor> | null = null;
      if (typeof opts.cursor === "string" && opts.cursor.length > 0) {
        decodedCursor = decodeCursor(opts.cursor);
      }
      const snapshotTs = decodedCursor?.snapshotTs ?? Date.now();

      // Cursor cutoff is `(_creation_time, _id) > (ct, id)` for asc
      // (`<` for desc). Compiled inline so we don't have to teach
      // `buildCondition` about system columns.
      const cursorFrag = decodedCursor === null
        ? null
        : direction === "desc"
          ? sql`(_creation_time, _id) < (${decodedCursor.creationTime}, ${decodedCursor.id})`
          : sql`(_creation_time, _id) > (${decodedCursor.creationTime}, ${decodedCursor.id})`;

      // Snapshot guard — applied on EVERY page (including the very first
      // one) so the snapshotTs we encode into `continueCursor` describes
      // the exact set of rows visible to this chain.
      const snapshotFrag = sql`_creation_time <= ${snapshotTs}`;

      // Build the SELECT ourselves so we can splice the keyset alongside
      // any user-supplied filter without abusing the FilterExpr type.
      const collection = safeCollection(plan.collection);
      const table = sql.unsafe(`nosql."${collection}"`);
      const wherePieces: unknown[] = [];
      if (plan.index) {
        safeIdent(plan.index.name, "index name");
        const idx = compileIndexBounds(sql, plan.index);
        if (idx !== null) wherePieces.push(idx);
      }
      if (plan.filter) wherePieces.push(compileFilterExpr(sql, plan.filter));
      wherePieces.push(snapshotFrag);
      if (cursorFrag !== null) wherePieces.push(cursorFrag);

      let whereFrag: unknown | null = wherePieces[0];
      for (let i = 1; i < wherePieces.length; i++) {
        whereFrag = sql`${whereFrag} AND ${wherePieces[i]}`;
      }
      const directionUpper = direction === "desc" ? "DESC" : "ASC";
      const orderFrag = sql.unsafe(`ORDER BY _creation_time ${directionUpper}, _id ${directionUpper}`);
      const limit = numItems + 1;
      const rows: Array<DbRow> = await sql`
        SELECT _id, _creation_time, doc
        FROM ${table}
        WHERE ${whereFrag}
        ${orderFrag}
        LIMIT ${limit}
      `;
      const hasMore = rows.length > numItems;
      const visible = hasMore ? rows.slice(0, numItems) : rows;
      const page = visible.map(rowToDoc);
      let continueCursor = "";
      if (hasMore && visible.length > 0) {
        const last = visible[visible.length - 1];
        const ct = typeof last._creation_time === "number"
          ? last._creation_time
          : Number(last._creation_time);
        continueCursor = encodeCursor({ creationTime: ct, id: last._id, snapshotTs });
      }
      return { page, isDone: !hasMore, continueCursor };
    }
    default: {
      const t = (terminal as string) ?? "";
      throw new Error(`unsupported terminal: ${t}`);
    }
  }
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
      case "query": {
        // Convex-shape chainable builder. The worker shim posts a compiled
        // `QueryPlan` (mirrors @excalibase/server's plan shape one-for-one)
        // plus the terminal name and any extra payload. We re-set the
        // plan's collection so `safeCollection` runs uniformly with all
        // other ops above — defence in depth against a worker that
        // forgets to set it.
        if (!op.plan || typeof op.plan !== "object") {
          return { ok: false, error: "query op requires a plan" };
        }
        if (op.plan.collection !== collection) {
          return { ok: false, error: "plan.collection must match op.collection" };
        }
        const terminal = op.terminal;
        if (terminal !== "first" && terminal !== "unique"
            && terminal !== "collect" && terminal !== "take" && terminal !== "paginate") {
          return { ok: false, error: `query op requires a valid terminal, got: ${String(terminal)}` };
        }
        const data = await executeQueryPlan(sql, op.plan, terminal, op.extra);
        return { ok: true, data };
      }
      default: {
        return { ok: false, error: `unknown op: ${(op as { op: string }).op}` };
      }
    }
  } catch (e) {
    // Phase 9a: propagate the SQLSTATE on postgres.js errors so the
    // outer dispatch can detect retryable conflicts (40001, 40P01) and
    // route through the retry loop. PostgresError carries `.code` as a
    // 5-char SQLSTATE string; non-PG errors fall through with no code.
    const sqlState = typeof (e as { code?: unknown })?.code === "string"
      ? ((e as { code: string }).code)
      : undefined;
    const message = String((e instanceof Error ? e.message : e) ?? "db error");
    return sqlState
      ? ({ ok: false, error: message, sqlState } as DbResultErr)
      : ({ ok: false, error: message } as DbResultErr);
  }
}

export { newCache };

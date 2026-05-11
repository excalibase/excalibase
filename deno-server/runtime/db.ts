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
import { decodeCursor, encodeCursor } from "./cursor.ts";
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

// Clamp the vector-search neighbour count. We share clampLimit's upper
// bound (1000) so a runaway client can't ask for 1M nearest neighbours
// and pull the entire collection.
function clampTopK(topK: number | undefined): number {
  if (typeof topK !== "number" || !Number.isFinite(topK) || topK <= 0) return 30;
  if (topK > 1000) return 1000;
  return Math.floor(topK);
}

// Convert a Postgres `timestamptz::text` literal (e.g. `2026-05-11
// 12:00:00.123456+00`) into the ISO-8601 form CursorCodec validates
// (`YYYY-MM-DDTHH:MM:SS[.fraction]Z`). We keep the fractional seconds
// as-is so the cursor preserves sub-millisecond precision; truncating
// to milliseconds would make cursors imprecise on dense inserts and
// risk skipping rows. The function is tolerant of either `+00` (no
// fractional offset) or `+00:00` Postgres might emit.
function pgTimestampToIso(s: string): string {
  // Replace the space separator with `T` and rewrite any UTC offset to `Z`.
  let out = s.replace(" ", "T");
  out = out.replace(/\+00(:00)?$/, "Z");
  out = out.replace(/-00(:00)?$/, "Z");
  // Some clients return `.000` already trimmed; in either case the
  // cursor codec's ISO_INSTANT_RE accepts 0-9 fractional digits.
  return out;
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
        const limit = clampLimit(op.options?.limit);

        // Cursor-mode path mirrors `DocumentQueryCompiler.compileFind` on
        // the Java side: keyset over `(created_at DESC, id DESC)` with an
        // optional `(created_at, id) < (cursorTs, cursorId)` cutoff. We
        // fetch one extra row so we can decide whether `nextCursor` should
        // be null (no more rows) or the encoded keyset of the last visible
        // row (more rows available).
        if (op.options?.cursorMode === true) {
          const cursorToken = op.options?.cursor ?? null;
          const fetchLimit = limit + 1;
          // We SELECT `created_at::text` alongside the usual columns so the
          // cursor we emit preserves Postgres' microsecond precision.
          // postgres.js otherwise hydrates `timestamptz` into a JS Date,
          // which truncates to millisecond resolution; a cursor minted
          // from a truncated value would exclude rows whose actual
          // `created_at` falls inside the same millisecond window,
          // causing pagination to skip records on dense inserts.
          let rows: Array<{
            id: string;
            data: unknown;
            created_at: Date | string;
            updated_at: Date | string;
            created_at_text: string;
          }>;
          if (cursorToken && typeof cursorToken === "string") {
            const cur = decodeCursor(cursorToken);
            const baseConds = conds ?? [];
            // IMPORTANT: bind via `::text::timestamptz` rather than the
            // direct `::timestamptz` cast. postgres.js detects the cast
            // target and pre-converts the JS string through a Date, which
            // truncates sub-millisecond fractional seconds and would lead
            // the keyset comparison to miss rows whose `created_at` shares
            // a millisecond bucket with the cursor's timestamp. Routing
            // through `::text` first forces Postgres to parse the string
            // directly and preserves microsecond precision.
            const keysetFrag = sql`(created_at, id) < (${cur.createdAt}::text::timestamptz, ${cur.id}::uuid)`;
            const combined = baseConds.length === 0
              ? keysetFrag
              : joinAnd(sql, [...baseConds, keysetFrag]);
            rows = await sql`
              SELECT id, data, created_at, updated_at, created_at::text AS created_at_text
              FROM ${table}
              WHERE ${combined}
              ORDER BY created_at DESC, id DESC
              LIMIT ${fetchLimit}
            `;
          } else if (conds === null) {
            rows = await sql`
              SELECT id, data, created_at, updated_at, created_at::text AS created_at_text
              FROM ${table}
              ORDER BY created_at DESC, id DESC
              LIMIT ${fetchLimit}
            `;
          } else {
            rows = await sql`
              SELECT id, data, created_at, updated_at, created_at::text AS created_at_text
              FROM ${table}
              WHERE ${joinAnd(sql, conds)}
              ORDER BY created_at DESC, id DESC
              LIMIT ${fetchLimit}
            `;
          }
          const hasMore = rows.length > limit;
          const visible = hasMore ? rows.slice(0, limit) : rows;
          let nextCursor: string | null = null;
          if (hasMore && visible.length > 0) {
            const last = visible[visible.length - 1];
            // `created_at::text` returns a Postgres-style timestamptz
            // literal like `2026-05-11 12:00:00.123456+00`. CursorCodec
            // validates ISO-8601 with a `Z` terminator, so normalise.
            const createdAt = pgTimestampToIso(last.created_at_text);
            nextCursor = encodeCursor({ createdAt, id: last.id });
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
      case "search": {
        // Full-text search over the generated `search_text` tsvector
        // column. Mirrors `DocumentQueryCompiler.compileSearch` on the
        // Java side: `websearch_to_tsquery` for the parser (so users can
        // pass quoted phrases / OR-keywords directly) and `ts_rank` for
        // the ORDER BY. The rank score is NOT exposed in the response —
        // Java only returns the doc envelope, so we drop the rank column
        // before mapping. Documenting that here so a future PR doesn't
        // mistake the omission for a bug.
        const queryText = op.query;
        if (typeof queryText !== "string" || queryText.length === 0) {
          return { ok: false, error: "search query must be a non-empty string" };
        }
        const limit = clampLimit(op.options?.limit);
        const rows = await sql`
          SELECT id, data, created_at, updated_at
          FROM ${table}
          WHERE search_text @@ websearch_to_tsquery(${queryText})
          ORDER BY ts_rank(search_text, websearch_to_tsquery(${queryText})) DESC
          LIMIT ${limit}
        `;
        return { ok: true, data: rows.map(rowToDoc) };
      }
      case "vectorSearch": {
        // k-NN over `embedding vector(N)` via the cosine-distance
        // operator `<=>`, which matches the operator class declared on
        // the HNSW index (`vector_cosine_ops`) and the Java compiler's
        // choice in `DocumentQueryCompiler.compileVectorSearch`. Any
        // additional filter is AND-combined with the implicit "all rows"
        // predicate so callers can narrow by tag/category before the
        // similarity sort runs.
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
        // pgvector accepts the textual `[a,b,c]` literal cast to `vector`;
        // the value is bound as a parameter so no user-supplied text ever
        // lands in the SQL string itself.
        const vecLiteral = `[${embedding.join(",")}]`;
        const conds = compileWhere(sql, op.filter);
        const rows = conds === null
          ? await sql`
              SELECT id, data, created_at, updated_at
              FROM ${table}
              ORDER BY embedding <=> ${vecLiteral}::vector
              LIMIT ${topK}
            `
          : await sql`
              SELECT id, data, created_at, updated_at
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

# Functions: chainable query builder

Edge functions can read collection data with a Convex-shape chainable
query builder exposed on `ctx.db.query(name)`. The API mirrors
[Convex's reading-data docs](https://docs.convex.dev/database/reading-data)
one-for-one so handlers ported from a Convex codebase do not require
translation.

The lower-level imperative surface — `ctx.db.collection(name).find(...)`,
`.findOne(...)`, etc. — is unchanged. `ctx.db.query(name)` is the
recommended path for new code.

## Quick reference

```ts
// inside a query/mutation handler:
const recent = await ctx.db
  .query("posts")                                  // returns Query<Doc<TablePosts>>
  .withIndex("by_author", q => q.eq("author", uid))// optional index hint
  .order("desc")                                   // default "asc" by _creationTime
  .filter(q => q.gt(q.field("votes"), 10))         // post-index filter
  .paginate({ cursor: prevCursor, numItems: 20 }); // → { page, isDone, continueCursor }

const single = await ctx.db.query("posts")
  .withIndex("by_id", q => q.eq("_id", id))
  .unique();

const all = await ctx.db.query("posts").collect(); // throws if too many

const first = await ctx.db.query("posts").first(); // null if empty
const some  = await ctx.db.query("posts").take(5);

// search
const hits = await ctx.db.query("posts")
  .withSearchIndex("body_idx", q => q.search("body", "convex"))
  .take(10);

// vector
const similar = await ctx.db.query("posts")
  .withVectorIndex("by_embedding", q => q.vector(embedding, 10))
  .collect();
```

## Chainable methods

Each chainable method returns a NEW `Query<TDoc>` — the builder is
immutable. State accumulates onto a `QueryPlan` that the runtime
compiles to a single SQL statement on the main thread.

| Method | Signature | Description |
|---|---|---|
| `withIndex` | `(name, q => q.eq(...).gt(...))` | Hint an index name. The builder callback `IndexQ` exposes `.eq`, `.gt`, `.gte`, `.lt`, `.lte`, `.range(field, lo, hi)`. Postgres' planner picks the actual index — the name is validated for safety. |
| `withSearchIndex` | `(name, q => q.search(field, text))` | Full-text search via `tsvector` + `websearch_to_tsquery`. `.eq(field, value)` is allowed for filterFields declared in the search index. |
| `withVectorIndex` | `(name, q => q.vector(embedding, k))` | k-NN over `pgvector`'s `<=>` cosine-distance operator. `.eq(field, value)` is allowed for filterFields. |
| `filter` | `(q => q.<expr>)` | Post-index predicate. `FilterQ` exposes `.field(name)`, `.eq/.neq/.gt/.gte/.lt/.lte`, `.and(...)`, `.or(...)`, `.not(...)`. Multiple `.filter()` calls compose with AND. |
| `order` | `("asc" \| "desc")` | Sort by `_creationTime` ASC (default) or DESC. |

## Terminal methods

| Method | Returns | Notes |
|---|---|---|
| `first()` | `Doc \| null` | First matching doc; `null` when empty. |
| `unique()` | `Doc` | Throws if 0 or >1 docs match. |
| `collect()` | `Doc[]` | Throws if the result exceeds `EXCALIBASE_QUERY_MAX_RESULTS` (default 16384). |
| `take(n)` | `Doc[]` | Up to `n` matching docs. |
| `paginate(opts)` | `{ page, isDone, continueCursor }` | Keyset pagination keyed on `(_creation_time, _id)` with a Phase 14 snapshot watermark; see [Pagination snapshot consistency](#pagination-snapshot-consistency). `opts.cursor` is `null` to walk from the start. |

## Convex divergences

These deltas from Convex's published API are intentional for Phase 6.
They are tracked alongside the parity matrix in
[`convex-parity-matrix.md`](./convex-parity-matrix.md).

- `paginate()` result omits Convex's `pageStatus` field. Reactive
  coordination — the subscription-aware status flag — is scheduled
  for Phase 9. The `page` / `isDone` / `continueCursor` triple matches
  Convex exactly.
- Automatic index selection (Phase 6.5) covers `.filter()` against
  declared btree indexes only. When `plan.index` is unset and a
  declared index's leading fields are eq-bound in the filter, the
  worker stamps the index onto the plan and labels it `auto="true"`
  on the `excalibase_query_index_selected_total` Prometheus counter.
  Convex divergences still standing:
  - `withSearchIndex` and `withVectorIndex` require explicit hints.
    Convex auto-selects those too, but the heuristic is field-based,
    not filter-based — scheduled for a Phase 6.5+1 follow-up.
  - Range bounds (`gt`/`gte`/`lt`/`lte`) in the filter do NOT
    contribute to prefix-matching today. A composite `[a, b]` with
    `eq(a) AND gt(b)` matches on `a` only; the gt becomes a
    post-filter. Convex documents the same behaviour.
  - When the user calls `.withIndex(...)` explicitly, the explicit
    hint is final — auto-selection is skipped and the counter
    labels the call `auto="false"`.
- `Query.getDependencies(): string[]` is an Excalibase extension. It
  returns the table names this query touches so the Phase 9 CDC
  scheduler can subscribe to the right watcher streams.

## Plan compiler

The worker collects chained calls into a serialisable `QueryPlan`:

```ts
interface QueryPlan {
  collection: string;
  index?: { name: string; bounds: IndexBound[] };
  search?: { name: string; field: string; query: string; filters: ... };
  vector?: { name: string; embedding: number[]; k: number; filters: ... };
  filter?: FilterExpr;        // AST of and/or/not + comparisons
  order?: "asc" | "desc";
}
```

Terminal methods post the plan over the existing db RPC with
`op: "query"` plus the terminal name and any extra payload. The main
thread runs `executeQueryPlan(sql, plan, terminal, extra)`, which
compiles the plan to a single `postgres.js` tagged template:

- every user-supplied value crosses a parameter boundary (no string
  interpolation),
- every identifier (collection name, field name, index name) is
  regex-validated against `[a-zA-Z_]\w{0,62}` before it ever appears
  in SQL text,
- the Convex-shape projection is constant — `_id`, `_creation_time`,
  `doc` — so the terminal-specific bits are only the
  WHERE / ORDER / LIMIT shape.

## Pagination snapshot consistency

Phase 14 — `.paginate()` captures a millisecond-epoch `snapshotTs`
on the first call (when `opts.cursor === null`) and encodes it into the
returned `continueCursor`. Every subsequent page on the same chain adds
`_creation_time <= snapshotTs` to the WHERE clause, so rows inserted
after pagination started are invisible to that pagination session. This
matches Convex's per-`paginate()` snapshot contract:

- INSERTs that occur between page fetches do **not** appear on later
  pages of the same chain (the SKIP bug is closed).
- The cursor blob is now 3-field (`creationTime|id|snapshotTs`); the
  pre-Phase-14 2-field form is still accepted on decode — pre-14
  cursors capture a fresh snapshot on the next call (degraded only on
  the first paginate after the runtime upgrade).
- The keyset itself still uses `(_creation_time, _id)` so two rows with
  identical `_creation_time` get a deterministic order via the `_id`
  tiebreaker.

Divergences from Convex worth knowing:

- **UPDATEs that mutate `_creation_time`** (only possible via raw SQL —
  the `update` op patches `doc` only) can shift a row into a later
  page's range and cause a duplicate. Convex's storage layer treats
  `_creationTime` as immutable; Excalibase tables enforce that by
  convention but not via DB constraint. If you write raw SQL that
  touches `_creation_time`, pagination is **not** snapshot-consistent
  for that row.
- **Manual snapshot escape hatch** — for true snapshot-by-instant
  pagination without trusting the codec, add an explicit filter:
  `q.filter(q => q.lte(q.field("_creationTime"), initialTs))`. This
  remains effective even across runtime upgrades and is the
  recommended pattern when a client needs to pin a snapshot for
  longer than a single user-facing paginate session.
- **Strict transaction-snapshot mode** is on the roadmap as Phase
  14.1: opening a `REPEATABLE READ` transaction at the first
  paginate and re-attaching to it via `SET TRANSACTION SNAPSHOT`
  on subsequent calls. Reserved for environments that demand
  Postgres-level isolation; the watermark above is the v1 default
  because it imposes no long-lived transaction on the pool.

## Tuning

| Env var | Default | Effect |
|---|---|---|
| `EXCALIBASE_QUERY_MAX_RESULTS` | 16384 | Maximum docs returned by `.collect()` before throwing. Mirrors Convex's 16k cap. |

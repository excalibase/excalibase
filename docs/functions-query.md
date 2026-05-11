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
| `paginate(opts)` | `{ page, isDone, continueCursor }` | Keyset pagination keyed on `(_creation_time, _id)`. `opts.cursor` is `null` to walk from the start. |

## Convex divergences

These deltas from Convex's published API are intentional for Phase 6.
They are tracked alongside the parity matrix in
[`convex-parity-matrix.md`](./convex-parity-matrix.md).

- `paginate()` result omits Convex's `pageStatus` field. Reactive
  coordination — the subscription-aware status flag — is scheduled
  for Phase 9. The `page` / `isDone` / `continueCursor` triple matches
  Convex exactly.
- Automatic index selection is not yet implemented. Convex picks an
  index by matching the filter against declared indexes. Phase 6 keeps
  `.withIndex()` explicit; Phase 6.5 will detect declared indexes
  whose fields cover the filter and apply them transparently.
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

## Tuning

| Env var | Default | Effect |
|---|---|---|
| `EXCALIBASE_QUERY_MAX_RESULTS` | 16384 | Maximum docs returned by `.collect()` before throwing. Mirrors Convex's 16k cap. |

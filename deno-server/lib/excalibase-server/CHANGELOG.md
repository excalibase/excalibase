# Changelog

## 0.11.0

**Breaking**: removed the document-store data plane. The lib now exposes
only the function-execution surface.

Removed from the public API:
- `defineSchema`, `defineTable` (whole schema module)
- `v.*` validators and the `Validator` / `Id` / `Infer` type family
- `makeQuery` and the entire chainable query builder (`Query`,
  `IndexQ`, `FilterQ`, `SearchIndexQ`, `VectorIndexQ`, pagination types)
- Document types: `Doc<T>`, `DbClient`, `CollectionApi`, `CollectionDoc`,
  `SystemFields`, `Filter`, `FilterOps`, `FindOptions`, `UpdateOp`,
  `UpdateResult`, `DeleteResult`
- `ValidationError` and `ValidationIssue`
- `Subscription` type (reactive subscriptions are gone)
- `ctx.db` on every ctx kind — now `db: null` uniformly

Retained:
- `query` / `mutation` / `action` + `internalQuery` / `internalMutation` /
  `internalAction` wrappers
- `httpAction` / `httpRouter` / `Router` / `RouteDef`
- `cronJobs` / `Crons` / `CronJob` / `CronSchedule`
- `ctx.auth` (claims + `getUserIdentity`), `ctx.runQuery/runMutation/runAction`,
  `ctx.scheduler`, `ctx.storage` (reader/writer per kind)
- `FunctionRef` / `makeFunctionRef` / `FunctionDef` / `FunctionKind`
- `ConflictError` + `ConflictSqlState` + `ConflictErrorInit`

Migration: handlers that previously read `ctx.db.collection(name)` must
move their data access to a different layer (REST or GraphQL API). A
future minor release may re-introduce `ctx.db` backed by a different
storage engine; until then the field is `null` on every ctx and any
attempt to call methods on it will throw at runtime.

## 0.10.0

- `ctx.storage` — Convex-parity file-storage surface. Mirrors
  https://docs.convex.dev/file-storage one-for-one.

  ```ts
  import { mutation, query, v } from "@excalibase/server";

  // Mutation: mint a signed PUT URL the client uploads to directly.
  export const generateUploadUrl = mutation({
    args: v.object({}),
    handler: async (ctx) => ctx.storage.generateUploadUrl(),
  });

  // Mutation: attach the storage id returned by the upload to a row.
  export const sendImage = mutation({
    args: v.object({ storageId: v.id("_storage"), author: v.string() }),
    handler: async (ctx, { storageId, author }) => {
      await ctx.db.collection("messages").insert({ author, image: storageId });
    },
  });

  // Query: resolve the storage id to a short-lived signed download URL.
  export const getMessages = query({
    args: v.object({}),
    handler: async (ctx) => {
      const rows = await ctx.db.collection("messages").find();
      return Promise.all(
        rows.map(async (m) => ({
          ...m,
          url: await ctx.storage.getUrl(m.image as never),
        })),
      );
    },
  });
  ```

  Type surface:
  * `QueryCtx.storage`: `StorageReader` — `getUrl`, `get`, `getMetadata`.
    Write methods are intentionally absent so calling them from a query
    fails to type-check.
  * `MutationCtx.storage` / `ActionCtx.storage`: `StorageWriter` — adds
    `generateUploadUrl`, `store`, `delete`.
  * `v.id("_storage")` brands ids as `Id<"_storage">`; mixing them with
    other table ids is a compile error (phantom brand from Phase 13).
  * `StorageFileMetadata`: `{ storageId, sha256, size, contentType? }`.

  BREAKING for downstream Ctx-mocking code: every typed `QueryCtx`,
  `MutationCtx`, `ActionCtx` literal must now carry a `storage` field.
  Handler code that does not construct ctx manually (i.e. ordinary
  query / mutation / action authors) is unaffected — the runtime
  supplies the storage surface.

  Runtime fulfilment lives in excalibase-provisioning's Deno runtime
  (Phase 10 there), routing every method through the main-thread RPC
  channel onto the existing R2-backed storage handler.

## 0.9.0

- `ctx.auth.getUserIdentity()` — Convex-parity typed accessor for the
  verified caller's JWT identity. Returns `Promise<UserIdentity | null>`;
  `null` when the request had no Authorization header.

  ```ts
  import type { UserIdentity } from "@excalibase/server";

  export const me = query({
    args: v.object({}),
    handler: async (ctx) => {
      const id: UserIdentity | null = await ctx.auth.getUserIdentity();
      if (!id) return null;
      return {
        userKey: id.tokenIdentifier,   // `${issuer}|${subject}`
        email: id.email,
        // Custom JWT claims flow through the index signature.
        tenant: id["tenant"],
      };
    },
  });
  ```

  The full surface mirrors
  https://docs.convex.dev/api/interfaces/server.UserIdentity — every
  documented optional claim (`name`, `email`, `emailVerified`, `pictureUrl`,
  `givenName`, `familyName`, `nickname`, `preferredUsername`, `profileUrl`,
  `updatedAt`, `birthday`, `gender`, `language`, `timezone`,
  `phoneNumber`, `phoneNumberVerified`) is exposed, and a
  `[customClaim: string]: unknown` index signature carries everything else.

  The legacy `ctx.auth.claims` raw payload remains so existing Phase 0–11
  callers compile unchanged. Adding `getUserIdentity` is purely additive
  at the type level; runtime fulfilment lives in the excalibase-
  provisioning Deno worker (Phase 12 there).

- `Id<TableName>` — phantom-branded id type returned by `v.id(tableName)`.
  `Id<"posts">` and `Id<"comments">` are mutually unassignable at compile
  time, so passing a posts id where a comments id is expected fails to
  type-check instead of silently issuing a query for the wrong table.

  ```ts
  import { v } from "@excalibase/server";
  import type { Id } from "@excalibase/server";

  const postId = v.id("posts").parse("p_1");   // Id<"posts">
  const commentId: Id<"comments"> = postId;    // compile error
  ```

  Pure TypeScript brand — `v.id(...).parse(...)` accepts the same inputs
  and returns the same string at runtime as before.

  New named exports from the package root: `Id`, `UserIdentity`, `AuthCtx`.

## 0.8.0

- `PaginationResult` now carries an optional `pageStatus` field — a
  Phase 9b reactive coordination hint mirroring Convex's surface:

  ```ts
  type PageStatus = "SplitRecommended" | "SplitRequired" | null;

  interface PaginationResult<TDoc> {
    readonly page: ReadonlyArray<TDoc>;
    readonly isDone: boolean;
    readonly continueCursor: string;
    readonly pageStatus?: PageStatus;
  }
  ```

  Semantics (set by the runtime when the query is served as part of a
  `db.functions.<>.<>.watch()` subscription):

  * `"SplitRecommended"` — the page returned the requested `numItems`
    AND a non-empty `continueCursor` is available. Reactive subscribers
    SHOULD eagerly fetch the next page so the live result stays
    uninterrupted across mutation deltas.
  * `"SplitRequired"` — the underlying data shifted enough that the
    current cursor is no longer valid. Subscribers MUST restart
    pagination from the top before serving more results.
  * `null` — explicit "no hint" from the runtime.
  * Field omitted entirely — backwards-compat behaviour for non-reactive
    callers (no reactive subscription, no field).

  Pure type extension on the lib side — runtime fulfilment lives in the
  excalibase-provisioning Deno runtime (Phase 9b). No breaking changes
  for v0.7.x callers that didn't read `pageStatus`.

  Also exports the named `PageStatus` union from the package root so
  TypeScript consumers can narrow on the value without importing from
  internal paths.

## 0.7.1

- New `ConflictError` thrown by the Excalibase runtime when a mutation's
  automatic retry loop exhausts its max attempts on a serialization
  failure (`40001`) or deadlock (`40P01`). Carries `code:
  "MUTATION_CONFLICT"`, the attempt count, and the final SQLSTATE +
  Postgres message so callers (and the public HTTP layer, which maps
  this to a `409 Conflict`) can distinguish a contention exhaustion
  from a generic runtime error.

  ```ts
  import { ConflictError } from "@excalibase/server";

  try {
    await ctx.runMutation(internal.x.y, args);
  } catch (e) {
    if (e instanceof ConflictError) {
      // give up, surface to caller, or fall back — this is a real
      // contention exhaustion, not a logic bug.
    }
    throw e;
  }
  ```

  The retry loop itself lives in the Deno runtime (top-level mutations
  only; actions never retry, nested ctx.runMutation rides the parent's
  txn — see provisioning Phase 9a).

## 0.7.0

- New `ctx.scheduler` surface on `MutationCtx` and `ActionCtx` for
  deferred function execution. Mirrors Convex's scheduler API:

  ```ts
  // From a mutation — enqueue lives inside the mutation's Postgres
  // transaction. Mutation rolls back ⇒ scheduled task never runs.
  export default mutation({
    args: v.object({ to: v.string() }),
    handler: async (ctx, { to }) => {
      const id = await ctx.scheduler.runAfter(60_000, internal.jobs.send, { to });
      return { scheduledId: id };
    },
  });

  // From an action — enqueue commits immediately, regardless of action outcome.
  export default action({
    args: v.object({ at: v.number() }),
    handler: async (ctx, { at }) => {
      await ctx.scheduler.runAt(at, api.jobs.tick, {});
    },
  });

  // Cancel a previously scheduled task.
  await ctx.scheduler.cancel(scheduledId);
  ```

  The `Scheduler` type ships from `@excalibase/server`; the runtime
  injects the real implementation. `QueryCtx` deliberately does NOT
  carry `scheduler` (read-only contract — calling the scheduler from a
  query would violate side-effect-free semantics).

- New `cronJobs()` registry for declaring scheduled jobs at module
  scope. Mirrors Convex's `cronJobs()` API:

  ```ts
  // crons.ts
  import { cronJobs } from "@excalibase/server";
  const crons = cronJobs();
  crons.daily("send-digest", { hourUTC: 9, minuteUTC: 0 },
    internal.jobs.sendDigest, {});
  crons.cron("every-5-min", "*/5 * * * *", internal.jobs.tick, {});
  crons.interval("heartbeat", { minutes: 10 }, internal.jobs.beat, {});
  crons.hourly("purge-stale", { minuteUTC: 17 }, internal.jobs.purge, {});
  export default crons;
  ```

  The registry validates cron syntax (5-field standard form),
  duplicate names, FunctionRef shape, and schedule bounds at registration
  time so a bad expression fails the deploy rather than the first tick.
  Bundler picks up the table via `globalThis.__excalibase_crons`.

- New exported names: `cronJobs`, `isCrons`, `validateCronExpression`,
  and the `Scheduler` / `ScheduledId` / `Crons` / `CronJob` /
  `CronSchedule` types.

## 0.6.0

- New `internalQuery` / `internalMutation` / `internalAction` wrappers.
  Same signatures as their public counterparts but tagged
  `isInternal: true` on the returned `FunctionDef`. The Excalibase Go
  gateway short-circuits `PublicInvoke` traffic targeting internal
  functions to 404 (indistinguishable from a missing function — no
  information leak); they are reachable only via `ctx.runQuery` /
  `ctx.runMutation` / `ctx.runAction` from another function or via the
  trusted internal-invoke admin path.
- New `ctx.runQuery / runMutation / runAction` surface on every `Ctx`
  variant. The composition shape is Convex-compatible:

  ```ts
  // QueryCtx — read-only; runMutation and runAction are compile errors.
  export default query({
    args: v.object({ id: v.id("users") }),
    handler: async (ctx, { id }) => {
      const user = await ctx.db.get(id);
      const friends = await ctx.runQuery(api.users.friendsOf, { id });
      return { user, friends };
    },
  });

  // MutationCtx — runQuery + runMutation, no runAction. The runtime
  // executes nested mutations inside the caller's Postgres transaction
  // so the composite write is atomic. Caller mutation rolls back =>
  // every nested mutation rolls back together.
  export default mutation({
    args: v.object({ uid: v.id("users") }),
    handler: async (ctx, { uid }) => {
      await ctx.runMutation(internal.admin.cleanup, { uid });
      return await ctx.db.collection("users").getById(uid);
    },
  });

  // ActionCtx — all three. Each call runs in its own DB transaction.
  ```

  The `EXCALIBASE_RUN_MAX_DEPTH` env var (default `8`) caps the
  composition depth at the runtime to prevent infinite recursion.
- New typed function reference: `FunctionRef<TArgs, TResult>` and
  `makeFunctionRef(moduleName, exportName)`. The codegen-side
  `api.<module>.<export>` graph will emit this shape so cross-function
  call types flow end-to-end. Runtime guard: `isFunctionRef(value)`.
- New `httpAction(handler)` wrapper and `httpRouter()` factory for
  raw HTTP endpoints. The router exposes `.route({ path, method,
  handler })` (chainable) and `.getRoutes()` (defensive copy). The
  bundler extracts the route table from the default export and
  persists it on the Function record; the Go gateway dispatches each
  incoming request against the persisted table without re-loading the
  bundle.

  ```ts
  // http.ts
  import { httpAction, httpRouter } from "@excalibase/server";
  const http = httpRouter();
  http.route({
    path: "/webhook",
    method: "POST",
    handler: httpAction(async (ctx, req) => {
      const body = await req.json();
      return new Response(JSON.stringify({ ok: true }), {
        status: 202,
        headers: { "content-type": "application/json" },
      });
    }),
  });
  export default http;
  ```

  Supported methods: `GET`, `POST`, `PUT`, `PATCH`, `DELETE`,
  `OPTIONS`. Route handlers must be `httpAction(...)` results — bare
  function values are rejected with a `TypeError` at registration
  time. Paths must start with `/`.
- New named exports: `internalQuery`, `internalMutation`,
  `internalAction`, `httpAction`, `httpRouter`, `isHttpRouter`,
  `isHttpAction`, `isInternalFn`, `makeFunctionRef`, `isFunctionRef`,
  `ALLOWED_HTTP_METHODS`.
- New type exports: `FunctionRef`, `HttpActionDef`, `HttpMethod`,
  `RouteDef`, `Router`, `QueryRunners`, `MutationRunners`,
  `ActionRunners`.

### Divergences from Convex

- `ctx.runQuery`'s read-only contract is enforced by TypeScript, not at
  runtime: the worker dispatcher will happily invoke a mutation from a
  query if the caller dodges the type system. Convex enforces this at
  both layers; Phase 8 follow-up will tighten the runtime path.
- `httpRouter()` does not yet support nested routers or path
  parameters — Phase 7 keeps the route table flat. The Excalibase
  bundler reads `.getRoutes()` once at deploy time, so dynamic routes
  added after deploy would not take effect anyway.
- The `api.<module>.<export>` typed graph emitted by the SDK codegen
  side lands in Phase 7.1 (out of scope here because Phase 2 codegen
  is in `excalibase-sdk-js`, not this package). Until then, callers
  build refs via `makeFunctionRef(...)` directly.

## 0.5.0

- New Convex-shape chainable query builder on `ctx.db.query(name)`. Mirrors
  `https://docs.convex.dev/database/reading-data` one-for-one:

  ```ts
  const recent = await ctx.db
    .query("posts")
    .withIndex("by_author", q => q.eq("author", uid))
    .order("desc")
    .filter(q => q.gt(q.field("votes"), 10))
    .paginate({ cursor: prevCursor, numItems: 20 });
  ```

  Chainable methods: `.withIndex`, `.withSearchIndex`, `.withVectorIndex`,
  `.filter`, `.order`. Each returns a new `Query` instance — the builder
  is immutable.

  Terminal methods: `.first()` → `Doc | null`, `.unique()` → `Doc`
  (throws if 0 or >1), `.collect()` → `Doc[]` (throws if exceeds
  `EXCALIBASE_QUERY_MAX_RESULTS`, default 16384), `.take(n)` → `Doc[]`,
  `.paginate({ cursor, numItems })` → `{ page, isDone, continueCursor }`.

  Builder callback shapes mirror Convex exactly:

  * `IndexQ`: `.eq/.gt/.gte/.lt/.lte/.range(field, lo, hi)`.
  * `SearchIndexQ`: `.search(field, queryText)` plus optional `.eq` for
    filterFields.
  * `VectorIndexQ`: `.vector(embedding, k)` plus optional `.eq` for
    filterFields.
  * `FilterQ`: `.field(name)`, `.eq/.neq/.gt/.gte/.lt/.lte`, `.and/.or`
    (variadic), `.not(expr)`. Multiple `.filter()` calls compose with AND.
- `DbClient.query(name)` is now part of the public surface. The existing
  `.collection(name)` API is unchanged — it stays the lower-level path
  for code that prefers the imperative shape.
- New named export `makeQuery(name, runtime)` so the runtime side (and
  unit tests) can construct a `Query<TDoc>` against an injected runtime
  callback without going through a full `ctx.db.query()` chain.
- New types exported: `Query`, `QueryPlan`, `QueryRuntime`, `QueryTerminal`,
  `IndexQ`, `IndexBound`, `IndexHint`, `SearchIndexQ`, `SearchIndexHint`,
  `VectorIndexQ`, `VectorIndexHint`, `FilterQ`, `FilterExpr`,
  `PaginationOptions`, `PaginationResult`.

### Divergences from Convex

- `.paginate()` result omits Convex's `pageStatus` field. Reactive
  coordination (a subscription-aware status flag) is scheduled for
  Phase 9; the `page` / `isDone` / `continueCursor` triple matches.
- Automatic index selection from filter shape is not yet implemented —
  Phase 6 keeps `.withIndex()` explicit. Phase 6.5 follow-up will detect
  declared indexes whose fields match the filter and apply them without
  the explicit hint.
- `Query.getDependencies()` is an Excalibase extension (not part of
  Convex's API) — it returns the table names this query touches so the
  Phase 9 CDC scheduler can subscribe to the right watcher streams.

## 0.4.0

- New `Doc<T>` and `SystemFields` types: every stored document carries
  `_id: string` and `_creationTime: number` (Convex parity). `Doc<T>` is the
  intersection of caller-supplied user fields with the system fields.
- `CollectionDoc` is now an alias for `Doc<Record<string, unknown>>` — the
  legacy `id` / `createdAt` / `updatedAt` triple has been removed.
- `CollectionApi.insert` and `.insertMany` accept `Omit<TDoc, "_id" |
  "_creationTime">` so the caller cannot supply system fields. Both system
  fields are generated by the runtime at insert time.
- `CollectionApi.search(query, opts?)` and `.vectorSearch(embedding, opts?)`
  are now typed `Promise<ReadonlyArray<TDoc>>` (previously `Promise<never>`
  placeholders). The runtime side has had the implementation since Phase
  1.5; this commit only updates the public type surface to match.
- `vectorSearch`'s options take an optional `filter` so callers can AND-
  combine a Mongo-style predicate with the similarity sort.
- `UpdateResult<TDoc>` and `DeleteResult<TDoc>` are now generic over the
  doc shape; callers narrowing `CollectionApi<UserDoc>` get
  `UpdateResult<UserDoc>` automatically.
- `v.id(tableName)` validator documents the relaxation to accept any non-
  empty string. The runtime mints ids in a 30-char lowercase base32
  alphabet (matches Convex's opaque id shape); validation does not pin the
  format so future layers (Phase 6 branded ids, externally transported
  ids) keep working.

### Breaking changes

- `CollectionDoc` no longer exposes `id` / `createdAt` / `updatedAt`. Any
  code that read those properties off a `Doc` returned by `ctx.db` must
  migrate to `_id` / `_creationTime`. There is no shim.
- `CollectionApi.insert(doc)` rejects `_id` / `_creationTime` at the type
  level. Code that previously passed either field now fails to compile;
  remove them from the payload.
- `CollectionApi.search` / `.vectorSearch` return types changed from
  `Promise<never>` to `Promise<ReadonlyArray<TDoc>>`. Callers that had
  guarded against the placeholder no longer need the guard.

## 0.3.0

- New `v.*` validator namespace mirroring Convex's `convex/values` API exactly:
  `v.string()`, `v.number()`, `v.boolean()`, `v.int64()`, `v.bytes()`,
  `v.null()`, `v.any()`, `v.id(tableName)`, `v.literal(value)`,
  `v.array(item)`, `v.object(shape)`, `v.union(...members)`,
  `v.optional(value)`, `v.record(keys, values)`. Each validator exposes
  `.kind`, `.parse(input)` (throws `ValidationError` with `{ path, message }[]`),
  and `.toJsonSchema()` (JSON Schema 2020-12).
- New schema declaration surface: `defineTable(validator)` returns a
  `TableBuilder` with chainable `.index(name, fields)`,
  `.searchIndex(name, opts)`, and `.vectorIndex(name, opts)`.
  `defineSchema({ tableName: builder, ... })` returns a `Schema` that emits a
  serialisable JSON representation via `.toJsonSchema()`. Convex system
  fields `_id` and `_creationTime` are auto-injected into every table.
- `defineSchema` writes the resulting JSON schema to
  `globalThis.__excalibase_schema` so the platform bundler can extract it
  after running the user module — same side-channel pattern Convex uses.
- New type exports: `Validator`, `ValidatorKind`, `Infer`, `JsonSchemaNode`,
  `IdValidator`, `LiteralValidator`, `ArrayValidator`, `ObjectValidator`,
  `ObjectShape`, `ObjectInfer`, `UnionValidator`, `OptionalValidator`,
  `RecordValidator`, `Schema`, `SchemaJsonSchema`, `TableBuilder`,
  `TableJsonSchema`, `IndexDef`, `SearchIndexDef`, `SearchIndexOptions`,
  `VectorIndexDef`, `VectorIndexOptions`, `ValidatorJsonSchemaObject`.

## 0.2.0

- Per-kind ctx: `query` handlers now receive `QueryCtx`, `mutation` handlers
  receive `MutationCtx`, and `action` handlers receive `ActionCtx`. The legacy
  `Ctx` alias is preserved as the union of all three.
- `QueryCtx.db` and `MutationCtx.db` are non-null `DbClient`. `ActionCtx.db`
  is `null` — actions invoke other functions via `runQuery`/`runMutation`
  (Phase 1.5) instead of touching the database directly.
- Real `DbClient` surface (Phase 1): `collection(name)` returns a
  `CollectionApi<Doc>` with `insert`, `insertMany`, `find`, `findOne`,
  `getById`, `update`, `delete`, `count`. `search` and `vectorSearch` are
  typed as `() => Promise<never>` placeholders that throw at runtime; full
  implementation lands in Phase 1.5.
- New `ValidationError` exception class. Carries a frozen `issues` array of
  `{ path, message }` entries — mirrors the Java `JsonSchemaValidator` shape.
- New type exports: `QueryCtx`, `MutationCtx`, `ActionCtx`, `CollectionApi`,
  `CollectionDoc`, `Filter`, `FilterOps`, `FindOptions`, `UpdateOp`,
  `UpdateResult`, `DeleteResult`, `ValidationIssue`.

### Breaking changes

- `FunctionDef` now carries a fourth generic parameter (`TCtx`). Code that
  references `FunctionDef<...>` directly must update its type arguments;
  most callers use the `QueryDef` / `MutationDef` / `ActionDef` aliases and
  are unaffected.
- A handler typed against the wrong ctx (e.g. `query` with an `ActionCtx`
  handler) is now a compile error.

## 0.1.0

- Initial public release.
- `query`, `mutation`, `action` tagged wrappers.
- `Ctx`, `AuthClaims`, `DbClient` (placeholder) type exports.
- `zod-to-json-schema` integration for runtime JSON Schema metadata.

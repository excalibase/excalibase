import type { z } from "zod";
import type { zodToJsonSchema } from "zod-to-json-schema";

import type { Scheduler } from "./scheduler";
import type { StorageReader, StorageWriter } from "./storage";

/**
 * Authentication claims extracted from the request. Shape is intentionally
 * loose so the runtime can inject whatever the project's auth provider
 * produces (JWT claims, session payload, etc.).
 */
export type AuthClaims = Record<string, unknown>;

/**
 * Typed view of a verified JWT. The runtime constructs this from the JWT
 * payload after the gateway has verified the signature.
 * `tokenIdentifier` is the canonical `"{issuer}|{subject}"` string that
 * uniquely keys a user across providers — use it as the foreign key in
 * your own user tables. Standard OIDC claims have typed slots here;
 * unknown / project-specific claims pass through the index signature so
 * handler code can read them without losing the typed view of the
 * standard ones.
 *
 * Returned by `ctx.auth.getUserIdentity()` — the recommended way to read
 * the caller's identity. The legacy `ctx.auth.claims` remains for code
 * that needs the raw payload shape.
 */
export interface UserIdentity {
  /** `"{issuer}|{subject}"` — stable cross-provider identifier. */
  readonly tokenIdentifier: string;
  /** JWT `sub` claim. */
  readonly subject: string;
  /** JWT `iss` claim. */
  readonly issuer: string;
  readonly name?: string;
  readonly email?: string;
  readonly emailVerified?: boolean;
  readonly phoneNumber?: string;
  readonly phoneNumberVerified?: boolean;
  readonly pictureUrl?: string;
  readonly givenName?: string;
  readonly familyName?: string;
  readonly nickname?: string;
  readonly preferredUsername?: string;
  readonly profileUrl?: string;
  readonly updatedAt?: string;
  readonly birthday?: string;
  readonly gender?: string;
  readonly language?: string;
  readonly timezone?: string;
  /**
   * Index signature for any other claim the JWT carried (custom roles,
   * tenant IDs, vendor namespaces, etc.).
   */
  readonly [customClaim: string]: unknown;
}

/**
 * Auth surface shared by every ctx kind. Carries the raw `claims` (legacy)
 * plus the typed `getUserIdentity()` helper.
 */
export interface AuthCtx {
  readonly claims: AuthClaims | null;
  /**
   * Returns the verified caller's identity, or `null` when the request
   * had no Authorization header / no recognisable JWT.
   */
  getUserIdentity(): Promise<UserIdentity | null>;
}

/**
 * Per-request context constructed by the Excalibase Deno runtime and passed
 * as the first argument to every wrapped handler.
 *
 * `Ctx` is the union of the three concrete contexts (Query/Mutation/Action).
 */
export type Ctx = QueryCtx | MutationCtx | ActionCtx;

/**
 * Context passed to query handlers. `db` is `null` — the document-store
 * data plane was removed. Future iterations may re-introduce a typed
 * client here.
 */
export interface QueryCtx extends QueryRunners {
  readonly db: null;
  readonly auth: AuthCtx;
  /** Read-only storage surface — getUrl / get / getMetadata only. */
  readonly storage: StorageReader;
}

/**
 * Context passed to mutation handlers. `db` is `null` (see QueryCtx).
 * Mutations can additionally invoke other mutations via `ctx.runMutation`.
 */
export interface MutationCtx extends MutationRunners {
  readonly db: null;
  readonly auth: AuthCtx;
  /** Scheduler surface — enqueue functions for later execution. */
  readonly scheduler: Scheduler;
  /** Storage surface — read + write. */
  readonly storage: StorageWriter;
}

/**
 * Context passed to action handlers. `db` is `null` — actions cannot
 * touch the database directly. Use `runQuery` / `runMutation` /
 * `runAction` to invoke other functions.
 */
export interface ActionCtx extends ActionRunners {
  readonly db: null;
  readonly auth: AuthCtx;
  /** Scheduler surface — action enqueues commit immediately. */
  readonly scheduler: Scheduler;
  /** Storage surface — read + write. */
  readonly storage: StorageWriter;
}

/** Discriminator for the three flavours of Excalibase Function. */
export type FunctionKind = "query" | "mutation" | "action";

/**
 * Typed reference to a deployed function. Produced by `api.<module>.<export>`
 * in user code and by `makeFunctionRef(...)` in tests and lower-level
 * callers. Carries phantom type slots `_args` and `_result` so
 * `ctx.runQuery/runMutation/runAction` can infer the wire types.
 */
export interface FunctionRef<TArgs = unknown, TResult = unknown> {
  readonly moduleName: string;
  readonly exportName: string;
  /** Phantom slot — present for type inference, never read at runtime. */
  readonly _args?: TArgs;
  /** Phantom slot — present for type inference, never read at runtime. */
  readonly _result?: TResult;
}

/**
 * Composition surface available on a `QueryCtx`. Read-only — calling a
 * mutation from inside a query would violate the transactional contract,
 * so `runMutation` and `runAction` are intentionally absent.
 */
export interface QueryRunners {
  runQuery<TArgs, TResult>(ref: FunctionRef<TArgs, TResult>, args: TArgs): Promise<TResult>;
}

/**
 * Composition surface available on a `MutationCtx`. Mutations can call
 * other queries and mutations. They cannot call actions — actions
 * perform side effects which would violate mutation atomicity.
 */
export interface MutationRunners {
  runQuery<TArgs, TResult>(ref: FunctionRef<TArgs, TResult>, args: TArgs): Promise<TResult>;
  runMutation<TArgs, TResult>(ref: FunctionRef<TArgs, TResult>, args: TArgs): Promise<TResult>;
}

/**
 * Composition surface available on an `ActionCtx`. Actions live outside
 * any transaction so they may invoke queries, mutations, and other
 * actions.
 */
export interface ActionRunners {
  runQuery<TArgs, TResult>(ref: FunctionRef<TArgs, TResult>, args: TArgs): Promise<TResult>;
  runMutation<TArgs, TResult>(ref: FunctionRef<TArgs, TResult>, args: TArgs): Promise<TResult>;
  runAction<TArgs, TResult>(ref: FunctionRef<TArgs, TResult>, args: TArgs): Promise<TResult>;
}

/** JSON Schema metadata produced by `zod-to-json-schema`. */
export type ArgsJsonSchema = ReturnType<typeof zodToJsonSchema>;

/**
 * Generic shape returned by `query` / `mutation` / `action`. The runtime
 * pattern-matches on `kind` and invokes `handler(ctx, parsedArgs)`. Each
 * kind carries its own ctx type so handler authors can't accidentally use
 * the wrong surface.
 */
export interface FunctionDef<TKind extends FunctionKind, TCtx, TArgs, TResult> {
  readonly kind: TKind;
  readonly args: z.ZodType<TArgs>;
  readonly handler: (ctx: TCtx, args: TArgs) => Promise<TResult>;
  /**
   * `true` when produced by `internalQuery` / `internalMutation` /
   * `internalAction`. The Go gateway treats internal-only functions as
   * non-existent on the public route (returns 404, indistinguishable from
   * a truly missing function) so they can only be reached via
   * `ctx.runQuery / ctx.runMutation / ctx.runAction` or the trusted
   * internal-invoke admin path.
   */
  readonly isInternal?: boolean;
  readonly __metadata: {
    readonly argsJsonSchema: ArgsJsonSchema;
  };
}

export type QueryDef<TArgs, TResult> = FunctionDef<"query", QueryCtx, TArgs, TResult>;
export type MutationDef<TArgs, TResult> = FunctionDef<"mutation", MutationCtx, TArgs, TResult>;
export type ActionDef<TArgs, TResult> = FunctionDef<"action", ActionCtx, TArgs, TResult>;

/**
 * HTTP action def — returned by `httpAction(handler)`. Unlike the
 * query/mutation/action triple, this carries no zod validator: the
 * handler receives the raw `Request` and is responsible for parsing
 * whatever body shape it needs. The runtime dispatches by passing the
 * incoming request through unchanged and writing the returned `Response`
 * straight back to the caller.
 */
export interface HttpActionDef {
  readonly kind: "httpAction";
  readonly handler: (ctx: ActionCtx, req: Request) => Promise<Response>;
  readonly __metadata: Record<string, unknown>;
}

/** HTTP method set accepted by `Router.route()`. */
export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "OPTIONS";

/**
 * One route registered on an `httpRouter()`. The `handler` is always an
 * `HttpActionDef` produced by `httpAction(...)`; the router rejects bare
 * handler functions at registration time.
 */
export interface RouteDef {
  readonly path: string;
  readonly method: HttpMethod;
  readonly handler: HttpActionDef;
}

/**
 * Object returned by `httpRouter()`. Implements the minimal router
 * surface: `.route(...)` registers a triple, `.getRoutes()` returns a
 * defensive copy used by the bundler to extract the route table.
 *
 * The Excalibase bundler reads the route table at deploy time via the
 * globalThis side-channel and persists it on the Function record so the
 * Go gateway can dispatch incoming requests to the right route without
 * re-loading the bundle.
 */
export interface Router {
  route(def: RouteDef): Router;
  getRoutes(): RouteDef[];
}

/**
 * Loose alias used by helpers that operate on any function def regardless of
 * its generic parameters. The handler params are typed `any` (not `unknown`)
 * so that variance keeps `QueryDef<X, Y>` assignable to `AnyFunctionDef` for
 * arbitrary `X`/`Y`.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type AnyFunctionDef = FunctionDef<FunctionKind, any, any, any>;

import type { z } from "zod";
import { zodToJsonSchema } from "zod-to-json-schema";

import type {
  ActionCtx,
  ActionDef,
  FunctionDef,
  FunctionKind,
  HttpActionDef,
  MutationCtx,
  MutationDef,
  QueryCtx,
  QueryDef,
} from "./types";

interface WrapperConfig<TCtx, TSchema extends z.ZodTypeAny, TResult> {
  readonly args: TSchema;
  readonly handler: (ctx: TCtx, args: z.infer<TSchema>) => Promise<TResult>;
}

function defineFunction<
  TKind extends FunctionKind,
  TCtx,
  TSchema extends z.ZodTypeAny,
  TResult,
>(
  kind: TKind,
  config: WrapperConfig<TCtx, TSchema, TResult>,
  isInternal: boolean,
): FunctionDef<TKind, TCtx, z.infer<TSchema>, TResult> {
  const argsJsonSchema = zodToJsonSchema(config.args);
  const def: FunctionDef<TKind, TCtx, z.infer<TSchema>, TResult> = {
    kind,
    args: config.args,
    handler: config.handler,
    __metadata: { argsJsonSchema },
    // Carry `isInternal` only when true so v1 records stay byte-identical
    // and `JSON.stringify(def)` matches the legacy shape for the public
    // surface.
    ...(isInternal ? { isInternal: true as const } : {}),
  };
  return def;
}

/**
 * Define a read-only Excalibase Function. The wrapper does not execute the
 * handler — the runtime invokes it per request with a constructed `QueryCtx`
 * carrying auth, storage, runQuery, and (db: null reserved for future use).
 */
export function query<TSchema extends z.ZodTypeAny, TResult>(
  config: WrapperConfig<QueryCtx, TSchema, TResult>,
): QueryDef<z.infer<TSchema>, TResult> {
  return defineFunction("query", config, false);
}

/**
 * Define a write Excalibase Function (creates/updates/deletes inside the
 * transactional database client). Same inert-wrapper semantics as `query`;
 * the runtime passes a `MutationCtx`.
 */
export function mutation<TSchema extends z.ZodTypeAny, TResult>(
  config: WrapperConfig<MutationCtx, TSchema, TResult>,
): MutationDef<z.infer<TSchema>, TResult> {
  return defineFunction("mutation", config, false);
}

/**
 * Define an Excalibase Function that may perform side effects against
 * external systems (HTTP calls, queues, etc.). Same inert-wrapper semantics
 * as `query`. Receives an `ActionCtx` with `db: null` — actions invoke other
 * functions rather than touching the database directly.
 */
export function action<TSchema extends z.ZodTypeAny, TResult>(
  config: WrapperConfig<ActionCtx, TSchema, TResult>,
): ActionDef<z.infer<TSchema>, TResult> {
  return defineFunction("action", config, false);
}

/**
 * Define an internal-only query. Behaves identically to `query` at the
 * type and zod-validation layers, but the platform gateway returns 404
 * for `PublicInvoke` traffic targeting it. Only callable via
 * `ctx.runQuery(api.module.export, args)` from another function in the
 * same project, or via the trusted internal-invoke admin path.
 *
 * The `isInternal: true`
 * marker rides on the same `FunctionDef` record the public wrappers
 * produce so downstream tooling (codegen, bundler, runtime) only needs
 * to read one extra field.
 */
export function internalQuery<TSchema extends z.ZodTypeAny, TResult>(
  config: WrapperConfig<QueryCtx, TSchema, TResult>,
): QueryDef<z.infer<TSchema>, TResult> {
  return defineFunction("query", config, true);
}

/** Internal-only mutation — see `internalQuery`. */
export function internalMutation<TSchema extends z.ZodTypeAny, TResult>(
  config: WrapperConfig<MutationCtx, TSchema, TResult>,
): MutationDef<z.infer<TSchema>, TResult> {
  return defineFunction("mutation", config, true);
}

/** Internal-only action — see `internalQuery`. */
export function internalAction<TSchema extends z.ZodTypeAny, TResult>(
  config: WrapperConfig<ActionCtx, TSchema, TResult>,
): ActionDef<z.infer<TSchema>, TResult> {
  return defineFunction("action", config, true);
}

/**
 * Define an HTTP action — a raw `(ctx, Request) => Response` handler
 * mounted on `httpRouter()`. Unlike `action`, there is no zod validator:
 * the handler reads the request body itself.
 *
 * The Excalibase runtime dispatches incoming requests via the v2 worker path
 * bypass — instead of parsing `{ args }`, it forwards the raw method,
 * URL, headers, and body straight to the handler.
 */
export function httpAction(
  handler: (ctx: ActionCtx, req: Request) => Promise<Response>,
): HttpActionDef {
  return {
    kind: "httpAction",
    handler,
    __metadata: {},
  };
}

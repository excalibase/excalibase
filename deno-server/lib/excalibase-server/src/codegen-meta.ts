import type { AnyFunctionDef, ArgsJsonSchema, FunctionKind, HttpActionDef } from "./types";

const VALID_KINDS: ReadonlySet<FunctionKind> = new Set<FunctionKind>([
  "query",
  "mutation",
  "action",
]);

/**
 * Structural type guard for objects produced by `query` / `mutation` /
 * `action` (or their `internalX` variants). Useful for codegen tooling
 * that scans a module's exports and filters down to Excalibase Function
 * definitions.
 *
 * Phase 7: also returns true for `httpAction(...)` results — they share
 * the same export discovery path and should reach the bundler's
 * function-scan loop. The kind discriminator on the returned record
 * still distinguishes httpAction from the validator-carrying flavours.
 */
export function isFunctionDef(value: unknown): value is AnyFunctionDef | HttpActionDef {
  if (value === null || typeof value !== "object") {
    return false;
  }
  const candidate = value as {
    kind?: unknown;
    args?: unknown;
    handler?: unknown;
    __metadata?: { argsJsonSchema?: unknown } | Record<string, unknown> | null;
  };
  if (typeof candidate.kind !== "string") {
    return false;
  }
  if (typeof candidate.handler !== "function") {
    return false;
  }
  if (candidate.__metadata === undefined || candidate.__metadata === null) {
    return false;
  }
  if (typeof candidate.__metadata !== "object") {
    return false;
  }
  // httpAction has no args validator (raw Request goes to the handler);
  // the validator-carrying kinds must have a non-null args slot AND a
  // populated argsJsonSchema.
  if (candidate.kind === "httpAction") {
    return true;
  }
  if (!VALID_KINDS.has(candidate.kind as FunctionKind)) {
    return false;
  }
  if (candidate.args === undefined || candidate.args === null) {
    return false;
  }
  const meta = candidate.__metadata as { argsJsonSchema?: unknown };
  if (meta.argsJsonSchema === undefined) {
    return false;
  }
  return true;
}

/** Return the kind discriminator of a function def. */
export function getFunctionKind(def: AnyFunctionDef): FunctionKind {
  return def.kind;
}

/** Return the JSON Schema produced for the def's args at definition time. */
export function getArgsJsonSchema(def: AnyFunctionDef): ArgsJsonSchema {
  return def.__metadata.argsJsonSchema;
}

/**
 * Structural type guard for `httpAction(...)` results. Distinct from
 * `isFunctionDef` so codegen can route http handlers to their own table
 * persistence path without re-checking the `kind` field.
 */
export function isHttpAction(value: unknown): value is HttpActionDef {
  if (value === null || typeof value !== "object") return false;
  const v = value as { kind?: unknown; handler?: unknown; __metadata?: unknown };
  return v.kind === "httpAction" && typeof v.handler === "function" && v.__metadata !== undefined;
}

/**
 * Structural test for the `isInternal: true` marker added by
 * `internalQuery / internalMutation / internalAction`. The Go gateway
 * uses this in the `PublicInvoke` path to short-circuit to a 404 before
 * the runtime is touched; the worker dispatcher uses it to validate
 * that the target of `ctx.runQuery / runMutation / runAction` is
 * actually reachable from the caller.
 */
export function isInternalFn(value: unknown): boolean {
  if (!isFunctionDef(value)) return false;
  const v = value as { isInternal?: unknown };
  return v.isInternal === true;
}

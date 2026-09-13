import type { FunctionRef } from "./types";

/**
 * Construct a typed function reference. Phase 7.1 codegen will emit a
 * fully-typed `api.<module>.<export>` graph that calls this under the
 * hood; tests and lower-level callers may invoke it directly.
 *
 * The phantom `TArgs` / `TResult` slots flow through to
 * `ctx.runQuery / ctx.runMutation / ctx.runAction` so the compiler can
 * enforce the cross-function call shape end-to-end.
 *
 * The returned object is plain JSON-serialisable — `moduleName` and
 * `exportName` ride across the worker postMessage bridge unchanged.
 */
export function makeFunctionRef<TArgs = unknown, TResult = unknown>(
  moduleName: string,
  exportName: string,
): FunctionRef<TArgs, TResult> {
  if (typeof moduleName !== "string" || moduleName.length === 0) {
    throw new TypeError("makeFunctionRef: moduleName must be a non-empty string");
  }
  if (typeof exportName !== "string" || exportName.length === 0) {
    throw new TypeError("makeFunctionRef: exportName must be a non-empty string");
  }
  return Object.freeze({ moduleName, exportName }) as FunctionRef<TArgs, TResult>;
}

/**
 * Runtime guard used by the worker dispatcher to validate the first
 * argument of `ctx.runQuery / runMutation / runAction`. Rejects empty
 * strings explicitly so the wire payload always carries usable values.
 */
export function isFunctionRef(value: unknown): value is FunctionRef {
  if (value === null || typeof value !== "object") return false;
  const v = value as { moduleName?: unknown; exportName?: unknown };
  return (
    typeof v.moduleName === "string" &&
    v.moduleName.length > 0 &&
    typeof v.exportName === "string" &&
    v.exportName.length > 0
  );
}

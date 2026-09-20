/**
 * Type-level + runtime tests for `ctx.runQuery/runMutation/runAction`.
 *
 * The Phase 7 surface adds compositional invocation to every Ctx variant:
 *
 *  * `QueryCtx.runQuery(fnRef, args)` — read-only; calling a mutation from a
 *    query is a compile-time error.
 *  * `MutationCtx.{runQuery, runMutation}` — read or chain inside the same
 *    transaction. `runAction` is unavailable in a mutation context (
 *    parity: mutations are transactional and can't perform side effects).
 *  * `ActionCtx.{runQuery, runMutation, runAction}` — all three available.
 *
 * The actual main-thread RPC plumbing lives in the runtime. Lib-side tests
 * confirm the type surface compiles and that the typed function references
 * round-trip through `makeFunctionRef`.
 */
import { z } from "zod";

import {
  makeFunctionRef,
  isFunctionRef,
  type ActionCtx,
  type FunctionRef,
  type MutationCtx,
  type QueryCtx,
} from "../src";

describe("FunctionRef + makeFunctionRef()", () => {
  it("makeFunctionRef returns a tagged record { moduleName, exportName }", () => {
    const ref = makeFunctionRef<{ id: string }, string>("admin/cleanup", "default");
    expect(ref.moduleName).toBe("admin/cleanup");
    expect(ref.exportName).toBe("default");
    expect(isFunctionRef(ref)).toBe(true);
  });

  it("makeFunctionRef rejects empty / non-string module or export names", () => {
    expect(() => makeFunctionRef("", "x")).toThrow(/moduleName/);
    expect(() => makeFunctionRef("m", "")).toThrow(/exportName/);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => makeFunctionRef(42 as any, "x")).toThrow(/moduleName/);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => makeFunctionRef("m", 42 as any)).toThrow(/exportName/);
  });

  it("isFunctionRef returns false for non-refs", () => {
    expect(isFunctionRef(null)).toBe(false);
    expect(isFunctionRef(undefined)).toBe(false);
    expect(isFunctionRef({})).toBe(false);
    expect(isFunctionRef({ moduleName: 1, exportName: "x" })).toBe(false);
    expect(isFunctionRef({ moduleName: "m", exportName: 2 })).toBe(false);
    expect(isFunctionRef({ moduleName: "", exportName: "x" })).toBe(false);
    expect(isFunctionRef({ moduleName: "m", exportName: "" })).toBe(false);
  });

  it("argument and result types flow through the typed ref", () => {
    type Args = { id: string };
    type Result = number;
    const ref: FunctionRef<Args, Result> = makeFunctionRef<Args, Result>("m", "x");
    // Compile-time only — confirms the typed slots survive.
    const _typecheck: { args: Args; result: Result } = {
      args: {} as Args,
      result: {} as Result,
    };
    void _typecheck;
    expect(ref).toBeTruthy();
  });
});

describe("Ctx surface (type-level)", () => {
  it("QueryCtx exposes runQuery only (compile-time guarded)", () => {
    // The presence of these properties is asserted with a structural cast.
    // `runMutation` and `runAction` are intentionally absent from QueryCtx
    // so that calling them inside a query handler is a type error.
    const stub: QueryCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: async <TArgs, TResult>(
        _ref: FunctionRef<TArgs, TResult>,
        _args: TArgs,
      ): Promise<TResult> => null as unknown as TResult,
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
      },
    };
    expect(typeof stub.runQuery).toBe("function");
    // @ts-expect-error — QueryCtx must not expose runMutation
    void stub.runMutation;
    // @ts-expect-error — QueryCtx must not expose runAction
    void stub.runAction;
  });

  it("MutationCtx exposes runQuery + runMutation (no runAction)", () => {
    const stub: MutationCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: async () => null as never,
      runMutation: async () => null as never,
      scheduler: {
        runAfter: async () => "id" as never,
        runAt: async () => "id" as never,
        cancel: async () => undefined,
      },
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
        generateUploadUrl: async () => ({ url: "", storageId: "", uploadId: "" }),
        completeUpload: async () => "" as never,
        store: async () => "sid" as never,
        delete: async () => undefined,
      },
    };
    expect(typeof stub.runQuery).toBe("function");
    expect(typeof stub.runMutation).toBe("function");
    // @ts-expect-error — mutations must not invoke actions (no transactional contract)
    void stub.runAction;
  });

  it("ActionCtx exposes runQuery + runMutation + runAction", () => {
    const stub: ActionCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: async () => null as never,
      runMutation: async () => null as never,
      runAction: async () => null as never,
      scheduler: {
        runAfter: async () => "id" as never,
        runAt: async () => "id" as never,
        cancel: async () => undefined,
      },
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
        generateUploadUrl: async () => ({ url: "", storageId: "", uploadId: "" }),
        completeUpload: async () => "" as never,
        store: async () => "sid" as never,
        delete: async () => undefined,
      },
    };
    expect(typeof stub.runQuery).toBe("function");
    expect(typeof stub.runMutation).toBe("function");
    expect(typeof stub.runAction).toBe("function");
  });
});

describe("runQuery / runMutation / runAction — invocation shape", () => {
  it("runX accepts a typed FunctionRef and args, returning the awaited result", async () => {
    const ref = makeFunctionRef<{ x: number }, number>("math", "double");
    // Cast the generic runX members through `unknown` — jest.fn cannot
    // express the generic `<TArgs, TResult>(...)` shape natively, but the
    // shape is checked at the call site via the FunctionRef phantom types.
    const runQuerySpy = jest.fn(
      async (_r: FunctionRef<unknown, unknown>, args: unknown) =>
        (args as { x: number }).x * 2,
    );
    const ctx: MutationCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: runQuerySpy as unknown as MutationCtx["runQuery"],
      runMutation: (async () => null) as unknown as MutationCtx["runMutation"],
      scheduler: {
        runAfter: async () => "id" as never,
        runAt: async () => "id" as never,
        cancel: async () => undefined,
      },
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
        generateUploadUrl: async () => ({ url: "", storageId: "", uploadId: "" }),
        completeUpload: async () => "" as never,
        store: async () => "sid" as never,
        delete: async () => undefined,
      },
    };
    const out = await ctx.runQuery(ref, { x: 21 });
    expect(out).toBe(42);
    expect(runQuerySpy).toHaveBeenCalledWith(ref, { x: 21 });
  });

  it("an ActionCtx handler can chain mutation calls via runMutation", async () => {
    const mref = makeFunctionRef<{ name: string }, { id: string }>("users", "create");
    const runMutationSpy = jest.fn(
      async (_r: FunctionRef<unknown, unknown>, args: unknown) => ({
        id: "u_" + (args as { name: string }).name,
      }),
    );
    const ctx: ActionCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: (async () => null) as unknown as ActionCtx["runQuery"],
      runMutation: runMutationSpy as unknown as ActionCtx["runMutation"],
      runAction: (async () => null) as unknown as ActionCtx["runAction"],
      scheduler: {
        runAfter: async () => "id" as never,
        runAt: async () => "id" as never,
        cancel: async () => undefined,
      },
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
        generateUploadUrl: async () => ({ url: "", storageId: "", uploadId: "" }),
        completeUpload: async () => "" as never,
        store: async () => "sid" as never,
        delete: async () => undefined,
      },
    };
    // Use validator to mirror real usage with zod args.
    const argsSchema = z.object({ name: z.string() });
    const args = argsSchema.parse({ name: "ada" });
    const result = await ctx.runMutation(mref, args);
    expect(result).toEqual({ id: "u_ada" });
    expect(runMutationSpy).toHaveBeenCalled();
  });
});

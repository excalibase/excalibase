import { z } from "zod";

import {
  internalQuery,
  internalMutation,
  internalAction,
  isFunctionDef,
  isInternalFn,
  query,
  mutation,
  action,
  getFunctionKind,
} from "../src";

describe("internalQuery() / internalMutation() / internalAction()", () => {
  it("internalQuery returns a kind=query def tagged isInternal=true", () => {
    const def = internalQuery({
      args: z.object({ id: z.string() }),
      handler: async (_ctx, args) => args.id,
    });
    expect(def.kind).toBe("query");
    expect(def.isInternal).toBe(true);
    expect(typeof def.handler).toBe("function");
  });

  it("internalMutation returns a kind=mutation def tagged isInternal=true", () => {
    const def = internalMutation({
      args: z.object({ n: z.number() }),
      handler: async (_ctx, args) => args.n,
    });
    expect(def.kind).toBe("mutation");
    expect(def.isInternal).toBe(true);
  });

  it("internalAction returns a kind=action def tagged isInternal=true", () => {
    const def = internalAction({
      args: z.object({}),
      handler: async () => null,
    });
    expect(def.kind).toBe("action");
    expect(def.isInternal).toBe(true);
  });

  it("public query/mutation/action defs are NOT tagged isInternal", () => {
    const q = query({ args: z.object({}), handler: async () => 1 });
    const m = mutation({ args: z.object({}), handler: async () => 1 });
    const a = action({ args: z.object({}), handler: async () => 1 });
    expect(q.isInternal).toBeFalsy();
    expect(m.isInternal).toBeFalsy();
    expect(a.isInternal).toBeFalsy();
  });
});

describe("isInternalFn()", () => {
  it("returns true for internalX defs and false for public defs", () => {
    expect(
      isInternalFn(internalQuery({ args: z.object({}), handler: async () => 1 })),
    ).toBe(true);
    expect(
      isInternalFn(internalMutation({ args: z.object({}), handler: async () => 1 })),
    ).toBe(true);
    expect(
      isInternalFn(internalAction({ args: z.object({}), handler: async () => 1 })),
    ).toBe(true);

    expect(isInternalFn(query({ args: z.object({}), handler: async () => 1 }))).toBe(false);
    expect(isInternalFn(mutation({ args: z.object({}), handler: async () => 1 }))).toBe(false);
    expect(isInternalFn(action({ args: z.object({}), handler: async () => 1 }))).toBe(false);
  });

  it("returns false for non-function-def inputs", () => {
    expect(isInternalFn(null)).toBe(false);
    expect(isInternalFn(undefined)).toBe(false);
    expect(isInternalFn(42)).toBe(false);
    expect(isInternalFn({})).toBe(false);
    expect(isInternalFn({ kind: "query", isInternal: true })).toBe(false);
  });
});

describe("isFunctionDef accepts internal defs too", () => {
  it("isFunctionDef returns true for internalX outputs", () => {
    expect(
      isFunctionDef(internalQuery({ args: z.object({}), handler: async () => 1 })),
    ).toBe(true);
    expect(
      isFunctionDef(internalMutation({ args: z.object({}), handler: async () => 1 })),
    ).toBe(true);
    expect(
      isFunctionDef(internalAction({ args: z.object({}), handler: async () => 1 })),
    ).toBe(true);
  });

  it("getFunctionKind still reports the underlying kind for internal defs", () => {
    expect(
      getFunctionKind(internalQuery({ args: z.object({}), handler: async () => 1 })),
    ).toBe("query");
    expect(
      getFunctionKind(internalMutation({ args: z.object({}), handler: async () => 1 })),
    ).toBe("mutation");
    expect(
      getFunctionKind(internalAction({ args: z.object({}), handler: async () => 1 })),
    ).toBe("action");
  });
});

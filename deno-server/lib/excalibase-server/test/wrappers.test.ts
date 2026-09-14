import { z } from "zod";
import { zodToJsonSchema } from "zod-to-json-schema";

import {
  query,
  mutation,
  action,
  isFunctionDef,
  getFunctionKind,
  getArgsJsonSchema,
} from "../src";

describe("query()", () => {
  it("returns an inert record tagged kind=query", () => {
    const argsSchema = z.object({ n: z.number() });
    const handler = jest.fn(async (_ctx: unknown, args: { n: number }) => args.n + 1);

    const def = query({ args: argsSchema, handler });

    expect(def.kind).toBe("query");
    expect(def.args).toBe(argsSchema);
    expect(def.handler).toBe(handler);
    expect(handler).not.toHaveBeenCalled();
  });

  it("stashes a JSON Schema copy of args under __metadata.argsJsonSchema", () => {
    const argsSchema = z.object({ n: z.number() });
    const def = query({ args: argsSchema, handler: async () => 1 });
    const expected = zodToJsonSchema(argsSchema);
    expect(def.__metadata.argsJsonSchema).toEqual(expected);
  });

  it("does not invoke the handler at definition time", () => {
    const handler = jest.fn(async () => "never-called");
    query({ args: z.object({}), handler });
    expect(handler).not.toHaveBeenCalled();
  });
});

describe("mutation()", () => {
  it("returns an inert record tagged kind=mutation", () => {
    const argsSchema = z.object({ name: z.string() });
    const handler = jest.fn(async (_ctx: unknown, args: { name: string }) => args.name);

    const def = mutation({ args: argsSchema, handler });

    expect(def.kind).toBe("mutation");
    expect(def.args).toBe(argsSchema);
    expect(def.handler).toBe(handler);
    expect(handler).not.toHaveBeenCalled();
  });

  it("attaches argsJsonSchema metadata", () => {
    const argsSchema = z.object({ name: z.string() });
    const def = mutation({ args: argsSchema, handler: async () => null });
    expect(def.__metadata.argsJsonSchema).toEqual(zodToJsonSchema(argsSchema));
  });
});

describe("action()", () => {
  it("returns an inert record tagged kind=action", () => {
    const argsSchema = z.object({ url: z.string().url() });
    const handler = jest.fn(async (_ctx: unknown, args: { url: string }) => args.url);

    const def = action({ args: argsSchema, handler });

    expect(def.kind).toBe("action");
    expect(def.args).toBe(argsSchema);
    expect(def.handler).toBe(handler);
    expect(handler).not.toHaveBeenCalled();
  });

  it("attaches argsJsonSchema metadata", () => {
    const argsSchema = z.object({ url: z.string() });
    const def = action({ args: argsSchema, handler: async () => null });
    expect(def.__metadata.argsJsonSchema).toEqual(zodToJsonSchema(argsSchema));
  });
});

describe("codegen-meta helpers", () => {
  it("isFunctionDef returns true for query/mutation/action defs", () => {
    expect(isFunctionDef(query({ args: z.object({}), handler: async () => 1 }))).toBe(true);
    expect(isFunctionDef(mutation({ args: z.object({}), handler: async () => 1 }))).toBe(true);
    expect(isFunctionDef(action({ args: z.object({}), handler: async () => 1 }))).toBe(true);
  });

  it("isFunctionDef returns false for non-defs", () => {
    expect(isFunctionDef(null)).toBe(false);
    expect(isFunctionDef(undefined)).toBe(false);
    expect(isFunctionDef(42)).toBe(false);
    expect(isFunctionDef("string")).toBe(false);
    expect(isFunctionDef({})).toBe(false);
    expect(isFunctionDef({ kind: "bogus" })).toBe(false);
    expect(isFunctionDef({ kind: "query" })).toBe(false);
  });

  it("isFunctionDef rejects objects missing args", () => {
    expect(
      isFunctionDef({
        kind: "query",
        handler: () => undefined,
        __metadata: { argsJsonSchema: {} },
      }),
    ).toBe(false);
    expect(
      isFunctionDef({
        kind: "query",
        args: null,
        handler: () => undefined,
        __metadata: { argsJsonSchema: {} },
      }),
    ).toBe(false);
  });

  it("isFunctionDef rejects objects with non-function handler", () => {
    expect(
      isFunctionDef({
        kind: "query",
        args: {},
        handler: "not-a-function",
        __metadata: { argsJsonSchema: {} },
      }),
    ).toBe(false);
  });

  it("isFunctionDef rejects objects with malformed __metadata", () => {
    expect(
      isFunctionDef({
        kind: "query",
        args: {},
        handler: () => undefined,
        __metadata: null,
      }),
    ).toBe(false);
    expect(
      isFunctionDef({
        kind: "query",
        args: {},
        handler: () => undefined,
      }),
    ).toBe(false);
    expect(
      isFunctionDef({
        kind: "query",
        args: {},
        handler: () => undefined,
        __metadata: "not-an-object",
      }),
    ).toBe(false);
    expect(
      isFunctionDef({
        kind: "query",
        args: {},
        handler: () => undefined,
        __metadata: {},
      }),
    ).toBe(false);
  });

  it("getFunctionKind returns the stored kind", () => {
    expect(getFunctionKind(query({ args: z.object({}), handler: async () => 1 }))).toBe("query");
    expect(getFunctionKind(mutation({ args: z.object({}), handler: async () => 1 }))).toBe(
      "mutation",
    );
    expect(getFunctionKind(action({ args: z.object({}), handler: async () => 1 }))).toBe("action");
  });

  it("getArgsJsonSchema returns the stashed JSON Schema", () => {
    const argsSchema = z.object({ id: z.string() });
    const def = query({ args: argsSchema, handler: async () => null });
    expect(getArgsJsonSchema(def)).toEqual(zodToJsonSchema(argsSchema));
  });
});

describe("runtime-style invocation", () => {
  it("a runtime can call def.handler(ctx, parsedArgs) to execute (mutation ctx)", async () => {
    const def = mutation({
      args: z.object({ x: z.number() }),
      handler: async (ctx, args) => ({ hasDb: ctx.db !== null, doubled: args.x * 2 }),
    });

    // MutationCtx carries auth + runX + scheduler + storage surfaces; db
    // is null. Stub the minimum.
    const fakeCtx = {
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
        generateUploadUrl: async () => "",
        store: async () => "sid" as never,
        delete: async () => undefined,
      },
    } as const;
    const result = await def.handler(fakeCtx, { x: 21 });
    expect(result).toEqual({ hasDb: false, doubled: 42 });
  });

  it("an action handler receives ctx.db === null", async () => {
    const def = action({
      args: z.object({ x: z.number() }),
      handler: async (ctx, args) => ({ dbIsNull: ctx.db === null, value: args.x }),
    });
    const fakeCtx = {
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
        generateUploadUrl: async () => "",
        store: async () => "sid" as never,
        delete: async () => undefined,
      },
    } as const;
    const result = await def.handler(fakeCtx, { x: 5 });
    expect(result).toEqual({ dbIsNull: true, value: 5 });
  });
});

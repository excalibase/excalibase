import { httpAction, isHttpAction, isFunctionDef } from "../src";

describe("httpAction()", () => {
  it("returns a tagged record { kind: 'httpAction', handler, __metadata }", () => {
    const handler = jest.fn(async (_ctx: unknown, _req: Request) => new Response("ok"));
    const def = httpAction(handler);
    expect(def.kind).toBe("httpAction");
    expect(def.handler).toBe(handler);
    expect(def.__metadata).toBeDefined();
    expect(handler).not.toHaveBeenCalled();
  });

  it("does not require an args validator (raw Request goes straight to the handler)", () => {
    const def = httpAction(async () => new Response(null, { status: 204 }));
    // Validator is null/undefined — distinguishes from query/mutation/action.
    expect("args" in def === false || def.args === undefined || def.args === null).toBe(true);
  });

  it("isHttpAction returns true for httpAction defs and false for everything else", () => {
    const def = httpAction(async () => new Response("ok"));
    expect(isHttpAction(def)).toBe(true);
    expect(isHttpAction(null)).toBe(false);
    expect(isHttpAction(undefined)).toBe(false);
    expect(isHttpAction({ kind: "query" })).toBe(false);
    expect(isHttpAction({ kind: "httpAction" })).toBe(false); // missing handler
  });

  it("isFunctionDef accepts httpAction defs (codegen + runtime scan reach them)", () => {
    const def = httpAction(async () => new Response("ok"));
    expect(isFunctionDef(def)).toBe(true);
  });

  it("a runtime can invoke the handler with (ctx, Request) and get a Response", async () => {
    const def = httpAction(async (ctx, req) => {
      const body = await req.text();
      return new Response(JSON.stringify({ echo: body, dbIsNull: ctx.db === null }), {
        status: 201,
        headers: { "content-type": "application/json" },
      });
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
        generateUploadUrl: async () => ({ url: "", storageId: "", uploadId: "" }),
        completeUpload: async () => "" as never,
        store: async () => "sid" as never,
        delete: async () => undefined,
      },
    };
    const req = new Request("http://x.local/hook", { method: "POST", body: "hello" });
    const res = await def.handler(fakeCtx, req);
    expect(res.status).toBe(201);
    const payload = (await res.json()) as { echo: string; dbIsNull: boolean };
    expect(payload.echo).toBe("hello");
    expect(payload.dbIsNull).toBe(true);
  });
});

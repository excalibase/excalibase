// Win 1 — Phase 12: typed `ctx.auth.getUserIdentity()`.
//
// documented `UserIdentity` surface
// (https://docs..dev/api/interfaces/server.UserIdentity). The library
// only ships the type; the provisioning Deno runtime wires the actual
// implementation onto ctx.auth so handler code can call it.
//
// These tests are mostly compile-time. A handful of runtime structural
// assertions stand in so Jest has something to count and so the typed
// contract doesn't silently regress.

import type {
  ActionCtx,
  Ctx,
  MutationCtx,
  QueryCtx,
  UserIdentity,
} from "../src";

describe("UserIdentity / ctx.auth.getUserIdentity (Phase 12)", () => {
  it("UserIdentity carries -shape required fields", () => {
    const id: UserIdentity = {
      tokenIdentifier: "https://issuer|abc",
      subject: "abc",
      issuer: "https://issuer",
    };
    expect(id.tokenIdentifier).toBe("https://issuer|abc");
    expect(id.subject).toBe("abc");
    expect(id.issuer).toBe("https://issuer");
  });

  it("UserIdentity exposes every  optional claim slot", () => {
    // Compile-time: all of these assignments must type-check or the
    // surface drifted from . Runtime values just keep Jest happy.
    const id: UserIdentity = {
      tokenIdentifier: "i|s",
      subject: "s",
      issuer: "i",
      name: "Alice",
      email: "a@b",
      emailVerified: true,
      phoneNumber: "+1",
      phoneNumberVerified: false,
      pictureUrl: "https://p",
      givenName: "A",
      familyName: "B",
      nickname: "al",
      preferredUsername: "alice",
      profileUrl: "https://p/alice",
      updatedAt: "2026-01-01T00:00:00Z",
      birthday: "1990-01-01",
      gender: "n/a",
      language: "en",
      timezone: "UTC",
    };
    expect(id.email).toBe("a@b");
    expect(id.emailVerified).toBe(true);
    expect(id.phoneNumberVerified).toBe(false);
  });

  it("UserIdentity has an index signature so custom claims pass through", () => {
    const id: UserIdentity = {
      tokenIdentifier: "i|s",
      subject: "s",
      issuer: "i",
      // Arbitrary custom claim must be assignable via the index signature.
      tenant: "acme",
      roles: ["admin", "viewer"],
      "custom:scope": "rw",
    };
    expect(id.tenant).toBe("acme");
    expect(id["custom:scope"]).toBe("rw");
  });

  it("QueryCtx.auth.getUserIdentity returns Promise<UserIdentity | null>", async () => {
    // Build a structural QueryCtx — the real runtime wires this; this
    // test verifies the type surface accepts the expected shape.
    const qctx: QueryCtx = {
      db: null,
      auth: {
        claims: null,
        getUserIdentity: async () => null,
      },
      runQuery: async () => null as never,
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
      },
    };
    const id = await qctx.auth.getUserIdentity();
    expect(id).toBeNull();
  });

  it("MutationCtx and ActionCtx also expose getUserIdentity", async () => {
    const mctx: MutationCtx = {
      db: null,
      auth: {
        claims: { sub: "u1", iss: "https://x" },
        getUserIdentity: async () => ({
          tokenIdentifier: "https://x|u1",
          subject: "u1",
          issuer: "https://x",
        }),
      },
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
    };
    const m = await mctx.auth.getUserIdentity();
    expect(m?.subject).toBe("u1");

    const actx: ActionCtx = {
      db: null,
      auth: {
        claims: null,
        getUserIdentity: async () => null,
      },
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
    };
    const a = await actx.auth.getUserIdentity();
    expect(a).toBeNull();
  });

  it("Ctx union exposes auth.getUserIdentity uniformly", async () => {
    const useCtx = async (ctx: Ctx): Promise<UserIdentity | null> => {
      return ctx.auth.getUserIdentity();
    };
    // Action variant satisfies Ctx.
    const actx: ActionCtx = {
      db: null,
      auth: {
        claims: null,
        getUserIdentity: async () => ({
          tokenIdentifier: "i|s",
          subject: "s",
          issuer: "i",
        }),
      },
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
    };
    const out = await useCtx(actx);
    expect(out?.subject).toBe("s");
  });

  it("auth.claims is still exposed (backwards compatibility)", () => {
    // The original `claims` property must remain so existing Phase 0–11
    // callers keep compiling. Adding getUserIdentity is purely additive.
    const qctx: QueryCtx = {
      db: null,
      auth: {
        claims: { sub: "abc", custom: "anything" },
        getUserIdentity: async () => null,
      },
      runQuery: async () => null as never,
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
      },
    };
    expect(qctx.auth.claims?.sub).toBe("abc");
    expect(qctx.auth.claims?.custom).toBe("anything");
  });
});

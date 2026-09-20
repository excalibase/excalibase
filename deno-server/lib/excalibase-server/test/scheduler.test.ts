/**
 * Phase 8 — type-level + runtime tests for `ctx.scheduler`.
 *
 * The library does not implement the scheduler; the runtime supplies a real
 * `Scheduler` object on the mutation and action ctx. These tests pin the
 * type surface and the contract used by the runtime:
 *
 *  * `Scheduler.runAfter(delayMs, fnRef, args): Promise<ScheduledId>`
 *  * `Scheduler.runAt(timestamp, fnRef, args): Promise<ScheduledId>`
 *  * `Scheduler.cancel(id): Promise<void>`
 *
 * The `scheduler` slot lives on `MutationCtx` and `ActionCtx` only — calling
 * the scheduler from a query would violate the read-only contract (queries
 * must be side-effect free), so `QueryCtx` deliberately does NOT carry it.
 */

import {
  makeFunctionRef,
  type ActionCtx,
  type FunctionRef,
  type MutationCtx,
  type QueryCtx,
  type ScheduledId,
  type Scheduler,
} from "../src";

describe("Scheduler surface (type-level)", () => {
  it("QueryCtx does NOT expose scheduler (queries are read-only)", () => {
    const stub: QueryCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: async () => null as never,
      storage: {
        getUrl: async () => null,
        get: async () => null,
        getMetadata: async () => null,
      },
    };
    // @ts-expect-error — QueryCtx must not carry scheduler
    void stub.scheduler;
  });

  it("MutationCtx exposes scheduler with runAfter / runAt / cancel", () => {
    const stub: MutationCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: async () => null as never,
      runMutation: async () => null as never,
      scheduler: {
        runAfter: async () => "abc" as ScheduledId,
        runAt: async () => "def" as ScheduledId,
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
    expect(typeof stub.scheduler.runAfter).toBe("function");
    expect(typeof stub.scheduler.runAt).toBe("function");
    expect(typeof stub.scheduler.cancel).toBe("function");
  });

  it("ActionCtx exposes scheduler with runAfter / runAt / cancel", () => {
    const stub: ActionCtx = {
      db: null,
      auth: { claims: null, getUserIdentity: async () => null },
      runQuery: async () => null as never,
      runMutation: async () => null as never,
      runAction: async () => null as never,
      scheduler: {
        runAfter: async () => "xyz" as ScheduledId,
        runAt: async () => "uvw" as ScheduledId,
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
    expect(typeof stub.scheduler.runAfter).toBe("function");
    expect(typeof stub.scheduler.runAt).toBe("function");
    expect(typeof stub.scheduler.cancel).toBe("function");
  });
});

describe("Scheduler — invocation shape", () => {
  it("runAfter accepts (delayMs, FunctionRef, args) and resolves to a ScheduledId", async () => {
    const ref = makeFunctionRef<{ to: string }, void>("jobs", "sendEmail");
    const scheduler: Scheduler = {
      runAfter: async (
        delayMs: number,
        fnRef: FunctionRef<unknown, unknown>,
        args: unknown,
      ): Promise<ScheduledId> => {
        expect(delayMs).toBe(60_000);
        expect(fnRef).toEqual(ref);
        expect(args).toEqual({ to: "ada@example.com" });
        return "sch_01" as ScheduledId;
      },
      runAt: async () => "" as ScheduledId,
      cancel: async () => undefined,
    };
    const id = await scheduler.runAfter(60_000, ref, { to: "ada@example.com" });
    expect(id).toBe("sch_01");
  });

  it("runAt accepts (timestamp, FunctionRef, args) and resolves to a ScheduledId", async () => {
    const ref = makeFunctionRef<{ at: number }, void>("jobs", "ping");
    const scheduler: Scheduler = {
      runAfter: async () => "" as ScheduledId,
      runAt: async (
        ts: number,
        fnRef: FunctionRef<unknown, unknown>,
        args: unknown,
      ): Promise<ScheduledId> => {
        expect(ts).toBe(1_800_000_000_000);
        expect(fnRef).toEqual(ref);
        expect(args).toEqual({ at: 1_800_000_000_000 });
        return "sch_02" as ScheduledId;
      },
      cancel: async () => undefined,
    };
    const id = await scheduler.runAt(1_800_000_000_000, ref, { at: 1_800_000_000_000 });
    expect(id).toBe("sch_02");
  });

  it("cancel(id) resolves to void", async () => {
    let cancelled = "";
    const scheduler: Scheduler = {
      runAfter: async () => "" as ScheduledId,
      runAt: async () => "" as ScheduledId,
      cancel: async (id: ScheduledId): Promise<void> => {
        cancelled = id;
      },
    };
    await scheduler.cancel("sch_03" as ScheduledId);
    expect(cancelled).toBe("sch_03");
  });

  it("ScheduledId is a string subtype (brand-friendly, but assignable from string)", () => {
    // The runtime emits a 30-char base32 token; the type stays a string so
    // existing utilities (logging, serialisation) work without translation.
    const id: ScheduledId = "abcde12345abcde12345abcde12345" as ScheduledId;
    expect(typeof id).toBe("string");
    expect(id.length).toBe(30);
  });
});

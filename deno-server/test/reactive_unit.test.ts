// Phase 9b.A — In-process unit tests for SubscriptionRegistry and the
// WS handler URL-routing surface. The five integration tests in
// reactive_*.test.ts cover the end-to-end happy paths via a subprocess
// runtime; this file exercises the modules directly so coverage stays
// above the 80% gate even though the runtime is sandboxed.

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { SubscriptionRegistry, type ReactiveRuntime, type CommitEvent } from "../runtime/reactive.ts";
import { decodeJwtClaims, extractProjectIdFromWatchPath, handleFrame, tryUpgradeWatchSocket, WS_SUBPROTOCOL, type FrameContext } from "../runtime/ws_handler.ts";

interface FakeWs {
  sent: string[];
  closed: boolean;
  send: (s: string) => void;
  isClosed: () => boolean;
}

function fakeWs(): FakeWs {
  const w: FakeWs = {
    sent: [],
    closed: false,
    send: (s: string) => { w.sent.push(s); },
    isClosed: () => w.closed,
  };
  return w;
}

function fakeRuntime(handler: (id: string, args: unknown) =>
  Promise<{ status: number; data: unknown; error?: string; reads: Set<string> }>): ReactiveRuntime {
  return { invokeWithReads: handler };
}

Deno.test("SubscriptionRegistry.register sends initial result and stores deps", async () => {
  const rt = fakeRuntime(async (_id, _args) => ({
    status: 200, data: [{ x: 1 }], reads: new Set(["posts"]),
  }));
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s1", { moduleName: "list", exportName: "default" }, {}, "p1", null);
  assertEquals(ws.sent.length, 1);
  const sent = JSON.parse(ws.sent[0]);
  assertEquals(sent.op, "result");
  assertEquals(sent.subId, "s1");
  assertEquals(sent.data, [{ x: 1 }]);
  assertEquals(reg.size, 1);
});

Deno.test("SubscriptionRegistry.register surfaces invoke errors as {op:'error'} frames", async () => {
  const rt = fakeRuntime(async () => ({
    status: 500, data: null, error: "boom", reads: new Set(),
  }));
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s1", { moduleName: "x", exportName: "default" }, {}, "p1", null);
  assertEquals(ws.sent.length, 1);
  const sent = JSON.parse(ws.sent[0]);
  assertEquals(sent.op, "error");
  assertEquals(sent.code, "INVOKE_FAILED");
  assertEquals(reg.size, 0);
});

Deno.test("SubscriptionRegistry.register catches thrown errors with INTERNAL code", async () => {
  const rt = fakeRuntime(async () => { throw new Error("kaboom"); });
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s1", { moduleName: "x", exportName: "default" }, {}, "p1", null);
  const sent = JSON.parse(ws.sent[0]);
  assertEquals(sent.op, "error");
  assertEquals(sent.code, "INTERNAL");
  assertEquals(reg.size, 0);
});

Deno.test("dispatchCommit only re-executes subs whose deps intersect the event", async () => {
  let runs = 0;
  let returnData: unknown = [];
  const rt = fakeRuntime(async (id) => {
    runs++;
    // listPosts reads "posts", listAudit reads "audit".
    const reads = id.endsWith("listPosts") ? new Set(["posts"]) : new Set(["audit"]);
    return { status: 200, data: returnData, reads };
  });
  const reg = new SubscriptionRegistry(rt);
  const wsA = fakeWs();
  const wsB = fakeWs();
  await reg.register(wsA, "a", { moduleName: "listPosts", exportName: "default" }, {}, "p1", null);
  await reg.register(wsB, "b", { moduleName: "listAudit", exportName: "default" }, {}, "p1", null);
  assertEquals(runs, 2);

  // Commit on "posts" — only wsA should be re-executed and pushed.
  returnData = [{ id: 1 }];
  const event: CommitEvent = {
    projectId: "p1", fnId: "create", runtimeId: "p1__create",
    deps: ["posts"], ts: Date.now(),
  };
  reg.dispatchCommit(event);
  // Wait for the async re-execution to settle.
  await delay(100);
  assertEquals(runs, 3, "only one re-execution fired");
  // wsA: initial + push = 2 messages; wsB: initial = 1.
  assertEquals(wsA.sent.length, 2);
  assertEquals(wsB.sent.length, 1);
});

Deno.test("dispatchCommit isolates per-project subscriptions", async () => {
  let runs = 0;
  const rt = fakeRuntime(async () => {
    runs++;
    return { status: 200, data: [], reads: new Set(["x"]) };
  });
  const reg = new SubscriptionRegistry(rt);
  const ws1 = fakeWs();
  const ws2 = fakeWs();
  await reg.register(ws1, "s", { moduleName: "list", exportName: "default" }, {}, "proj_a", null);
  await reg.register(ws2, "s", { moduleName: "list", exportName: "default" }, {}, "proj_b", null);

  reg.dispatchCommit({
    projectId: "proj_a", fnId: "f", runtimeId: "proj_a__f",
    deps: ["x"], ts: Date.now(),
  });
  await delay(100);
  // Only proj_a's sub re-runs.
  assertEquals(runs, 3); // 2 initial + 1 push
});

Deno.test("dispatchCommit does NOT push when result hash is unchanged", async () => {
  const rt = fakeRuntime(async () => ({
    status: 200, data: [{ a: 1 }], reads: new Set(["x"]),
  }));
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s", { moduleName: "f", exportName: "default" }, {}, "p", null);
  assertEquals(ws.sent.length, 1);
  reg.dispatchCommit({ projectId: "p", fnId: "x", runtimeId: "p__x", deps: ["x"], ts: 0 });
  await delay(100);
  // Re-executed but same data — no second push.
  assertEquals(ws.sent.length, 1);
});

Deno.test("cleanupConn removes all subscriptions for a ws", async () => {
  const rt = fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(["x"]),
  }));
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s1", { moduleName: "f", exportName: "default" }, {}, "p", null);
  await reg.register(ws, "s2", { moduleName: "f", exportName: "default" }, {}, "p", null);
  assertEquals(reg.size, 2);
  reg.cleanupConn(ws);
  assertEquals(reg.size, 0);
  // dispatchCommit after cleanup must not throw.
  reg.dispatchCommit({ projectId: "p", fnId: "x", runtimeId: "p__x", deps: ["x"], ts: 0 });
});

Deno.test("unregister drops a single sub but leaves siblings", async () => {
  const rt = fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(["x"]),
  }));
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s1", { moduleName: "f", exportName: "default" }, {}, "p", null);
  await reg.register(ws, "s2", { moduleName: "f", exportName: "default" }, {}, "p", null);
  reg.unregister(ws, "s1");
  assertEquals(reg.size, 1);
});

Deno.test("unregister on unknown conn is a no-op", () => {
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  const ws = fakeWs();
  reg.unregister(ws, "ghost"); // never threw
  assertEquals(reg.size, 0);
});

Deno.test("dispatchCommit with empty deps array is a no-op", async () => {
  let runs = 0;
  const rt = fakeRuntime(async () => {
    runs++;
    return { status: 200, data: [], reads: new Set(["x"]) };
  });
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s", { moduleName: "f", exportName: "default" }, {}, "p", null);
  reg.dispatchCommit({ projectId: "p", fnId: "f", runtimeId: "p__f", deps: [], ts: 0 });
  await delay(50);
  assertEquals(runs, 1, "initial only — no commit triggered re-run");
});

Deno.test("coalesce: bursts of commits collapse via pendingRerun flag", async () => {
  let runs = 0;
  const rt = fakeRuntime(async () => {
    runs++;
    await delay(50); // make re-execution slow so we can pile on commits.
    return { status: 200, data: [{ run: runs }], reads: new Set(["x"]) };
  });
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s", { moduleName: "f", exportName: "default" }, {}, "p", null);
  // Fire 10 commits during the in-flight initial run? Initial is already
  // done. Fire bursts now.
  for (let i = 0; i < 10; i++) {
    reg.dispatchCommit({ projectId: "p", fnId: "f", runtimeId: "p__f", deps: ["x"], ts: i });
  }
  await delay(400);
  // 1 initial + at most 2 reruns (one in-flight + one queued via
  // pendingRerun). Should be much fewer than 11.
  if (runs < 2) throw new Error("expected ≥2 runs (initial + at least one rerun)");
  if (runs > 4) throw new Error(`coalesce too lax: ${runs} runs for 10 commits`);
});

Deno.test("dead conn before re-execution: no push attempted", async () => {
  const rt = fakeRuntime(async () => ({
    status: 200, data: [{ x: 1 }], reads: new Set(["x"]),
  }));
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s", { moduleName: "f", exportName: "default" }, {}, "p", null);
  // Close conn — registry hasn't cleaned up yet, but isClosed() returns true.
  ws.closed = true;
  reg.dispatchCommit({ projectId: "p", fnId: "f", runtimeId: "p__f", deps: ["x"], ts: 0 });
  await delay(100);
  // Initial result was sent before close — that's expected.
  assertEquals(ws.sent.length, 1);
});

Deno.test("rerun reports invoke error as {op:'error',code:'RERUN_FAILED'}", async () => {
  let first = true;
  const rt = fakeRuntime(async () => {
    if (first) {
      first = false;
      return { status: 200, data: [], reads: new Set(["x"]) };
    }
    return { status: 500, data: null, error: "boom", reads: new Set() };
  });
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s", { moduleName: "f", exportName: "default" }, {}, "p", null);
  reg.dispatchCommit({ projectId: "p", fnId: "f", runtimeId: "p__f", deps: ["x"], ts: 0 });
  await delay(100);
  const last = JSON.parse(ws.sent[ws.sent.length - 1]);
  assertEquals(last.op, "error");
  assertEquals(last.code, "RERUN_FAILED");
});

Deno.test("rerun catches thrown errors from the runtime", async () => {
  let first = true;
  const rt = fakeRuntime(async () => {
    if (first) {
      first = false;
      return { status: 200, data: [], reads: new Set(["x"]) };
    }
    throw new Error("kaboom");
  });
  const reg = new SubscriptionRegistry(rt);
  const ws = fakeWs();
  await reg.register(ws, "s", { moduleName: "f", exportName: "default" }, {}, "p", null);
  reg.dispatchCommit({ projectId: "p", fnId: "f", runtimeId: "p__f", deps: ["x"], ts: 0 });
  await delay(100);
  const last = JSON.parse(ws.sent[ws.sent.length - 1]);
  assertEquals(last.op, "error");
  assertEquals(last.code, "RERUN_FAILED");
});

Deno.test("getLastCommit returns the most recent CommitEvent", async () => {
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  assertEquals(reg.getLastCommit(), null);
  const ev: CommitEvent = { projectId: "p", fnId: "f", runtimeId: "p__f", deps: ["x"], ts: 1 };
  reg.dispatchCommit(ev);
  assertEquals(reg.getLastCommit(), ev);
});

// --- ws_handler URL parsing surface -----------------------------------

Deno.test("extractProjectIdFromWatchPath matches the canonical pattern", () => {
  assertEquals(extractProjectIdFromWatchPath("/functions/v1/proj_abc/_watch"), "proj_abc");
  assertEquals(extractProjectIdFromWatchPath("/functions/v1/proj-1/_watch"), "proj-1");
  assertEquals(extractProjectIdFromWatchPath("/functions/v1//_watch"), null);
  assertEquals(extractProjectIdFromWatchPath("/functions/v1/proj/other"), null);
  assertEquals(extractProjectIdFromWatchPath("/health"), null);
});

Deno.test("tryUpgradeWatchSocket returns null for non-watch URLs", () => {
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  const req = new Request("http://x/health");
  assertEquals(tryUpgradeWatchSocket(req, reg), null);
});

Deno.test("tryUpgradeWatchSocket rejects non-GET methods with 405", () => {
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  const req = new Request("http://x/functions/v1/p/_watch", { method: "POST" });
  const res = tryUpgradeWatchSocket(req, reg);
  assertExists(res);
  assertEquals(res!.status, 405);
});

Deno.test("tryUpgradeWatchSocket rejects missing Upgrade header with 426", () => {
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  const req = new Request("http://x/functions/v1/p/_watch");
  const res = tryUpgradeWatchSocket(req, reg);
  assertExists(res);
  assertEquals(res!.status, 426);
});

Deno.test("tryUpgradeWatchSocket rejects wrong subprotocol with 400", () => {
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  const req = new Request("http://x/functions/v1/p/_watch", {
    headers: { upgrade: "websocket", "sec-websocket-protocol": "wrong-one" },
  });
  const res = tryUpgradeWatchSocket(req, reg);
  assertExists(res);
  assertEquals(res!.status, 400);
});

Deno.test("WS_SUBPROTOCOL is the canonical excalibase-fn-v1 string", () => {
  assertEquals(WS_SUBPROTOCOL, "excalibase-fn-v1");
});

// --- decodeJwtClaims ---------------------------------------------------

function makeJwt(claims: Record<string, unknown>): string {
  const enc = (obj: unknown) =>
    btoa(JSON.stringify(obj))
      .replace(/=+$/, "")
      .replace(/\+/g, "-")
      .replace(/\//g, "_");
  const header = enc({ alg: "ES256", typ: "JWT" });
  return `${header}.${enc(claims)}.sig`;
}

Deno.test("decodeJwtClaims returns the payload object on a well-formed token", () => {
  const claims = { sub: "u1", role: "admin" };
  const out = decodeJwtClaims(makeJwt(claims));
  assertExists(out);
  assertEquals(out!.sub, "u1");
  assertEquals(out!.role, "admin");
});

Deno.test("decodeJwtClaims returns null on empty/missing token", () => {
  assertEquals(decodeJwtClaims(""), null);
  // deno-lint-ignore no-explicit-any
  assertEquals(decodeJwtClaims(undefined as any), null);
});

Deno.test("decodeJwtClaims returns null on malformed token (wrong segment count)", () => {
  assertEquals(decodeJwtClaims("only.two"), null);
  assertEquals(decodeJwtClaims("a.b.c.d"), null);
});

Deno.test("decodeJwtClaims returns null when payload is not valid base64url JSON", () => {
  assertEquals(decodeJwtClaims("header.!@#bad.sig"), null);
  // Pad-length 1 isn't valid base64url either.
  assertEquals(decodeJwtClaims("a.x.y"), null);
});

Deno.test("decodeJwtClaims handles all valid base64url pad lengths", () => {
  // 1 byte → 4 chars after padding "=="
  const oneByte = decodeJwtClaims(`h.${btoa('"a"').replace(/=+$/, "")}.s`);
  // 2 bytes → 3 chars padded "==" or "=" depending on input.
  // We just confirm none of these throw.
  void oneByte;
  const twoByte = decodeJwtClaims(`h.${btoa('{"a":1}').replace(/=+$/, "")}.s`);
  assertExists(twoByte);
  assertEquals(twoByte!.a, 1);
});

// --- handleFrame -------------------------------------------------------

function fakeFrameCtx(): { ctx: FrameContext; sent: string[]; pongTs: { v: number } } {
  const sent: string[] = [];
  const pongTs = { v: 0 };
  const ctx: FrameContext = {
    wsLike: { send: (s) => sent.push(s), isClosed: () => false },
    setPong: (ts) => { pongTs.v = ts; },
    projectId: "p",
    claims: null,
  };
  return { ctx, sent, pongTs };
}

Deno.test("handleFrame ping → pong", async () => {
  const { ctx, sent } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  await handleFrame(JSON.stringify({ op: "ping" }), ctx, reg);
  assertEquals(sent.length, 1);
  assertEquals(JSON.parse(sent[0]), { op: "pong" });
});

Deno.test("handleFrame pong updates lastPong", async () => {
  const { ctx, pongTs } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  await handleFrame(JSON.stringify({ op: "pong" }), ctx, reg);
  if (pongTs.v === 0) throw new Error("expected lastPong to be set");
});

Deno.test("handleFrame subscribe registers and pushes initial result", async () => {
  const { ctx, sent } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [{ x: 1 }], reads: new Set(["x"]),
  })));
  await handleFrame(JSON.stringify({
    op: "subscribe", subId: "s", ref: { moduleName: "f", exportName: "default" }, args: {},
  }), ctx, reg);
  assertEquals(reg.size, 1);
  const sentJson = JSON.parse(sent[0]);
  assertEquals(sentJson.op, "result");
});

Deno.test("handleFrame subscribe with missing subId throws", async () => {
  const { ctx } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  let threw = false;
  try {
    await handleFrame(JSON.stringify({
      op: "subscribe", ref: { moduleName: "f", exportName: "default" },
    }), ctx, reg);
  } catch (_) { threw = true; }
  assertEquals(threw, true);
});

Deno.test("handleFrame unsubscribe removes the sub", async () => {
  const { ctx } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  await handleFrame(JSON.stringify({
    op: "subscribe", subId: "s", ref: { moduleName: "f", exportName: "default" }, args: {},
  }), ctx, reg);
  assertEquals(reg.size, 1);
  await handleFrame(JSON.stringify({ op: "unsubscribe", subId: "s" }), ctx, reg);
  assertEquals(reg.size, 0);
});

Deno.test("handleFrame unknown op throws", async () => {
  const { ctx } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  let threw = false;
  try {
    await handleFrame(JSON.stringify({ op: "wat" }), ctx, reg);
  } catch (_) { threw = true; }
  assertEquals(threw, true);
});

Deno.test("handleFrame invalid JSON throws", async () => {
  const { ctx } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  let threw = false;
  try {
    await handleFrame("not json", ctx, reg);
  } catch (_) { threw = true; }
  assertEquals(threw, true);
});

Deno.test("handleFrame non-object frame throws", async () => {
  const { ctx } = fakeFrameCtx();
  const reg = new SubscriptionRegistry(fakeRuntime(async () => ({
    status: 200, data: [], reads: new Set(),
  })));
  let threw = false;
  try {
    await handleFrame("123", ctx, reg);
  } catch (_) { threw = true; }
  assertEquals(threw, true);
});

// Phase 9b.B — Unit tests for the NatsBridge. The integration suite
// (reactive_nats*.test.ts) covers happy-path end-to-end. This file
// exercises the internal branches that are hard to hit through real
// NATS: malformed envelopes, dedupe TTL eviction, publish errors,
// connect retry on transient failure, and the stop() path.
//
// We construct the bridge with `connectFn` overrides so no real NATS
// client is imported.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { NatsBridge } from "../runtime/nats.ts";
import type { CommitEvent, SubscriptionRegistry } from "../runtime/reactive.ts";

function fakeRegistry(): { dispatched: CommitEvent[]; reg: Pick<SubscriptionRegistry, "dispatchCommit"> } {
  const dispatched: CommitEvent[] = [];
  return {
    dispatched,
    reg: { dispatchCommit: (e: CommitEvent) => { dispatched.push(e); } },
  };
}

function fakeConnect(behaviour: "ok" | "throw-then-ok" | "throw"): {
  fn: (url: string) => Promise<{
    publish: (subject: string, data: Uint8Array) => void;
    subscribe: (subject: string) => AsyncIterable<{ data: Uint8Array }>;
    close: () => Promise<void>;
    closed: () => Promise<void | Error>;
  }>;
  calls: number;
  published: Array<{ subject: string; data: Uint8Array }>;
  push: (data: Uint8Array) => void;
  endSub: () => void;
  publishError: (err: Error) => void;
} {
  let calls = 0;
  let publishErr: Error | null = null;
  const published: Array<{ subject: string; data: Uint8Array }> = [];
  // Manual async iterator we can push messages into.
  const queue: Array<Uint8Array | null> = [];
  let resolveWaiter: (() => void) | null = null;
  const push = (data: Uint8Array) => {
    queue.push(data);
    if (resolveWaiter) { resolveWaiter(); resolveWaiter = null; }
  };
  const endSub = () => {
    queue.push(null);
    if (resolveWaiter) { resolveWaiter(); resolveWaiter = null; }
  };
  const subscribe = (_subject: string): AsyncIterable<{ data: Uint8Array }> => ({
    [Symbol.asyncIterator]() {
      return {
        async next(): Promise<IteratorResult<{ data: Uint8Array }>> {
          while (true) {
            if (queue.length > 0) {
              const head = queue.shift();
              if (head === null) return { value: undefined, done: true } as IteratorResult<{ data: Uint8Array }>;
              return { value: { data: head as Uint8Array }, done: false };
            }
            await new Promise<void>((res) => { resolveWaiter = res; });
          }
        },
      };
    },
  });
  const fn = async (_url: string) => {
    calls++;
    if (behaviour === "throw" || (behaviour === "throw-then-ok" && calls === 1)) {
      throw new Error("simulated connect failure");
    }
    return {
      publish: (subject: string, data: Uint8Array) => {
        if (publishErr) throw publishErr;
        published.push({ subject, data });
      },
      subscribe,
      close: () => Promise.resolve(),
      closed: () => new Promise<void>(() => { /* never */ }),
    };
  };
  return {
    fn,
    get calls() { return calls; },
    published,
    push,
    endSub,
    publishError: (err: Error) => { publishErr = err; },
  };
}

function makeEvent(over: Partial<CommitEvent> = {}): CommitEvent {
  return {
    projectId: over.projectId ?? "p1",
    fnId: over.fnId ?? "fn",
    runtimeId: over.runtimeId ?? "p1__fn",
    deps: over.deps ?? ["posts"],
    ts: over.ts ?? Date.now(),
  };
}

Deno.test("NatsBridge.start succeeds and isConnected flips true", async () => {
  const { reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  await b.start();
  assertEquals(b.isConnected(), true);
  await b.stop();
  assertEquals(b.isConnected(), false);
});

Deno.test("NatsBridge.start retries on transient connect failure", async () => {
  const { reg } = fakeRegistry();
  const connect = fakeConnect("throw-then-ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
    baseDelayMs: 10,
    maxDelayMs: 20,
  });
  await b.start();
  assertEquals(b.isConnected(), true);
  assertEquals(connect.calls >= 2, true);
  await b.stop();
});

Deno.test("NatsBridge.publish serialises onto the project-scoped subject", async () => {
  const { reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  await b.start();
  const ev = makeEvent({ projectId: "pX" });
  await b.publish(ev);
  assertEquals(connect.published.length, 1);
  assertEquals(connect.published[0].subject, "excalibase.fn.pX.commits");
  const decoded = JSON.parse(new TextDecoder().decode(connect.published[0].data));
  assertEquals(decoded.senderId, "me");
  assertEquals(decoded.event.projectId, "pX");
  await b.stop();
});

Deno.test("NatsBridge.publish before connect is a silent no-op", async () => {
  const { reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  // Don't call start().
  await b.publish(makeEvent());
  assertEquals(connect.published.length, 0);
});

Deno.test("NatsBridge.publish surfaces underlying socket error", async () => {
  const { reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  await b.start();
  connect.publishError(new Error("socket dead"));
  let threw = false;
  try { await b.publish(makeEvent()); } catch (_) { threw = true; }
  assertEquals(threw, true);
  await b.stop();
});

Deno.test("NatsBridge.handleIncoming dedupes self-published messages by senderId", () => {
  const { dispatched, reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  const envelope = { senderId: "me", event: makeEvent() };
  b.handleIncoming(new TextEncoder().encode(JSON.stringify(envelope)));
  assertEquals(dispatched.length, 0, "self-published must be dropped");
});

Deno.test("NatsBridge.handleIncoming dispatches non-self messages", () => {
  const { dispatched, reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  const envelope = { senderId: "other", event: makeEvent({ projectId: "px" }) };
  b.handleIncoming(new TextEncoder().encode(JSON.stringify(envelope)));
  assertEquals(dispatched.length, 1);
  assertEquals(dispatched[0].projectId, "px");
});

Deno.test("NatsBridge.handleIncoming dedupes by (runtimeId, ts) within TTL", () => {
  const { dispatched, reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
    dedupeTtlMs: 5000,
  });
  const fixedTs = 12345;
  const env = { senderId: "other", event: makeEvent({ ts: fixedTs }) };
  const bytes = new TextEncoder().encode(JSON.stringify(env));
  b.handleIncoming(bytes);
  b.handleIncoming(bytes);
  b.handleIncoming(bytes);
  assertEquals(dispatched.length, 1, "duplicate events must be dropped");
  assertEquals(b.dedupeSize(), 1);
});

Deno.test("NatsBridge.handleIncoming drops malformed JSON", () => {
  const { dispatched, reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  b.handleIncoming(new TextEncoder().encode("not-json"));
  assertEquals(dispatched.length, 0);
});

Deno.test("NatsBridge.handleIncoming drops envelopes missing required fields", () => {
  const { dispatched, reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  b.handleIncoming(new TextEncoder().encode(JSON.stringify({ senderId: "other" })));
  b.handleIncoming(new TextEncoder().encode(JSON.stringify({ event: makeEvent() })));
  assertEquals(dispatched.length, 0);
});

Deno.test("NatsBridge dedupe set evicts entries older than TTL", async () => {
  const { reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
    dedupeTtlMs: 50,
  });
  const env = { senderId: "other", event: makeEvent({ ts: 1 }) };
  b.handleIncoming(new TextEncoder().encode(JSON.stringify(env)));
  assertEquals(b.dedupeSize(), 1);
  await new Promise((r) => setTimeout(r, 100));
  // Trigger gc by handling a different event.
  const env2 = { senderId: "other", event: makeEvent({ ts: 2 }) };
  b.handleIncoming(new TextEncoder().encode(JSON.stringify(env2)));
  // Old entry should have been evicted; only the new one remains.
  assertEquals(b.dedupeSize(), 1);
});

Deno.test("NatsBridge.attachCommitEmitter wires publish to the supplied register", async () => {
  const { reg } = fakeRegistry();
  const connect = fakeConnect("ok");
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: connect.fn,
  });
  await b.start();
  let handler: ((e: CommitEvent) => void) | null = null;
  b.attachCommitEmitter((h) => { handler = h; });
  if (handler) (handler as (e: CommitEvent) => void)(makeEvent({ projectId: "p9" }));
  // Wait microtask for the publish promise to resolve.
  await new Promise((r) => setTimeout(r, 10));
  assertEquals(connect.published.length, 1);
  assertEquals(connect.published[0].subject, "excalibase.fn.p9.commits");
  await b.stop();
});

Deno.test("NatsBridge.stop drains and closes; second stop is a no-op", async () => {
  const { reg } = fakeRegistry();
  let drained = 0;
  let closed = 0;
  // Minimal stub conn — subscribe returns an immediately-done iterator,
  // drain/close count invocations.
  const stubConn = {
    publish: () => {},
    subscribe: () => ({
      [Symbol.asyncIterator]() {
        return { next: () => Promise.resolve({ value: undefined, done: true as const }) };
      },
    } as AsyncIterable<{ data: Uint8Array }>),
    drain: () => { drained++; return Promise.resolve(); },
    close: () => { closed++; return Promise.resolve(); },
    closed: () => new Promise<void>(() => {}),
  };
  const b = new NatsBridge({
    url: "nats://fake",
    runtimeId: "me",
    registry: reg,
    connectFn: () => Promise.resolve(stubConn),
  });
  await b.start();
  await b.stop();
  await b.stop();
  assertEquals(drained, 1);
  assertEquals(closed, 0);
});

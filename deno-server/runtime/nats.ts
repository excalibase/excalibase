// Phase 9b.B — NATS bridge: cross-replica fan-out for the reactive subsystem.
//
// What this file does:
//   1. PUBLISHER — `runtime.onCommit` fires a CommitEvent per successful
//      mutation commit (Phase 9b.A). When `EXCALIBASE_NATS_URL` is set,
//      we serialise that event as JSON onto subject
//      `excalibase.fn.<projectId>.commits`.
//   2. SUBSCRIBER — we subscribe to the wildcard `excalibase.fn.*.commits`
//      and call `reactiveRegistry.dispatchCommit(event)` for every
//      received event whose `runtimeId` is NOT ours (self-dedupe). The
//      dedupe key is `${runtimeId}:${ts}`; we keep a Set of recent keys
//      and evict entries older than 5 seconds so the set can't grow
//      unbounded under sustained traffic.
//
// What this file does NOT do:
//   - Buffer outbound events during a NATS disconnect. Commits that fire
//     while disconnected are LOST cross-replica (local subs on the
//     publishing replica still see them — that path doesn't touch NATS).
//     This is acceptable for v1: reactive subscriptions are an eventually
//     consistent UX hint, not a transactional log. Persistence + replay
//     are out of scope until JetStream is wired in a later phase.
//   - Authenticate to NATS. The cluster is assumed to be inside the
//     trust boundary (k8s NetworkPolicy + private VPC). When TLS/auth
//     are required, the `url` argument can carry credentials
//     (`nats://user:pass@host`) — the npm:nats client honours that.
//
// Hard invariant: if `EXCALIBASE_NATS_URL` is unset at process start, the
// bridge is never constructed and the runtime behaves exactly like 9b.A
// (single-replica reactive). server.ts enforces this — if you change the
// wiring there, keep this invariant.

import type { CommitEvent, SubscriptionRegistry } from "./reactive.ts";

/**
 * Subset of npm:nats's `NatsConnection` we actually use. Declared as a
 * structural type so tests can fake it without pulling the full npm
 * import shape. The real client returned by `connect()` satisfies all
 * three methods.
 */
interface NatsConnectionLike {
  publish: (subject: string, data: Uint8Array) => void;
  subscribe: (subject: string) => AsyncIterable<{ data: Uint8Array }>;
  close: () => Promise<void>;
  drain?: () => Promise<void>;
  closed: () => Promise<void | Error>;
}

/**
 * Factory shape so `NatsBridge` doesn't import npm:nats at the module
 * top level. The default factory (used in production) imports lazily
 * inside `start()`; tests can pass a fake to avoid a real network call.
 */
type ConnectFn = (url: string) => Promise<NatsConnectionLike>;

export interface NatsBridgeOptions {
  /** NATS server URL — `nats://host:port` (or comma-separated cluster). */
  url: string;
  /**
   * Identifies this runtime instance. Stamped on every outbound
   * CommitEvent.runtimeId? Actually no — the CommitEvent already carries
   * a `runtimeId` that is per-FUNCTION (`${projectId}__${fnId}`). To
   * dedupe self-published messages we need a PROCESS-level id, which we
   * stamp into a separate envelope field on the wire. See `WireEnvelope`.
   */
  runtimeId: string;
  /** Receiver for dispatched events. */
  registry: Pick<SubscriptionRegistry, "dispatchCommit">;
  /** Optional injection point for tests; defaults to the npm:nats import. */
  connectFn?: ConnectFn;
  /** Base reconnect delay in ms; defaults to 1000. Capped at `maxDelayMs`. */
  baseDelayMs?: number;
  /** Reconnect delay cap in ms; defaults to 30000. */
  maxDelayMs?: number;
  /** Dedupe TTL in ms; defaults to 5000. */
  dedupeTtlMs?: number;
}

/**
 * Wire envelope for `excalibase.fn.<projectId>.commits`. The payload is
 * the CommitEvent plus a top-level `senderId` so receivers can drop
 * self-published messages without trusting the in-event `runtimeId`
 * (which is per-function, not per-process).
 */
interface WireEnvelope {
  senderId: string;
  event: CommitEvent;
}

/**
 * Default connection factory — lazy-imports `npm:nats@2` so test files
 * that never start a bridge don't pay the dependency cost.
 *
 * Reconnect behaviour: we DELEGATE to nats.js's built-in auto-reconnect
 * (its defaults are 1s wait, infinite retries). On reconnect, the
 * wildcard subscription resumes automatically because the AsyncIterable
 * keeps yielding once the conn comes back. If a future nats.js version
 * changes this, we fall back to the manual reconnect loop in `start()`.
 */
async function defaultConnect(url: string): Promise<NatsConnectionLike> {
  // deno-lint-ignore no-explicit-any
  const mod: any = await import("npm:nats@2");
  return await mod.connect({
    servers: url,
    reconnect: true,
    waitOnFirstConnect: false,
    reconnectTimeWait: 1000,
    maxReconnectAttempts: -1, // infinite
  }) as NatsConnectionLike;
}

export class NatsBridge {
  private readonly url: string;
  private readonly runtimeId: string;
  private readonly registry: Pick<SubscriptionRegistry, "dispatchCommit">;
  private readonly connectFn: ConnectFn;
  private readonly baseDelayMs: number;
  private readonly maxDelayMs: number;
  private readonly dedupeTtlMs: number;

  private conn: NatsConnectionLike | null = null;
  private starting = false;
  private stopped = false;
  /**
   * Recent `${runtimeId}:${ts}` keys. We GC by walking on every
   * received message and dropping entries older than `dedupeTtlMs`.
   * Memory bound: O(throughput × dedupeTtlMs / 1000) — a 1000 commits/s
   * replica with default 5s TTL holds 5_000 strings (~few hundred KB).
   */
  private readonly dedupe = new Map<string, number>();
  private encoder = new TextEncoder();
  private decoder = new TextDecoder();
  /** Set true once `start()` finishes its first successful connect. */
  private connected = false;

  constructor(opts: NatsBridgeOptions) {
    this.url = opts.url;
    this.runtimeId = opts.runtimeId;
    this.registry = opts.registry;
    this.connectFn = opts.connectFn ?? defaultConnect;
    this.baseDelayMs = opts.baseDelayMs ?? 1000;
    this.maxDelayMs = opts.maxDelayMs ?? 30_000;
    this.dedupeTtlMs = opts.dedupeTtlMs ?? 5_000;
  }

  /**
   * Attach the bridge to a CommitEvent emitter. The caller (server.ts)
   * forwards every local commit to `publish` so the bridge can serialise
   * onto NATS. Failures (during disconnect) are logged and dropped —
   * cross-replica is best-effort.
   */
  attachCommitEmitter(register: (h: (e: CommitEvent) => void) => void): void {
    register((event) => {
      this.publish(event).catch((err) => {
        console.warn(`[nats] publish failed: ${err instanceof Error ? err.message : err}`);
      });
    });
  }

  /**
   * Connect to NATS and start consuming the wildcard subscription. If
   * the initial connect fails, we retry with exponential backoff capped
   * at `maxDelayMs`. Returns once the first connect succeeds OR `stop()`
   * is called (whichever first).
   *
   * After the initial connect, the npm:nats client handles reconnect on
   * its own. The subscription async-iterable resumes automatically.
   */
  async start(): Promise<void> {
    if (this.starting || this.connected) return;
    this.starting = true;
    let attempt = 0;
    while (!this.stopped) {
      try {
        this.conn = await this.connectFn(this.url);
        this.connected = true;
        this.starting = false;
        // Kick off the subscriber loop. It runs until the conn is
        // closed; if the conn drops, the iterable ends and we log.
        this.runSubscriber().catch((err) => {
          console.warn(`[nats] subscriber loop ended: ${err instanceof Error ? err.message : err}`);
        });
        // Also surface unexpected close so ops can see it in logs.
        this.conn.closed().then((err) => {
          if (err) console.warn(`[nats] connection closed with error: ${err}`);
        }).catch(() => { /* ignore */ });
        return;
      } catch (err) {
        attempt++;
        const delay = Math.min(
          this.maxDelayMs,
          this.baseDelayMs * Math.pow(2, Math.min(10, attempt - 1)),
        );
        console.warn(`[nats] connect attempt ${attempt} failed: ${err instanceof Error ? err.message : err} (retry in ${delay}ms)`);
        await sleep(delay);
      }
    }
    this.starting = false;
  }

  /**
   * Publish a CommitEvent onto `excalibase.fn.<projectId>.commits`. The
   * envelope wraps the event with `senderId` so receivers can dedupe
   * self-published messages.
   *
   * Drops silently if not yet connected. Failures (e.g. conn dropped
   * mid-publish) are surfaced to the caller via thrown error.
   */
  async publish(event: CommitEvent): Promise<void> {
    if (!this.conn || !this.connected) return;
    const envelope: WireEnvelope = { senderId: this.runtimeId, event };
    const subject = `excalibase.fn.${event.projectId}.commits`;
    const data = this.encoder.encode(JSON.stringify(envelope));
    // nats.js publish is sync-ish but the underlying socket write can
    // throw on a half-closed conn. Wrap so attachCommitEmitter sees a
    // proper Promise rejection.
    try {
      this.conn.publish(subject, data);
    } catch (err) {
      throw err instanceof Error ? err : new Error(String(err));
    }
  }

  /**
   * Stop the bridge cleanly. Safe to call multiple times.
   */
  async stop(): Promise<void> {
    this.stopped = true;
    const conn = this.conn;
    this.conn = null;
    this.connected = false;
    if (!conn) return;
    try {
      if (conn.drain) await conn.drain();
      else await conn.close();
    } catch (_) { /* ignore — best-effort */ }
  }

  // ---------------------------------------------------------------------
  // Internals
  // ---------------------------------------------------------------------

  private async runSubscriber(): Promise<void> {
    if (!this.conn) return;
    const sub = this.conn.subscribe("excalibase.fn.*.commits");
    for await (const msg of sub) {
      this.handleIncoming(msg.data);
      if (this.stopped) break;
    }
  }

  /** Visible for unit tests — decode one wire message and dispatch. */
  handleIncoming(data: Uint8Array): void {
    let env: WireEnvelope | null = null;
    try {
      const parsed = JSON.parse(this.decoder.decode(data));
      if (parsed && typeof parsed === "object") env = parsed as WireEnvelope;
    } catch (err) {
      console.warn(`[nats] dropped malformed message: ${err instanceof Error ? err.message : err}`);
      return;
    }
    if (!env || !env.event || typeof env.senderId !== "string") {
      console.warn(`[nats] dropped message with missing envelope fields`);
      return;
    }
    // Self-dedupe: same process — drop. This is the primary defence.
    if (env.senderId === this.runtimeId) return;
    // Replay defence: NATS at-most-once for core pub/sub, but a future
    // JetStream switch could redeliver. Dedupe by (runtimeId, ts) within
    // the TTL window.
    const key = `${env.event.runtimeId}:${env.event.ts}`;
    this.gcDedupe();
    if (this.dedupe.has(key)) return;
    this.dedupe.set(key, Date.now());
    this.registry.dispatchCommit(env.event);
  }

  private gcDedupe(): void {
    const cutoff = Date.now() - this.dedupeTtlMs;
    for (const [k, t] of this.dedupe) {
      if (t < cutoff) this.dedupe.delete(k);
    }
  }

  /** Test/observability surface — current connected state. */
  isConnected(): boolean { return this.connected; }
  /** Test surface — current dedupe-set size. */
  dedupeSize(): number { return this.dedupe.size; }
}

function sleep(ms: number): Promise<void> {
  return new Promise((res) => setTimeout(res, ms));
}

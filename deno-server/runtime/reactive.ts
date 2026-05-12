// Phase 9b.A — In-process reactive subscriptions.
//
// SubscriptionRegistry holds the set of live `subscribe`s for each WebSocket
// conn. When the commit emitter fires after a successful mutation, we walk
// every subscription whose `deps` intersects the commit's `deps` and
// re-execute the query function. If the result hash changed, we push a new
// `{op:"result"}` frame down the wire.
//
// Concurrency model:
//   - Per-sub mutex: only one re-execution runs per subscription at a time.
//     A new commit arriving mid-flight sets a `pendingRerun` flag; the
//     current re-execution checks it on completion and chains another run
//     if set. This coalesces bursts (10 commits → ≤10 pushes, ≥1 push).
//   - Connection-level cleanup: when a WS closes, every sub bound to it is
//     dropped, and any in-flight re-execution finalises but its result is
//     swallowed (no postMessage to a dead conn).
//
// What this file does NOT do:
//   - Cross-runtime fan-out (Phase 9b.B will publish CommitEvents to NATS so
//     other replicas pick them up). The emitter shape `{projectId, fnId,
//     runtimeId, deps, ts}` is forward-compatible with that publisher.
//   - SDK changes — Phase 9b.C will land the `.watch()` API and a typed
//     client. This module's protocol contract is documented in `ws_handler.ts`.

/**
 * Shape of the event the in-process emitter fires on each successful
 * mutation commit. Phase 9b.B will serialise this verbatim onto NATS.
 *
 *   projectId  — the script's project (parsed from `runtimeId` before the
 *                `__` separator), used to bucket subs by tenant.
 *   fnId       — the mutation function id that committed.
 *   runtimeId  — the full deploy id (`${projectId}__${fnId}`); useful for
 *                debug traces when multiple pods publish into NATS later.
 *   deps       — collection names INSERT/UPDATE/DELETEd in the txn. Reads
 *                MUST NOT appear here.
 *   ts         — millisecond commit timestamp (Date.now() at emit time).
 */
export interface CommitEvent {
  projectId: string;
  fnId: string;
  runtimeId: string;
  deps: string[];
  ts: number;
}

/**
 * Function ref shape over the WS protocol. The runtime resolves
 * `moduleName` against deployed scripts in the same project as
 * `${projectId}__${moduleName}`; `exportName` is kept in the envelope for
 * future fan-out where one bundle may declare multiple v2 exports.
 */
export interface FunctionRef {
  moduleName: string;
  exportName: string;
}

/**
 * Bridge between the WS handler and the FunctionRuntime. We keep this thin
 * so the registry can be unit-tested with a stub runtime, and so the
 * concrete `FunctionRuntime` doesn't need to import this module (avoiding
 * a circular dep — server.ts is the binding layer).
 */
export interface ReactiveRuntime {
  /**
   * Invoke a deployed function and return both its result envelope AND the
   * set of collections it read during execution. Used by `register` for
   * the initial result and by every subsequent re-execution.
   */
  invokeWithReads(id: string, body: unknown): Promise<{
    status: number;
    data: unknown;
    error?: string;
    reads: Set<string>;
  }>;
}

interface WsLike {
  /** Send a JSON-encoded text frame. Best-effort; no throw on dead conn. */
  send: (data: string) => void;
  /** True when the conn is no longer writable. */
  isClosed: () => boolean;
}

interface Subscription {
  wsConn: WsLike;
  subId: string;
  projectId: string;
  ref: FunctionRef;
  args: unknown;
  jwt: string | null;
  deps: Set<string>;
  lastResultHash: string;
  /**
   * Coalesce state. When a commit arrives while `inFlight` is true, we set
   * `pendingRerun` and return; the current run checks it on completion.
   */
  inFlight: boolean;
  pendingRerun: boolean;
}

/** SHA-256 hex digest of a JSON-encoded value. Used for change detection. */
async function hashValue(value: unknown): Promise<string> {
  const json = JSON.stringify(value);
  const enc = new TextEncoder().encode(json);
  const buf = await crypto.subtle.digest("SHA-256", enc);
  const bytes = new Uint8Array(buf);
  let hex = "";
  for (let i = 0; i < bytes.length; i++) {
    hex += bytes[i].toString(16).padStart(2, "0");
  }
  return hex;
}

export class SubscriptionRegistry {
  private readonly runtime: ReactiveRuntime;
  /**
   * subs are keyed by `${connKey}::${subId}` so the same subId can be
   * reused across distinct conns without collision. Each conn gets a
   * sequence-allocated key on first sight.
   */
  private readonly subs: Map<string, Subscription> = new Map();
  /** Map ws conn → connKey for cleanupConn. */
  private readonly connKeys: Map<WsLike, string> = new Map();
  private nextConnSeq = 1;
  /**
   * Phase 9b.A test surface — last CommitEvent received. Tests use
   * `/reactive/debug/last-commit` to assert deps shape. Not part of the
   * production API.
   */
  private lastCommit: CommitEvent | null = null;

  constructor(runtime: ReactiveRuntime) {
    this.runtime = runtime;
  }

  get size(): number {
    return this.subs.size;
  }

  getLastCommit(): CommitEvent | null {
    return this.lastCommit;
  }

  private keyFor(wsConn: WsLike): string {
    let k = this.connKeys.get(wsConn);
    if (!k) {
      k = `c${this.nextConnSeq++}`;
      this.connKeys.set(wsConn, k);
    }
    return k;
  }

  /**
   * Register a new subscription. Performs the initial invocation, captures
   * deps + hash, and sends back the first `{op:"result"}` frame.
   *
   * Errors during the initial invocation are surfaced as a single
   * `{op:"error", subId, code, message}` frame; the subscription is NOT
   * stored in that case (the SDK will retry by re-subscribing).
   */
  async register(
    wsConn: WsLike,
    subId: string,
    ref: FunctionRef,
    args: unknown,
    projectId: string,
    jwt: string | null,
  ): Promise<void> {
    const targetId = `${projectId}__${ref.moduleName}`;
    let invoke;
    try {
      invoke = await this.runtime.invokeWithReads(targetId, args);
    } catch (err) {
      this.sendError(wsConn, subId, "INTERNAL", err instanceof Error ? err.message : String(err));
      return;
    }
    if (invoke.status < 200 || invoke.status >= 300 || invoke.error) {
      this.sendError(wsConn, subId, "INVOKE_FAILED", invoke.error ?? `status ${invoke.status}`);
      return;
    }
    const data = invoke.data;
    const hash = await hashValue(data);
    const connKey = this.keyFor(wsConn);
    const key = `${connKey}::${subId}`;
    const sub: Subscription = {
      wsConn,
      subId,
      projectId,
      ref,
      args,
      jwt,
      // If the function did no reads at all (e.g. pure compute), fall back
      // to the explicit moduleName→collection hint of "no deps" so commits
      // never re-execute it. This is the safer default than overloading
      // "no deps" to "all deps".
      deps: invoke.reads,
      lastResultHash: hash,
      inFlight: false,
      pendingRerun: false,
    };
    this.subs.set(key, sub);
    this.send(wsConn, { op: "result", subId, data });
  }

  /**
   * Drop a subscription. No frame is sent on the wire — `unsubscribe` is
   * client-initiated, so the client already knows.
   */
  unregister(wsConn: WsLike, subId: string): void {
    const connKey = this.connKeys.get(wsConn);
    if (!connKey) return;
    this.subs.delete(`${connKey}::${subId}`);
  }

  /**
   * Drop every subscription for a conn. Called when the WS closes (either
   * cleanly or via error). In-flight re-executions complete but their
   * results are discarded because `wsConn.isClosed()` short-circuits the
   * push.
   */
  cleanupConn(wsConn: WsLike): void {
    const connKey = this.connKeys.get(wsConn);
    if (!connKey) return;
    const prefix = `${connKey}::`;
    for (const key of [...this.subs.keys()]) {
      if (key.startsWith(prefix)) this.subs.delete(key);
    }
    this.connKeys.delete(wsConn);
  }

  /**
   * Fan a CommitEvent out to every subscription whose `deps` intersects
   * `event.deps`. Re-execution is per-sub, mutex'd so a sub can never run
   * twice concurrently; bursts coalesce via `pendingRerun`.
   */
  dispatchCommit(event: CommitEvent): void {
    this.lastCommit = event;
    if (!Array.isArray(event.deps) || event.deps.length === 0) return;
    const depSet = new Set(event.deps);
    for (const sub of this.subs.values()) {
      if (sub.projectId !== event.projectId) continue;
      let intersects = false;
      for (const d of sub.deps) {
        if (depSet.has(d)) { intersects = true; break; }
      }
      if (!intersects) continue;
      this.scheduleRerun(sub);
    }
  }

  private scheduleRerun(sub: Subscription): void {
    if (sub.inFlight) {
      sub.pendingRerun = true;
      return;
    }
    sub.inFlight = true;
    // Fire and forget — the run loop swallows errors so a dead conn or
    // function-throw never crashes the registry.
    this.runRerunLoop(sub).catch((err) => {
      // Defensive — runRerunLoop is supposed to internalise all errors.
      console.warn(`[reactive] runRerunLoop swallowed error: ${err}`);
      sub.inFlight = false;
    });
  }

  private async runRerunLoop(sub: Subscription): Promise<void> {
    try {
      do {
        sub.pendingRerun = false;
        await this.executeOnce(sub);
      } while (sub.pendingRerun && !sub.wsConn.isClosed());
    } finally {
      sub.inFlight = false;
    }
  }

  private async executeOnce(sub: Subscription): Promise<void> {
    if (sub.wsConn.isClosed()) return;
    const targetId = `${sub.projectId}__${sub.ref.moduleName}`;
    let invoke;
    try {
      invoke = await this.runtime.invokeWithReads(targetId, sub.args);
    } catch (err) {
      this.sendError(sub.wsConn, sub.subId, "RERUN_FAILED",
        err instanceof Error ? err.message : String(err));
      return;
    }
    if (invoke.status < 200 || invoke.status >= 300 || invoke.error) {
      this.sendError(sub.wsConn, sub.subId, "RERUN_FAILED",
        invoke.error ?? `status ${invoke.status}`);
      return;
    }
    // Refresh dep set in case the query plan now reads different collections.
    // Defensive: a future schema migration might rename a collection; we
    // want the subscription to track wherever the function actually reads.
    sub.deps = invoke.reads;
    const hash = await hashValue(invoke.data);
    if (hash !== sub.lastResultHash) {
      sub.lastResultHash = hash;
      this.send(sub.wsConn, { op: "result", subId: sub.subId, data: invoke.data });
    }
  }

  private send(wsConn: WsLike, msg: Record<string, unknown>): void {
    if (wsConn.isClosed()) return;
    try {
      wsConn.send(JSON.stringify(msg));
    } catch (_) {
      // postMessage failed — conn likely transitioning. Best-effort.
    }
  }

  private sendError(wsConn: WsLike, subId: string, code: string, message: string): void {
    this.send(wsConn, { op: "error", subId, code, message });
  }
}

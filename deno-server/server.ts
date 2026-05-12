// Excalibase Deno edge-function runtime — Supabase-compatible shape.
//
// Protocol (platform → runtime):
//   POST /deploy        { id, code, secrets }        — register/replace a function
//   POST /invoke/{id}   InvokeRequest                — run a function, get InvokeResponse
//   DELETE /delete/{id}                              — unregister a function
//   GET /health                                      — liveness
//
// InvokeRequest = { method, url, headers, body }
// InvokeResponse = { status, headers, body }
//
// The worker's user code must export a default handler of shape
//   (req: Request) => Response | Promise<Response>
// The platform's bundler rewrites `export default X` into
// `globalThis.__excalibase_default = X` before sending, so the worker can
// just read it from globalThis after loading the module.
//
// Security: every request (except /health) requires X-Runtime-Secret header.
// Workers run with net restricted to ALLOWED_HOSTS (or disabled) and NO
// filesystem, env, run, ffi, or write permission. Secrets are injected as
// a Deno.env mock so user code sees only its own project's env vars.
//
// Phase 1 ctx.db: queries and mutations receive a DbClient facade in the
// worker that RPCs back to the main thread. The main thread owns the
// postgres.js pool (see runtime/pool.ts) and executes each op against it,
// posting the result back over postMessage. Actions still see ctx.db=null
// (Convex pattern). The worker itself never opens a network socket — all
// DB I/O happens here in the privileged main thread.
//
// Local @excalibase/server resolution: the worker template doesn't import
// @excalibase/server directly — Phase 1 contract types are defined in the
// runtime/* modules. The package lives at ../excalibase-server in the
// workspace; once the worker bundler is added (Phase 2/3) it will resolve
// via a `file:` spec rather than the npm registry.

import { executeDbOp, newCache } from "./runtime/db.ts";
import type { DbOp } from "./runtime/db.ts";
import { newId } from "./runtime/ids.ts";
import { closePool, getPool } from "./runtime/pool.ts";
import type { Sql } from "./runtime/pool.ts";
import type { ValidatorCache } from "./runtime/validator.ts";
import { SubscriptionRegistry, type CommitEvent } from "./runtime/reactive.ts";
import { tryUpgradeWatchSocket } from "./runtime/ws_handler.ts";

// ---------------------------------------------------------------------------
// Phase 8.5 — Shared mutation transaction map.
//
// Convex parity: when a mutation handler runs, every ctx.db.* and
// ctx.scheduler.* op it performs (and every op performed by mutations it
// invokes via ctx.runMutation) must ride the SAME Postgres transaction.
// On handler return, the txn commits; on throw/timeout, it rolls back.
//
// We assign each invocation a unique `txnRefId` and stash the connection
// in `txnMap`. The worker echoes `txnRefId` on every db/scheduler RPC so
// the main-thread dispatch consults the map and routes through the txn
// handle (or the pool, when no txn is active).
//
// For nested ctx.runMutation, the new invocation gets a fresh `txnRefId`
// whose ActiveTxn shares the parent's `sql` handle. `owned=false` on the
// alias entry means COMMIT/ROLLBACK is the parent's responsibility — the
// nested call just clears its alias when it returns.
//
// Actions DO NOT open a txn. ActionCtx.runMutation calls the mutation
// without forwarding a parent ref, so the mutation opens a fresh
// top-level txn (Convex parity: the action's call-boundary is the new
// top-level).
// ---------------------------------------------------------------------------

interface ActiveTxn {
  /**
   * Reserved Postgres connection with BEGIN already issued — or null for
   * tracking-only entries (query/action v2 invocations that don't open a
   * txn but still want their reads tracked for the reactive subscription
   * registry). `sqlFor()` falls back to the pool when this is null.
   */
  sql: Sql | null;
  /** True when this entry opened the txn — only owners commit/rollback. */
  owned: boolean;
  /** Non-empty for alias entries; carries the parent's txnRefId. */
  parentRefId?: string;
  /** Millisecond timestamp at which the txn was opened (or aliased). */
  openedAt: number;
  /**
   * Phase 9b.A: function kind that owns this entry — `"query"` or
   * `"mutation"`. Drives commit-event emission (only mutations emit) and
   * lets `finalize()` distinguish tracking-only entries from owning ones.
   * Empty for v1 fetch handlers / aliases that don't carry a kind hint.
   */
  kind: string;
  /**
   * Phase 9b.A: id-of-the-function-that-was-invoked. Used for the
   * CommitEvent.fnId payload so downstream consumers (NATS publisher in
   * Phase 9b.B, observability in 9b.E) know which mutation committed.
   */
  fnId: string;
  /**
   * Phase 9b.A: project id parsed from the runtime id at invocation time,
   * so the emitter never has to re-split. Used for CommitEvent.projectId
   * and for cross-tenant subscription isolation in the registry.
   */
  projectId: string;
  /**
   * Phase 9b.A: collections written to inside this txn (INSERT/UPDATE/
   * DELETE only). Captured BEFORE `txnMap.delete(...)` and shipped on the
   * emitted CommitEvent. Reads MUST NOT land here — the dispatch site is
   * the only place that adds.
   */
  writes: Set<string>;
  /**
   * Phase 9b.A: collections read inside this invocation (find / findOne /
   * getById / count / search / vectorSearch / query). Used by the
   * SubscriptionRegistry to learn which collections a query function
   * depends on. NOT shipped on CommitEvent.
   */
  reads: Set<string>;
  /**
   * Phase 9a: marked when a db/scheduler op fails with a retryable
   * SQLSTATE (40001/40P01) inside this txn. Owners check this on
   * finalize and signal the retry loop. Aliases never observe their
   * own conflict since they share the parent's connection.
   */
  conflict?: { sqlState: string; message: string };
}

// ---------------------------------------------------------------------------
// Phase 9b.A — In-process commit emitter.
//
// Single-listener fan-out is enough for this phase: the local
// SubscriptionRegistry is the only consumer. Phase 9b.B will add a NATS
// publisher; that publisher will subscribe via `runtime.onCommit` and
// serialise `CommitEvent` verbatim onto the subject
// `excalibase.fn.<projectId>.commits`. Keep CommitEvent JSON-stable.
//
// We swallow listener errors so a buggy subscriber can't take down the
// commit path — a failed listener means stale pushes, not lost commits.
// ---------------------------------------------------------------------------
type CommitListener = (e: CommitEvent) => void;
const commitListeners = new Set<CommitListener>();

function onCommit(handler: CommitListener): () => void {
  commitListeners.add(handler);
  return () => commitListeners.delete(handler);
}

function emitCommit(event: CommitEvent): void {
  for (const fn of commitListeners) {
    try { fn(event); }
    catch (err) { console.warn(`[runtime] commit listener threw:`, err); }
  }
}

/**
 * Phase 9a — internal-only sentinel thrown out of `invokeOnce` when the
 * mutation hit a retryable Postgres conflict (40001 or 40P01). The
 * outer `invoke` retry loop catches this and either retries or
 * surfaces a 409 ConflictError envelope to the caller.
 *
 * Never escapes the runtime; the HTTP layer never sees this class.
 */
class MutationConflict extends Error {
  readonly sqlState: string;
  readonly pgMessage: string;
  constructor(sqlState: string, pgMessage: string) {
    super(`mutation conflict ${sqlState}: ${pgMessage}`);
    this.name = "MutationConflict";
    this.sqlState = sqlState;
    this.pgMessage = pgMessage;
  }
}

const txnMap: Map<string, ActiveTxn> = new Map();

// nextTxnRefSeq generates a monotonically-increasing, process-unique key
// for each top-level/alias txn entry. Globally unique across all
// scripts; the `t` prefix makes a debug log line unambiguous.
let nextTxnRefSeq = 1;
function newTxnRefId(): string {
  const id = `t${nextTxnRefSeq.toString(36)}_${Date.now().toString(36)}`;
  nextTxnRefSeq++;
  return id;
}

/**
 * Open a new Postgres transaction by reserving a connection from the
 * singleton pool and issuing BEGIN. Returns the reserved sql handle —
 * caller must commit or rollback exactly once.
 *
 * Phase 9a: BEGIN now carries the configured isolation level
 * (EXCALIBASE_MUTATION_ISOLATION; default SERIALIZABLE) so SSI catches
 * read-write conflicts the way Convex's OCC catches its own conflicts.
 * Operators can drop to REPEATABLE READ or READ COMMITTED via env when
 * contention overhead is a documented and measured concern.
 */
async function openTxn(): Promise<Sql> {
  const pool = getPool();
  // deno-lint-ignore no-explicit-any
  const reserved = await (pool as any).reserve();
  // MUTATION_ISOLATION is validated at boot against ALLOWED_MUTATION_ISOLATIONS
  // so this template string can never carry attacker-controlled SQL.
  // deno-lint-ignore no-explicit-any
  await (reserved as any).unsafe(`BEGIN ISOLATION LEVEL ${MUTATION_ISOLATION}`);
  return reserved;
}

/**
 * Commit a txn opened by openTxn and release the underlying connection.
 *
 * Phase 9a: COMMIT itself can raise 40001 / 40P01 under SERIALIZABLE
 * (the famous "conflict surfaces at commit time" property of SSI). When
 * that happens we throw a `MutationConflict` so the outer retry loop
 * can route through rollback + backoff. All other commit failures are
 * left as plain Errors — they aren't retryable.
 */
async function commitTxn(sql: Sql): Promise<void> {
  try {
    // deno-lint-ignore no-explicit-any
    await (sql as any).unsafe("COMMIT");
  } catch (err) {
    const code = (err as { code?: unknown }).code;
    if (typeof code === "string" && RETRYABLE_SQLSTATES.has(code)) {
      const pgMsg = (err instanceof Error ? err.message : String(err)) || "commit conflict";
      throw new MutationConflict(code, pgMsg);
    }
    throw err;
  } finally {
    // Release the connection back to the pool. After a failed COMMIT
    // the session is in an aborted state — the pool will hand out a
    // fresh connection on the retry; we don't try to reuse this one.
    // Wrapping in try/catch since release() is a postgres.js extension
    // and a noop on plain Sql handles in some test paths.
    // deno-lint-ignore no-explicit-any
    try { (sql as any).release?.(); } catch (_) { /* ignore */ }
  }
}

/** Roll back a txn opened by openTxn and release the underlying connection. */
async function rollbackTxn(sql: Sql): Promise<void> {
  try {
    // deno-lint-ignore no-explicit-any
    await (sql as any).unsafe("ROLLBACK");
  } catch (_) {
    // Best-effort; if rollback fails the connection is destroyed on release
    // anyway. We don't want the rollback path to throw and mask the
    // user-handler error that triggered it.
  } finally {
    // deno-lint-ignore no-explicit-any
    try { (sql as any).release?.(); } catch (_) { /* ignore */ }
  }
}

/**
 * Return the Sql handle that the given txnRefId should use.
 * Falls back to the pool when the ref is unknown / undefined (e.g.
 * legacy v1 handlers, queries, actions).
 */
function sqlFor(txnRefId: string | undefined | null): Sql {
  if (txnRefId && txnMap.has(txnRefId)) {
    const entry = txnMap.get(txnRefId)!;
    // Phase 9b.A — tracking-only entries (queries/actions) have `sql:null`
    // because they never opened a connection. Fall back to the pool so the
    // db dispatch issues an autocommit read; the dispatcher still picks up
    // the entry for `reads` tracking on success.
    if (entry.sql) return entry.sql;
  }
  return getPool();
}

/**
 * Phase 9a: stamp a retryable SQLSTATE on the active txn entry so
 * finalize() / the retry loop knows to roll back and re-invoke. Walks
 * up alias chains so a conflict observed on a nested-mutation alias is
 * recorded on the actual owning txn (which is the one that will be
 * rolled back). Safe no-op when the ref is unknown.
 */
function markConflict(
  txnRefId: string | undefined | null,
  sqlState: string,
  message: string,
): void {
  if (!txnRefId) return;
  let cursor: string | undefined = txnRefId;
  // bound the walk so a corrupted parent chain can't loop forever.
  for (let i = 0; i < 16 && cursor; i++) {
    const entry = txnMap.get(cursor);
    if (!entry) return;
    if (entry.owned) {
      if (!entry.conflict) entry.conflict = { sqlState, message };
      return;
    }
    cursor = entry.parentRefId;
  }
}

interface DeployRequest {
  id: string;
  code: string;
  secrets?: Record<string, string>;
}

interface InvokeRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

interface InvokeResponse {
  status: number;
  headers: Record<string, string>;
  body: string;
}

interface PendingRequest {
  resolve: (r: InvokeResponse) => void;
  reject: (e: Error) => void;
  timeout: number;
}

interface LogEntry {
  level: string;
  msg: string;
  ts: number;
}

interface ScriptMetadata {
  id: string;
  worker: Worker;
  createdAt: Date;
  invocations: number;
  // pending tracks in-flight invocations keyed by request id so concurrent
  // calls to the same function don't overwrite each other's response handlers.
  pending: Map<number, PendingRequest>;
  nextReqId: number;
  // Ring buffer of recent user-code log lines. Capped at LOG_RING_SIZE.
  logs: LogEntry[];
  // Per-worker JSON Schema validator cache. Lives for the lifetime of the
  // deployed function; refreshed only when the function is redeployed.
  dbCache: ValidatorCache;
  // Phase 8 — recorded from the metadata callback. The main thread uses
  // this for runX read-only enforcement (query handlers can't call
  // mutations/actions) and for shared-transaction routing (mutation→
  // mutation rides the caller's txn). One of `"query"|"mutation"|"action"
  // |"httpAction"|"httpRouter"|""` (empty for v1 fetch handlers).
  kind: string;
}

/**
 * Constant-time string comparison. Avoids early-exit timing leaks when
 * checking the runtime auth secret. Length difference still leaks (but
 * RUNTIME_SECRET length is constant).
 */
function constantTimeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let result = 0;
  for (let i = 0; i < a.length; i++) {
    // codePointAt is preferred over charCodeAt; for ASCII secrets the values
    // are identical, but the wider type satisfies modern lint rules.
    const ca = a.codePointAt(i) ?? 0;
    const cb = b.codePointAt(i) ?? 0;
    result |= ca ^ cb;
  }
  return result === 0;
}

const RUNTIME_SECRET_RAW = Deno.env.get("RUNTIME_SECRET");
if (!RUNTIME_SECRET_RAW) {
  console.error("FATAL: RUNTIME_SECRET environment variable is required");
  Deno.exit(1);
}
const RUNTIME_SECRET: string = RUNTIME_SECRET_RAW;

const MAX_CODE_SIZE = 512 * 1024; // 512 KB
const MAX_INVOKE_BODY = 1024 * 1024; // 1 MB
const MAX_SCRIPTS = 100;
const INVOKE_TIMEOUT_MS = 30_000;
const WORKER_INIT_TIMEOUT_MS = 5_000;
const VALID_ID = /^[a-zA-Z0-9_-]{1,128}$/;
// Per-function log ring buffer capacity. Old entries are dropped first.
const LOG_RING_SIZE = 100;
// Cap on a single log line so one huge console.log() can't blow up memory.
const MAX_LOG_LINE = 4 * 1024;

// Allowed network hosts for workers — only project Postgres services
// Format: "host1:port1,host2:port2" or empty for no network access
const ALLOWED_HOSTS = (Deno.env.get("ALLOWED_HOSTS") || "").split(",").filter(Boolean);

// Server port — defaults to 8000 for production; tests override via env so
// concurrent test runs don't collide on the same port.
const PORT = Number(Deno.env.get("PORT") || "8000");

// Feature flag for the v2 tagged FunctionDef shape (kind: "query" |
// "mutation" | "action"). When off (default), v2-shaped exports fall back
// through to the legacy Fetch handler path, which will fail naturally
// because the export is an object rather than a function. When on, the
// worker recognises the shape, builds a ctx skeleton (auth.claims from the
// incoming Bearer JWT, db: null in this phase), and dispatches the
// handler. Existing legacy Fetch handlers are unaffected either way.
const V2_ENABLED = Deno.env.get("EXCALIBASE_FUNCTIONS_V2") === "1" ||
  Deno.env.get("EXCALIBASE_FUNCTIONS_V2") === "true";

// Phase 2 metadata capture — when set, the runtime POSTs every worker's
// scanned v2 export metadata to ${PROVISIONING_URL}/internal/runtime/
// functions/{fnId}/metadata. Authenticated with the same RUNTIME_SECRET
// the Go side uses to talk to /deploy. Best-effort; failures are logged.
const PROVISIONING_URL = Deno.env.get("EXCALIBASE_PROVISIONING_URL") || "";

// V2_KINDS — recognised tagged FunctionDef kinds. Used both in the worker
// (kept as a literal in the template) and in shape checks here. Phase 3
// codegen will key off these too. Phase 7 extends the family with
// httpAction and httpRouter; these share the v2 boot path but skip the
// {args}-parsing dispatch and go through a raw-Request handler instead.
const V2_KINDS = ["query", "mutation", "action", "httpAction", "httpRouter"];

// EXCALIBASE_RUN_MAX_DEPTH bounds the nested ctx.runQuery/runMutation/
// runAction call chain so a buggy handler can't recurse without limit.
// Default 8 (matches @excalibase/server@0.6.0 CHANGELOG). Each runX RPC
// carries the caller's depth + 1 in its envelope; the dispatcher rejects
// any value above the limit before the message reaches the target worker.
const RUN_MAX_DEPTH = Math.max(1, Number(Deno.env.get("EXCALIBASE_RUN_MAX_DEPTH") || "8"));

// Phase 9a — mutation isolation level + retry-on-conflict tuning.
//
// EXCALIBASE_MUTATION_ISOLATION sets the BEGIN ISOLATION LEVEL clause for
// every top-level mutation. Default SERIALIZABLE matches Convex semantics
// (the commit boundary is the ordering boundary; SSI surfaces conflicts
// at COMMIT time). Operators with bench-proven contention costs can drop
// to REPEATABLE READ or READ COMMITTED at the cost of weaker guarantees.
// Validated at boot — an unknown value crashes the process so a typo in
// a Helm value doesn't silently downgrade isolation in prod.
const ALLOWED_MUTATION_ISOLATIONS = new Set([
  "SERIALIZABLE",
  "REPEATABLE READ",
  "READ COMMITTED",
]);
const MUTATION_ISOLATION_RAW = (Deno.env.get("EXCALIBASE_MUTATION_ISOLATION") || "SERIALIZABLE")
  .toUpperCase()
  .trim();
if (!ALLOWED_MUTATION_ISOLATIONS.has(MUTATION_ISOLATION_RAW)) {
  console.error(
    `FATAL: EXCALIBASE_MUTATION_ISOLATION="${MUTATION_ISOLATION_RAW}" is invalid; ` +
      `allowed: ${Array.from(ALLOWED_MUTATION_ISOLATIONS).join(", ")}`,
  );
  Deno.exit(1);
}
const MUTATION_ISOLATION: string = MUTATION_ISOLATION_RAW;

// EXCALIBASE_MUTATION_RETRY_MAX caps the retry-loop attempt count. 1 means
// "try once, never retry"; 5 (default) means up to 4 retries on
// 40001/40P01. Above 50 makes no operational sense — clamp.
const MUTATION_RETRY_MAX = Math.max(
  1,
  Math.min(50, Number(Deno.env.get("EXCALIBASE_MUTATION_RETRY_MAX") || "5")),
);
// EXCALIBASE_MUTATION_RETRY_BACKOFF_MS is the base for the exponential +
// jitter backoff: sleepMs = base * 2^(attempt-1) * (0.5 + random()),
// capped at 5000ms. Default 50.
const MUTATION_RETRY_BACKOFF_MS = Math.max(
  1,
  Number(Deno.env.get("EXCALIBASE_MUTATION_RETRY_BACKOFF_MS") || "50"),
);
const MUTATION_RETRY_BACKOFF_CAP_MS = 5_000;
// SQLSTATE codes the retry loop treats as retryable conflicts.
//  40001 — serialization_failure (SSI / RR ordering)
//  40P01 — deadlock_detected
const RETRYABLE_SQLSTATES = new Set(["40001", "40P01"]);

/** Build the JS source that runs inside the Deno Web Worker. */
function buildWorkerCode(userCode: string, secrets: Record<string, string>): string {
  // `Deno` is frozen inside workers, so we can't reassign `globalThis.Deno`.
  // Instead we wrap user code in an IIFE that shadows `Deno` with a mock via
  // a parameter. Inside the IIFE, any `Deno.env.get(...)` lookup resolves to
  // our mock; outside, the real `Deno` is untouched. Worker permissions still
  // apply either way.
  //
  // We also expose a plain `env` helper (`env.KEY`) for ergonomics — users
  // migrating from Supabase get `Deno.env.get`, new users get `env.KEY`.
  const secretsJSON = JSON.stringify(secrets);
  return String.raw`
    // --- console interceptor ---
    // Proxy console.* so user log output is streamed back to the runtime
    // and stored in a per-function ring buffer. Original console.* is still
    // called so logs also land on the pod stdout for operators.
    (function() {
      const LEVELS = ['log', 'info', 'warn', 'error', 'debug'];
      const MAX_LINE = ${MAX_LOG_LINE};
      const fmt = (args) => {
        try {
          return args.map((a) => {
            if (typeof a === 'string') return a;
            if (a instanceof Error) return a.stack || a.message || String(a);
            try { return JSON.stringify(a); } catch (_) { return String(a); }
          }).join(' ');
        } catch (_) { return '[unformattable]'; }
      };
      for (const lvl of LEVELS) {
        const orig = console[lvl] ? console[lvl].bind(console) : console.log.bind(console);
        console[lvl] = function() {
          const args = Array.prototype.slice.call(arguments);
          let msg = fmt(args);
          if (msg.length > MAX_LINE) msg = msg.slice(0, MAX_LINE) + '…';
          try {
            self.postMessage({ type: 'log', level: lvl, msg: msg, ts: Date.now() });
          } catch (_) { /* ignore */ }
          try { orig.apply(null, args); } catch { /* suppress original console errors */ }
        };
      }
    })();

    (function(Deno, env) {
      // --- user bundled code (may assign globalThis.__excalibase_default) ---
      ${userCode}
    })(
      // Shadowed Deno — .env is our mock, everything else is copied from the real Deno.
      (() => {
        const __secrets = ${secretsJSON};
        const mockEnv = {
          get(key) { return __secrets[key]; },
          has(key) { return Object.prototype.hasOwnProperty.call(__secrets, key); },
          toObject() { return { ...__secrets }; },
          set() { /* read-only */ },
          delete() { /* read-only */ },
        };
        const shadowed = Object.create(globalThis.Deno);
        Object.defineProperty(shadowed, 'env', { value: mockEnv, enumerable: true });
        Object.defineProperty(shadowed, 'serve', {
          value: () => {
            throw new Error('Deno.serve is not available inside Excalibase functions — export a default handler instead');
          },
          enumerable: true,
        });
        return shadowed;
      })(),
      // Plain env namespace (non-Deno code path)
      (() => {
        const __secrets = ${secretsJSON};
        return {
          get(key) { return __secrets[key]; },
          has(key) { return Object.prototype.hasOwnProperty.call(__secrets, key); },
          toObject() { return { ...__secrets }; },
        };
      })()
    );

    // --- v2 helpers (only used when EXCALIBASE_FUNCTIONS_V2 is on) ---
    const __V2_ENABLED = ${V2_ENABLED ? "true" : "false"};
    const __V2_KINDS = ${JSON.stringify(V2_KINDS)};

    // __isV2Export — duck-types a tagged FunctionDef record:
    //   { kind: "query"|"mutation"|"action", args: <zod-or-parseable>, handler: function }
    // or, for Phase 7's HTTP shapes:
    //   { kind: "httpAction", handler: function }
    //   { kind: "httpRouter", __excalibase_route_handlers: { ... } }
    // We accept the args-carrying kinds whose 'args' is non-null because
    // the user's zod (or hand-rolled) parser will run inside the handler
    // shim below. http* kinds skip the args validation entirely.
    function __isV2Export(d) {
      if (!d || typeof d !== 'object') return false;
      if (__V2_KINDS.indexOf(d.kind) === -1) return false;
      if (d.kind === 'httpAction') {
        return typeof d.handler === 'function';
      }
      if (d.kind === 'httpRouter') {
        // The router itself has no callable handler — dispatch resolves
        // per-route handlers from __excalibase_route_handlers. We just
        // confirm the side-channel map is present.
        return typeof d.__excalibase_route_handlers === 'object' &&
               d.__excalibase_route_handlers !== null;
      }
      if (typeof d.handler !== 'function') return false;
      if (d.args === null || d.args === undefined) return false;
      return true;
    }

    // __decodeJwtClaims — base64url-decode the JWT payload segment. The Go
    // gateway has already verified the signature before forwarding; this
    // worker only reads claims for ctx.auth. Returns null on a malformed
    // or absent token so the handler can still run unauthenticated.
    function __decodeJwtClaims(authHeader) {
      if (typeof authHeader !== 'string') return null;
      if (!authHeader.toLowerCase().startsWith('bearer ')) return null;
      const token = authHeader.slice(7).trim();
      if (!token) return null;
      const parts = token.split('.');
      if (parts.length !== 3) return null;
      try {
        // base64url → base64 (atob is base64 only)
        let p = parts[1].replace(/-/g, '+').replace(/_/g, '/');
        const pad = p.length % 4;
        if (pad === 2) p += '==';
        else if (pad === 3) p += '=';
        else if (pad !== 0) return null;
        const json = atob(p);
        return JSON.parse(json);
      } catch (_) {
        return null;
      }
    }

    // --- ctx.db RPC facade ---
    // The worker holds no DB connection of its own. Each ctx.db.<op>() call
    // posts a {type:"db", rpcId, op, ...} message to the main thread; the
    // main thread executes SQL against its singleton postgres.js pool and
    // posts {type:"dbResult", rpcId, result} back. We keep a pending map
    // keyed by rpcId so multiple concurrent calls inside one invocation
    // can race without overwriting each other's resolvers.
    const __dbPending = new Map();
    let __nextDbRpcId = 1;
    // Phase 8.5: per-invocation txn ref. Set by __dispatchV2/__dispatchHttp
    // before calling the user handler so every db/scheduler/runX RPC the
    // handler posts can be routed to the right Postgres connection on the
    // main thread. Empty string means "no active txn" (use pool).
    let __currentTxnRefId = '';

    function __dbCall(op, collection, payload) {
      const rpcId = __nextDbRpcId++;
      return new Promise((resolve, reject) => {
        __dbPending.set(rpcId, { resolve, reject });
        self.postMessage({ type: 'db', rpcId, op, collection,
          txnRefId: __currentTxnRefId,
          ...payload });
      });
    }

    // __schemaConfig — the worker reads the bundler-injected metadata at
    // globalThis.__excalibase_function_metadata.schemaJson on first
    // ctx.db.collection call. When absent, the runtime stays permissive
    // (any collection name OK). When present, an unknown collection name
    // throws a friendly error before any SQL is issued, and search and
    // vectorSearch are pre-flighted against the declared index lists.
    // This mirrors Phase 5a schema-driven migration so the runtime and
    // DB shape stay in lock-step.
    function __schemaTables() {
      const meta = globalThis.__excalibase_function_metadata;
      if (!meta || meta.schemaJson === null || meta.schemaJson === undefined) return null;
      const raw = meta.schemaJson;
      if (typeof raw !== 'object' || !raw) return null;
      const tables = raw.tables;
      if (!tables || typeof tables !== 'object') return null;
      return tables;
    }

    function __assertCollectionInSchema(name) {
      const tables = __schemaTables();
      if (tables === null) return; // permissive mode
      if (!Object.prototype.hasOwnProperty.call(tables, name)) {
        throw new Error("Collection '" + name + "' is not in schema; declare it in schema.ts and redeploy");
      }
    }

    function __assertSearchable(name) {
      const tables = __schemaTables();
      if (tables === null) return; // permissive mode — surface 42703 from PG
      const def = tables[name];
      if (!def || !Array.isArray(def.searchIndexes) || def.searchIndexes.length === 0) {
        throw new Error("Collection '" + name + "' has no search index; declare one via defineTable(...).searchIndex(...) and redeploy");
      }
    }

    function __assertVectorable(name) {
      const tables = __schemaTables();
      if (tables === null) return; // permissive mode
      const def = tables[name];
      if (!def || !Array.isArray(def.vectorIndexes) || def.vectorIndexes.length === 0) {
        throw new Error("Collection '" + name + "' has no vector index; declare one via defineTable(...).vectorIndex(...) and redeploy");
      }
    }

    function __makeCollectionApi(name) {
      // Preflight at construction so an unknown collection fails fast even
      // before the user calls any specific op. The error propagates
      // through the user's await chain naturally.
      __assertCollectionInSchema(name);
      return {
        insert: (doc) => __dbCall('insert', name, { doc }),
        insertMany: (docs) => __dbCall('insertMany', name, { docs }),
        find: (filter, options) => __dbCall('find', name, { filter, options }),
        findOne: (filter) => __dbCall('findOne', name, { filter }),
        getById: (id) => __dbCall('getById', name, { id }),
        update: (filter, patch) => __dbCall('update', name, { filter, patch }),
        delete: (filter) => __dbCall('delete', name, { filter }),
        count: (filter) => __dbCall('count', name, { filter }),
        // search/vectorSearch are pre-flighted against the schema's
        // index declarations. In permissive mode (no schema), an actual
        // missing column surfaces as SQLSTATE 42703 from postgres.js.
        search: (query, options) => {
          __assertSearchable(name);
          return __dbCall('search', name, { query, options });
        },
        vectorSearch: (embedding, options) => {
          __assertVectorable(name);
          const opts = options || {};
          const payload = { embedding, options: { topK: opts.topK }, filter: opts.filter };
          return __dbCall('vectorSearch', name, payload);
        },
      };
    }

    // __makeQuery — Convex-shape chainable query builder, Phase 6.
    // Mirrors @excalibase/server@0.5.0's Query<TDoc> exactly:
    //   ctx.db.query("posts")
    //     .withIndex("by_author", q => q.eq("author", uid))
    //     .filter(q => q.gt(q.field("votes"), 10))
    //     .order("desc")
    //     .paginate({ cursor, numItems: 20 });
    //
    // Each chainable method returns a new builder whose plan carries the
    // accumulated state. Terminal methods (.first/.unique/.collect/.take/
    // .paginate) post a {type:'db', op:'query', plan, terminal, extra}
    // RPC to the main thread, which compiles the plan to SQL.
    function __makeQuery(name) {
      __assertCollectionInSchema(name);

      function __terminal(plan, terminal, extra) {
        return __dbCall('query', name, { plan, terminal, extra });
      }

      function __indexQ(initial) {
        const bounds = initial || [];
        function push(b) { return __indexQ([...bounds, b]); }
        return {
          eq: (field, value) => push({ field, op: 'eq', value }),
          gt: (field, value) => push({ field, op: 'gt', value }),
          gte: (field, value) => push({ field, op: 'gte', value }),
          lt: (field, value) => push({ field, op: 'lt', value }),
          lte: (field, value) => push({ field, op: 'lte', value }),
          range: (field, lo, hi) =>
            __indexQ([...bounds, { field, op: 'gte', value: lo }, { field, op: 'lte', value: hi }]),
          __bounds: () => bounds,
        };
      }

      function __searchQ(field, queryText, filters) {
        return {
          search: (f, q) => __searchQ(f, q, filters),
          eq: (f, v) => __searchQ(field, queryText, [...filters, { field: f, value: v }]),
          __field: field, __query: queryText, __filters: filters,
        };
      }

      function __vectorQ(embedding, k, filters) {
        return {
          vector: (e, k2) => __vectorQ(e, k2, filters),
          eq: (f, v) => __vectorQ(embedding, k, [...filters, { field: f, value: v }]),
          __embedding: embedding, __k: k, __filters: filters,
        };
      }

      // filterQ — pure AST constructor (no accumulation; expressions are
      // composed by the caller's lambda).
      const filterQ = {
        field: (n) => ({ kind: 'field', name: n }),
        eq:  (l, r) => ({ kind: 'eq',  left: l, right: r }),
        neq: (l, r) => ({ kind: 'neq', left: l, right: r }),
        gt:  (l, r) => ({ kind: 'gt',  left: l, right: r }),
        gte: (l, r) => ({ kind: 'gte', left: l, right: r }),
        lt:  (l, r) => ({ kind: 'lt',  left: l, right: r }),
        lte: (l, r) => ({ kind: 'lte', left: l, right: r }),
        and: function() { return { kind: 'and', args: Array.prototype.slice.call(arguments) }; },
        or:  function() { return { kind: 'or',  args: Array.prototype.slice.call(arguments) }; },
        not: (a) => ({ kind: 'not', arg: a }),
      };

      function __buildQuery(plan) {
        return {
          withIndex: (idxName, builder) => {
            const seed = __indexQ();
            const built = builder ? builder(seed) : seed;
            const bounds = (built && typeof built.__bounds === 'function') ? built.__bounds() : [];
            return __buildQuery(Object.assign({}, plan, { index: { name: idxName, bounds } }));
          },
          withSearchIndex: (idxName, builder) => {
            const out = builder(__searchQ('', '', []));
            return __buildQuery(Object.assign({}, plan, {
              search: { name: idxName, field: out.__field, query: out.__query, filters: out.__filters },
            }));
          },
          withVectorIndex: (idxName, builder) => {
            const out = builder(__vectorQ([], 0, []));
            return __buildQuery(Object.assign({}, plan, {
              vector: { name: idxName, embedding: out.__embedding, k: out.__k, filters: out.__filters },
            }));
          },
          filter: (pred) => {
            const expr = pred(filterQ);
            const merged = plan.filter
              ? { kind: 'and', args: [plan.filter, expr] }
              : expr;
            return __buildQuery(Object.assign({}, plan, { filter: merged }));
          },
          order: (direction) => __buildQuery(Object.assign({}, plan, { order: direction })),

          first:   () => __terminal(plan, 'first'),
          unique:  () => __terminal(plan, 'unique'),
          collect: () => __terminal(plan, 'collect'),
          take:    (n) => __terminal(plan, 'take', n),
          paginate: (opts) => __terminal(plan, 'paginate', opts),

          getDependencies: () => [plan.collection],
        };
      }
      return __buildQuery({ collection: name });
    }

    function __makeDbClient() {
      return {
        collection: (name) => __makeCollectionApi(name),
        query: (name) => __makeQuery(name),
      };
    }

    // --- ctx.runQuery / runMutation / runAction (Phase 7) ---
    // Composition surface for cross-function calls. Each call posts a
    // {type:'runX', op, ref, args} message to the main thread; the main
    // thread looks up the target (in-process if same runtime, HTTP self-call
    // if not) and posts {type:'runXResult', rpcId, result} back.
    //
    // The current call depth rides along in the envelope so the dispatcher
    // can reject runaway recursion before the message reaches the target.
    // The initial depth is set by the v2 dispatcher when it builds the ctx
    // for an invocation, and increments for every nested call this worker
    // makes via __runXCall.
    const __runXPending = new Map();
    let __nextRunXRpcId = 1;
    // Set by __dispatchV2 / httpAction dispatch before the handler runs.
    let __currentRunDepth = 0;
    const __RUN_MAX_DEPTH = ${RUN_MAX_DEPTH};

    function __runXCall(op, ref, args) {
      // ref must look like { moduleName, exportName }. Reject malformed
      // values up-front so the user gets a clean error.
      if (!ref || typeof ref !== 'object' ||
          typeof ref.moduleName !== 'string' || ref.moduleName.length === 0 ||
          typeof ref.exportName !== 'string' || ref.exportName.length === 0) {
        return Promise.reject(new Error("ctx." + op + ": first argument must be a FunctionRef { moduleName, exportName }"));
      }
      const nextDepth = __currentRunDepth + 1;
      if (nextDepth > __RUN_MAX_DEPTH) {
        return Promise.reject(new Error(
          "ctx." + op + ": run depth limit exceeded (" + __RUN_MAX_DEPTH +
          "). Check for infinite recursion."
        ));
      }
      const rpcId = __nextRunXRpcId++;
      return new Promise((resolve, reject) => {
        __runXPending.set(rpcId, { resolve, reject });
        self.postMessage({
          type: 'runX', rpcId, op, ref, args,
          depth: nextDepth,
          // Phase 8.5: forward the caller's txn ref so a mutation→mutation
          // call can ride the parent's transaction. Main-thread dispatch
          // only honours this when the caller's kind is "mutation"; for
          // queries/actions the ref is recorded but ignored.
          txnRefId: __currentTxnRefId,
        });
      });
    }

    function __makeRunQuery()    { return (ref, args) => __runXCall('runQuery',    ref, args); }
    function __makeRunMutation() { return (ref, args) => __runXCall('runMutation', ref, args); }
    function __makeRunAction()   { return (ref, args) => __runXCall('runAction',   ref, args); }

    // --- ctx.scheduler (Phase 8) ---
    // Worker-side facade for runAfter / runAt / cancel. Each method posts
    // a {type:'scheduler', op, ...} message to the main thread; the main
    // thread does the INSERT/UPDATE on the project DB and posts a
    // {type:'schedulerResult', rpcId, result} message back.
    const __schedPending = new Map();
    let __nextSchedRpcId = 1;
    function __schedCall(op, payload) {
      const rpcId = __nextSchedRpcId++;
      return new Promise((resolve, reject) => {
        __schedPending.set(rpcId, { resolve, reject });
        // Phase 8.5: route the scheduler INSERT through the mutation's txn
        // when one is active. Action handlers carry no txnRefId, so their
        // schedule lands on the pool — matching Convex's "actions commit
        // independently" semantics.
        self.postMessage({ type: 'scheduler', rpcId, op, payload,
          txnRefId: __currentTxnRefId });
      });
    }
    function __makeScheduler() {
      return {
        runAfter: (delayMs, ref, args) => {
          if (!ref || typeof ref !== 'object' ||
              typeof ref.moduleName !== 'string' || ref.moduleName.length === 0 ||
              typeof ref.exportName !== 'string' || ref.exportName.length === 0) {
            return Promise.reject(new Error("ctx.scheduler.runAfter: ref must be { moduleName, exportName }"));
          }
          const d = Number(delayMs);
          if (!Number.isFinite(d)) {
            return Promise.reject(new Error("ctx.scheduler.runAfter: delayMs must be a finite number"));
          }
          return __schedCall('runAfter', { delayMs: d, ref, args: args === undefined ? {} : args });
        },
        runAt: (timestamp, ref, args) => {
          if (!ref || typeof ref !== 'object' ||
              typeof ref.moduleName !== 'string' || ref.moduleName.length === 0 ||
              typeof ref.exportName !== 'string' || ref.exportName.length === 0) {
            return Promise.reject(new Error("ctx.scheduler.runAt: ref must be { moduleName, exportName }"));
          }
          const t = Number(timestamp);
          if (!Number.isFinite(t)) {
            return Promise.reject(new Error("ctx.scheduler.runAt: timestamp must be a finite number"));
          }
          return __schedCall('runAt', { timestamp: t, ref, args: args === undefined ? {} : args });
        },
        cancel: (id) => {
          if (typeof id !== 'string' || id.length === 0) {
            return Promise.reject(new Error("ctx.scheduler.cancel: id must be a non-empty string"));
          }
          return __schedCall('cancel', { id });
        },
      };
    }

    // __dispatchHttp — Phase 7 httpAction/httpRouter dispatch. Skips the
    // {args}-parsing v2 path; reconstructs a raw Request from the invoke
    // envelope and forwards the handler's Response unchanged.
    //
    // For an httpAction the default export's handler runs every time.
    // For an httpRouter we look up the matching (path, method) row in
    // the bundled __excalibase_route_handlers map; misses return 404.
    async function __dispatchHttp(reqId, reqData, fnDef, txnRefId) {
      try {
        const headers = reqData.headers || {};
        const auth = headers['Authorization'] || headers['authorization'] || '';
        const claims = __decodeJwtClaims(auth);
        // httpAction/httpRouter don't have an args envelope, so the runX
        // envelope's depth must ride alongside reqData.runDepth (the
        // gateway forwards it that way for internal invocations).
        // Public traffic has no depth, so default to 0.
        const incomingDepth = typeof reqData.runDepth === 'number' ? reqData.runDepth : 0;
        __currentRunDepth = incomingDepth;
        // Phase 8.5: http* handlers don't own a transaction (action-equivalent
        // boundary). Forward any incoming txnRefId so a nested ctx.runMutation
        // routes through the right entry on the main side.
        __currentTxnRefId = typeof txnRefId === 'string' ? txnRefId : '';

        // Reconstruct a raw Request. The runtime accepts relative URLs;
        // Request requires an absolute one. Use the same synthetic base
        // as the legacy Fetch path so URL parsing inside the handler
        // (req.url, new URL(req.url)) works.
        let url = reqData.url || '/';
        if (!/^https?:\/\//.test(url)) {
          url = 'http://fn.excalibase.local' + (url.startsWith('/') ? '' : '/') + url;
        }
        const init = { method: reqData.method || 'GET', headers };
        if (reqData.body && reqData.method !== 'GET' && reqData.method !== 'HEAD') {
          init.body = reqData.body;
        }
        const req = new Request(url, init);

        // httpAction ctx parity: db is null (Convex contract), runX
        // surface is wired so the handler can compose with other functions.
        // Scheduler is also present — httpAction handlers can enqueue
        // background work the same way actions can.
        const ctx = {
          db: null,
          auth: { claims },
          runQuery:    __makeRunQuery(),
          runMutation: __makeRunMutation(),
          runAction:   __makeRunAction(),
          scheduler:   __makeScheduler(),
        };

        // Find the handler. For httpAction the def itself carries it; for
        // httpRouter we resolve via the route-handlers map keyed by
        // "METHOD path". A miss is a 404 (Convex parity: method mismatch
        // is also 404, not 405).
        let handler;
        if (fnDef.kind === 'httpAction') {
          handler = fnDef.handler;
        } else if (fnDef.kind === 'httpRouter') {
          const handlers = fnDef.__excalibase_route_handlers || {};
          const path = new URL(req.url).pathname;
          handler = handlers[req.method + ' ' + path];
          if (typeof handler !== 'function') {
            self.postMessage({ type: 'success', reqId, status: 404,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'not found' }) });
            return;
          }
        }
        if (typeof handler !== 'function') {
          self.postMessage({ type: 'success', reqId, status: 500,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: 'http handler missing' }) });
          return;
        }

        let res;
        try {
          res = await handler(ctx, req);
        } catch (err) {
          self.postMessage({ type: 'success', reqId, status: 500,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: String(err && err.message || err) }) });
          return;
        }
        if (!(res instanceof Response)) {
          self.postMessage({ type: 'success', reqId, status: 500,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: 'http handler must return a Response' }) });
          return;
        }
        const bodyText = await res.text();
        const outHeaders = {};
        res.headers.forEach((v, k) => { outHeaders[k] = v; });
        self.postMessage({ type: 'success', reqId, status: res.status,
          headers: outHeaders, body: bodyText });
      } catch (outer) {
        self.postMessage({ type: 'error', reqId,
          error: String(outer && outer.message || outer) });
      }
    }

    // __dispatchV2 — runs the tagged FunctionDef contract:
    //   POST body must be { "args": <object> }; on missing args, 400.
    //   Calls handler(ctx, body.args); wraps result as { data } JSON.
    //   ctx.db is a real DbClient for query/mutation; null for action.
    async function __dispatchV2(reqId, reqData, fnDef, txnRefId) {
      try {
        const headers = reqData.headers || {};
        const auth = headers['Authorization'] || headers['authorization'] || '';
        const claims = __decodeJwtClaims(auth);

        let body = {};
        if (reqData.body) {
          try { body = JSON.parse(reqData.body); } catch (_) {
            self.postMessage({ type: 'success', reqId, status: 400,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'invalid JSON body' }) });
            return;
          }
        }
        if (!body || typeof body !== 'object' || !('args' in body)) {
          self.postMessage({ type: 'success', reqId, status: 400,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: "request body must contain 'args' field" }) });
          return;
        }

        // Convex parity: actions get db=null; query/mutation get a real
        // DbClient facade. Unknown kinds default to null to be safe.
        const db = (fnDef.kind === 'query' || fnDef.kind === 'mutation')
          ? __makeDbClient()
          : null;
        // Initial depth comes from the request body (set by dispatchRunX
        // for cross-function calls; absent on top-level public traffic).
        // The gateway sets it to 0 for top-level public calls; runX bumps
        // it for each nested call so the chain is end-to-end bounded.
        const incomingDepth = typeof body.runDepth === 'number' ? body.runDepth : 0;
        __currentRunDepth = incomingDepth;
        // Phase 8.5: pick up the txn ref the main thread stamped on this
        // invocation. Empty string means "no active txn" (query, action,
        // or v1 fetch handler) and db/scheduler RPCs will fall back to the
        // pool on the main side.
        __currentTxnRefId = typeof txnRefId === 'string' ? txnRefId : '';
        const ctx = {
          db,
          auth: { claims },
          // Phase 7: composition surface. Type-level read-only rules are
          // enforced by the lib (QueryCtx has no runMutation/runAction);
          // the worker exposes all three on every Ctx variant so a
          // hand-rolled bundle can still compose, but discipline lives
          // in the typed lib path. Read-only enforcement at runtime is
          // the main thread's job (dispatchRunX kind-matrix check).
          runQuery:    __makeRunQuery(),
          runMutation: __makeRunMutation(),
          runAction:   __makeRunAction(),
          // Phase 8: scheduler — present on mutation/action ctx via the
          // typed lib. Worker exposes it on every Ctx so the runtime
          // path is uniform; the typed lib still keeps it off QueryCtx
          // (a query handler that tries to schedule is a compile error).
          scheduler:   __makeScheduler(),
        };
        try {
          const result = await fnDef.handler(ctx, body.args);
          self.postMessage({ type: 'success', reqId, status: 200,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ data: result === undefined ? null : result }) });
        } catch (err) {
          // Surface ValidationError from @excalibase/server as a 400.
          if (err && err.name === 'ValidationError' && Array.isArray(err.issues)) {
            self.postMessage({ type: 'success', reqId, status: 400,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'validation', issues: err.issues }) });
            return;
          }
          // If the handler threw a zod-style issues array, relay it as 400.
          if (err && Array.isArray(err.issues)) {
            self.postMessage({ type: 'success', reqId, status: 400,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ error: 'args validation failed', issues: err.issues }) });
            return;
          }
          self.postMessage({ type: 'success', reqId, status: 500,
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ error: String(err && err.message || err) }) });
        }
      } catch (outer) {
        self.postMessage({ type: 'error', reqId, error: String(outer && outer.message || outer) });
      }
    }

    // --- v2 export metadata scan + report ---
    // After the user module loaded above, scan globalThis.__excalibase_default
    // for the v2 tagged shape and build a name/kind/argsJsonSchema array.
    // Since this phase has a single default export per function, the array
    // has at most one entry. The export's __metadata.argsJsonSchema (set by
    // the @excalibase/server tag wrappers when they call zodToJsonSchema)
    // is preferred; we fall back to an empty schema when none is attached.
    function __collectV2Metadata() {
      if (!__V2_ENABLED) return [];
      const exp = globalThis.__excalibase_default;
      if (!__isV2Export(exp)) return [];
      const meta = exp.__metadata || {};
      const argsJsonSchema =
        (meta && typeof meta.argsJsonSchema === 'object' && meta.argsJsonSchema !== null)
          ? meta.argsJsonSchema
          : { type: 'object', properties: {} };
      // The name field is filled by the main thread (it knows the runtime
      // id) before the HTTP forward — here we report a placeholder.
      const entry = { name: 'default', kind: exp.kind, argsJsonSchema: argsJsonSchema };
      // Phase 7: surface the isInternal flag so the gateway can gate
      // PublicInvoke. Omit when false to keep the metadata payload
      // byte-stable for legacy v2 records.
      if (exp.isInternal === true) entry.isInternal = true;
      return [entry];
    }

    // --- worker dispatch ---
    // Each invoke message carries a unique reqId so the runtime can correlate
    // concurrent responses on the same worker without races. The same
    // handler also routes dbResult replies back to the right __dbCall().
    self.onmessage = async (e) => {
      const msg = e.data;
      if (msg && msg.type === 'dbResult') {
        const pending = __dbPending.get(msg.rpcId);
        if (!pending) return;
        __dbPending.delete(msg.rpcId);
        if (msg.result && msg.result.ok === true) {
          pending.resolve(msg.result.data);
        } else {
          const err = new Error(msg.result && msg.result.error || 'db error');
          if (msg.result && msg.result.issues) {
            // Surface validation issues so the handler can catch and
            // re-render. Attach as an issues array for ValidationError parity.
            err.issues = msg.result.issues;
            err.name = 'ValidationError';
          }
          pending.reject(err);
        }
        return;
      }
      // Phase 7: runX RPC reply. Mirrors the db RPC channel but on its
      // own pending map so the two don't collide.
      if (msg && msg.type === 'runXResult') {
        const pending = __runXPending.get(msg.rpcId);
        if (!pending) return;
        __runXPending.delete(msg.rpcId);
        if (msg.result && msg.result.ok === true) {
          pending.resolve(msg.result.data);
        } else {
          const err = new Error(msg.result && msg.result.error || 'runX error');
          if (msg.result && msg.result.issues) {
            err.issues = msg.result.issues;
            err.name = msg.result.errorName || 'ValidationError';
          } else if (msg.result && msg.result.errorName) {
            err.name = msg.result.errorName;
          }
          pending.reject(err);
        }
        return;
      }
      // Phase 8: scheduler RPC reply.
      if (msg && msg.type === 'schedulerResult') {
        const pending = __schedPending.get(msg.rpcId);
        if (!pending) return;
        __schedPending.delete(msg.rpcId);
        if (msg.result && msg.result.ok === true) {
          pending.resolve(msg.result.data);
        } else {
          pending.reject(new Error(msg.result && msg.result.error || 'scheduler error'));
        }
        return;
      }
      if (msg && msg.type === 'invoke') {
        const reqId = msg.reqId;
        const reqData = msg.data;
        // Phase 8.5: main-thread stamps the txn ref onto every invoke
        // envelope so this worker's db/scheduler/runX RPCs can echo it
        // back. Empty string when no txn is active (queries, actions,
        // v1 handlers, or the initial top-level call before main decides).
        const txnRefId = typeof msg.txnRefId === 'string' ? msg.txnRefId : '';

        // v2 path — only if the flag is on AND the export matches the
        // tagged FunctionDef shape. Anything else falls through to legacy.
        const exp = globalThis.__excalibase_default;
        if (__V2_ENABLED && __isV2Export(exp)) {
          // Phase 7: httpAction / httpRouter take the raw-Request path.
          if (exp.kind === 'httpAction' || exp.kind === 'httpRouter') {
            await __dispatchHttp(reqId, reqData, exp, txnRefId);
            return;
          }
          await __dispatchV2(reqId, reqData, exp, txnRefId);
          return;
        }

        try {
          const init = { method: reqData.method || 'GET', headers: reqData.headers || {} };
          if (reqData.body && reqData.method !== 'GET' && reqData.method !== 'HEAD') {
            init.body = reqData.body;
          }
          // Accept relative URLs from the platform (e.g. /api/.../invoke path).
          // Request() requires an absolute URL, so prepend a synthetic base.
          let url = reqData.url || '/';
          if (!/^https?:\/\//.test(url)) {
            url = 'http://fn.excalibase.local' + (url.startsWith('/') ? '' : '/') + url;
          }
          const req = new Request(url, init);

          const handler = globalThis.__excalibase_default;
          if (typeof handler !== 'function') {
            throw new Error('No default export found — function must export default (req: Request) => Response');
          }

          let res = await handler(req);
          // Normalise non-Response returns into JSON responses
          if (!(res instanceof Response)) {
            res = Response.json(res);
          }

          const bodyText = await res.text();
          const headers = {};
          res.headers.forEach((v, k) => { headers[k] = v; });
          self.postMessage({ type: 'success', reqId, status: res.status, headers, body: bodyText });
        } catch (err) {
          self.postMessage({ type: 'error', reqId, error: String(err && err.message || err) });
        }
      }
    };
    // Best-effort metadata emission. Captured before 'ready' so main can
    // forward the payload as soon as deploy finishes; failures are
    // swallowed so a malformed user export never blocks deploy.
    try {
      const __exports = __collectV2Metadata();
      if (__exports.length > 0) {
        self.postMessage({ type: 'metadata', exports: __exports });
      }
    } catch (_metaErr) { /* ignore */ }

    self.postMessage({ type: 'ready' });
  `;
}

// RunXMessage is the envelope shape posted by a worker's __runXCall(...)
// surface back to the main thread. `op` is the variant being invoked,
// `ref` carries the typed function reference, `args` is the validated
// payload, and `depth` is the post-increment composition depth (the
// dispatcher enforces depth ≤ RUN_MAX_DEPTH before forwarding).
interface RunXMessage {
  type: "runX";
  rpcId: number;
  op: "runQuery" | "runMutation" | "runAction";
  ref: { moduleName: string; exportName: string };
  args: unknown;
  depth: number;
  // Phase 8.5: present when the caller's worker has an active mutation
  // txn. dispatchRunX only forwards it to the target when the kind matrix
  // permits shared-txn composition (mutation → mutation).
  txnRefId?: string;
}

// dispatchRunX resolves a function reference against the runtime's local
// script table and invokes the target in-process. The caller's runtime id
// (`${projectId}__${fnId}`) gives us the project; we look for any deployed
// script in the same project whose fn-id matches the ref's moduleName.
//
// For Phase 7 the in-process path is the only one wired here. Cross-runtime
// hops can be added in Phase 8 (scheduler) when separate pods need to
// compose — the gateway's /internal/invoke route is ready to receive them.
async function dispatchRunX(callerRuntimeID: string, msg: RunXMessage): Promise<unknown> {
  if (msg.depth > RUN_MAX_DEPTH) {
    throw new Error(`ctx.${msg.op}: run depth limit exceeded (${RUN_MAX_DEPTH})`);
  }
  const sep = callerRuntimeID.indexOf("__");
  if (sep < 0) {
    throw new Error("runX: caller runtime id missing project separator");
  }
  const projectID = callerRuntimeID.slice(0, sep);
  const targetID = `${projectID}__${msg.ref.moduleName}`;

  // Phase 8 read-only enforcement. The caller and target kinds are
  // captured at deploy time via the metadata callback (see
  // ScriptMetadata.kind). We refuse the call before dispatching when the
  // matrix says "no":
  //
  //   query   → query      OK
  //   query   → mutation   ERROR
  //   query   → action     ERROR
  //   mutation→ query      OK
  //   mutation→ mutation   OK (shared txn — runtime-side wiring)
  //   mutation→ action     OK
  //   action  → *          OK
  //
  // Unknown caller or target kinds are not enforced — falls back to the
  // pre-Phase-8 behaviour so v1 fetch handlers (which never report a
  // kind) keep working.
  const callerKind = runtime.getKind(callerRuntimeID);
  const targetKind = runtime.getKind(targetID);
  if (callerKind === "query") {
    if (targetKind === "mutation") {
      throw new Error(
        `Calling mutation ${msg.ref.moduleName} from a query is not allowed`,
      );
    }
    if (targetKind === "action") {
      throw new Error(
        `Calling action ${msg.ref.moduleName} from a query is not allowed`,
      );
    }
  }
  // Phase 8.5: forward the caller's txn ref only when the caller is a
  // mutation. From a mutation → another mutation we want shared-txn
  // semantics; from an action → mutation we deliberately do NOT propagate
  // (the action has no transactional boundary), so the inner mutation
  // opens a fresh top-level txn — Convex parity.
  const propagateTxn = callerKind === "mutation" && targetKind === "mutation"
    && typeof msg.txnRefId === "string" && msg.txnRefId.length > 0;
  const bodyPayload: Record<string, unknown> = {
    args: msg.args,
    runDepth: msg.depth,
  };
  if (propagateTxn) bodyPayload.txnRefId = msg.txnRefId;
  const invokeReq: InvokeRequest = {
    method: "POST",
    url: `/invoke/${targetID}`,
    headers: { "content-type": "application/json" },
    body: JSON.stringify(bodyPayload),
  };
  let res: InvokeResponse;
  try {
    res = await runtime.invoke(targetID, invokeReq);
  } catch (err) {
    throw err instanceof Error ? err : new Error(String(err));
  }
  // The target's response envelope mirrors the dispatchV2 contract:
  //   200 + { data } on success, 400/500 + { error, issues? } on failure.
  let parsed: { data?: unknown; error?: string; issues?: unknown };
  try {
    parsed = JSON.parse(res.body);
  } catch (_) {
    parsed = { error: res.body };
  }
  if (res.status >= 200 && res.status < 300) {
    return parsed.data === undefined ? null : parsed.data;
  }
  const err = new Error(parsed.error || `runX target returned ${res.status}`);
  if (parsed.issues) {
    (err as { issues?: unknown }).issues = parsed.issues;
    err.name = "ValidationError";
  }
  throw err;
}

// SchedulerMessage is the envelope shape posted by a worker's
// `ctx.scheduler.*` calls. Mirrors RunXMessage but on its own type so
// dispatch is unambiguous.
interface SchedulerMessage {
  type: "scheduler";
  rpcId: number;
  op: "runAfter" | "runAt" | "cancel";
  payload: {
    delayMs?: number;
    timestamp?: number;
    ref?: { moduleName: string; exportName: string };
    args?: unknown;
    id?: string;
  };
}

/**
 * dispatchScheduler executes one `ctx.scheduler.*` op against the supplied
 * Sql handle. Resolves with the result that the worker's promise should
 * receive (a `ScheduledId` for `runAfter`/`runAt`, `null` for `cancel`).
 *
 * Phase 8.5: `sql` is the active mutation transaction handle when the
 * caller is a mutation (sqlFor(msg.txnRefId)) or the singleton pool
 * otherwise. The caller passes the right handle so this function stays
 * pool-agnostic and we can reuse the same SQL composition for both.
 */
async function dispatchScheduler(
  callerRuntimeID: string,
  msg: SchedulerMessage,
  sql: Sql,
): Promise<unknown> {
  const sep = callerRuntimeID.indexOf("__");
  if (sep < 0) {
    throw new Error("scheduler: caller runtime id missing project separator");
  }
  const projectID = callerRuntimeID.slice(0, sep);
  if (msg.op === "runAfter") {
    const delayMs = msg.payload.delayMs ?? 0;
    const ref = msg.payload.ref;
    if (!ref) throw new Error("scheduler.runAfter: missing ref");
    const args = msg.payload.args ?? {};
    const id = newId();
    const scheduledForMs = Date.now() + Math.max(0, delayMs);
    await sql`
      INSERT INTO excalibase_scheduled_functions
        (id, project_id, module_name, export_name, args, scheduled_for, status)
      VALUES
        (${id}, ${projectID}, ${ref.moduleName}, ${ref.exportName},
         ${sql.json(args as Record<string, unknown>)},
         to_timestamp(${scheduledForMs / 1000}), 'pending')
    `;
    return id;
  }
  if (msg.op === "runAt") {
    const ts = msg.payload.timestamp ?? Date.now();
    const ref = msg.payload.ref;
    if (!ref) throw new Error("scheduler.runAt: missing ref");
    const args = msg.payload.args ?? {};
    const id = newId();
    await sql`
      INSERT INTO excalibase_scheduled_functions
        (id, project_id, module_name, export_name, args, scheduled_for, status)
      VALUES
        (${id}, ${projectID}, ${ref.moduleName}, ${ref.exportName},
         ${sql.json(args as Record<string, unknown>)},
         to_timestamp(${ts / 1000}), 'pending')
    `;
    return id;
  }
  if (msg.op === "cancel") {
    const id = msg.payload.id;
    if (typeof id !== "string" || id.length === 0) {
      throw new Error("scheduler.cancel: id required");
    }
    await sql`
      UPDATE excalibase_scheduled_functions
         SET status = 'cancelled'
       WHERE id = ${id} AND status = 'pending'
    `;
    return null;
  }
  throw new Error(`scheduler: unknown op ${msg.op}`);
}

class FunctionRuntime {
  private readonly scripts = new Map<string, ScriptMetadata>();

  async deploy(req: DeployRequest): Promise<{ id: string; url: string }> {
    const { id, code, secrets = {} } = req;

    if (!id || !VALID_ID.test(id)) {
      throw new Error("Invalid function id");
    }
    if (!code || code.length > MAX_CODE_SIZE) {
      throw new Error(`code exceeds maximum size (${MAX_CODE_SIZE / 1024} KB)`);
    }
    if (this.scripts.size >= MAX_SCRIPTS && !this.scripts.has(id)) {
      throw new Error(`max scripts limit reached (${MAX_SCRIPTS})`);
    }

    // Replace any existing worker
    const existing = this.scripts.get(id);
    if (existing) existing.worker.terminate();

    const workerCode = buildWorkerCode(code, secrets);
    // TypeScript MIME type tells Deno to treat the blob as TS and strip type
    // annotations. Without this, user code with `req: Request` annotations
    // fails to parse as plain JS.
    const blob = new Blob([workerCode], { type: "application/typescript" });

    const netPermission: boolean | string[] =
      ALLOWED_HOSTS.length > 0 ? ALLOWED_HOSTS : false;

    const worker = new Worker(URL.createObjectURL(blob), {
      type: "module",
      // deno-lint-ignore no-explicit-any
      deno: {
        permissions: {
          net: netPermission,
          env: false,
          read: false,
          write: false,
          run: false,
          ffi: false,
        },
      },
    } as any);

    // Capture metadata reported during init. The worker emits it before
    // 'ready'; we buffer here and forward to provisioning after the
    // handshake so a failed HTTP callback never blocks the deploy.
    let initMetadataExports: unknown = null;

    // Wait for the worker's 'ready' message. Installs a temporary handler
    // that swaps to the permanent router on first 'ready'.
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        // Terminate the orphan so we don't leak it on init failure.
        try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on timeout:", terminateErr); }
        reject(new Error("worker init timeout"));
      }, WORKER_INIT_TIMEOUT_MS);
      worker.onmessage = (e) => {
        if (e.data?.type === "metadata") {
          initMetadataExports = e.data.exports;
          return;
        }
        if (e.data?.type === "ready") {
          clearTimeout(timeout);
          resolve();
        }
      };
      worker.onerror = (err) => {
        clearTimeout(timeout);
        try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on error:", terminateErr); }
        reject(new Error(err.message ?? "worker init error"));
      };
    });

    // Fire-and-forget forward — provisioning callback failures must never
    // affect deploy success. The Go side persists the payload on receipt.
    if (initMetadataExports != null && PROVISIONING_URL !== "") {
      forwardMetadataToProvisioning(id, initMetadataExports).catch((err) => {
        console.warn(`[runtime] metadata forward failed for ${id}:`, err);
      });
    }

    // Pull the kind off the metadata array (Phase 7 emits one entry per
    // worker — name=default, kind=<query|mutation|action|httpAction|
    // httpRouter>). Defaults to empty for legacy v1 fetch handlers.
    let detectedKind = "";
    if (Array.isArray(initMetadataExports) && initMetadataExports.length > 0) {
      const first = initMetadataExports[0] as { kind?: unknown };
      if (typeof first.kind === "string") detectedKind = first.kind;
    }
    const meta: ScriptMetadata = {
      id,
      worker,
      createdAt: new Date(),
      invocations: 0,
      pending: new Map(),
      nextReqId: 1,
      logs: [],
      dbCache: newCache(),
      kind: detectedKind,
    };
    this.scripts.set(id, meta);

    // Permanent message router — dispatches responses to pending requests by reqId
    // and appends log messages to the ring buffer. Installed AFTER init handshake
    // so the 'ready' message above lands on the temporary handler.
    worker.onmessage = (e) => {
      const msg = e.data;
      if (!msg) return;

      if (msg.type === "log") {
        const level = typeof msg.level === "string" ? msg.level : "log";
        const text = typeof msg.msg === "string" ? msg.msg : "";
        const ts = typeof msg.ts === "number" ? msg.ts : Date.now();
        meta.logs.push({ level, msg: text, ts });
        if (meta.logs.length > LOG_RING_SIZE) {
          meta.logs.splice(0, meta.logs.length - LOG_RING_SIZE);
        }
        return;
      }

      if (msg.type === "metadata") {
        // Late metadata (post-init). Forward as fire-and-forget — same
        // best-effort semantics as the init-time path.
        if (PROVISIONING_URL !== "") {
          forwardMetadataToProvisioning(id, msg.exports).catch((err) => {
            console.warn(`[runtime] late metadata forward failed for ${id}:`, err);
          });
        }
        return;
      }

      if (msg.type === "db") {
        // Async db op from the worker — execute against the pool (or the
        // active mutation txn, when one is registered) and post the result
        // back. Errors are normalised into a {ok:false,error} envelope so
        // the worker can reject the user's promise cleanly.
        const rpcId = msg.rpcId;
        if (typeof rpcId !== "number") return;
        (async () => {
          let result;
          try {
            // Phase 8.5: route through the shared mutation txn when this
            // invocation registered one. sqlFor falls back to the pool
            // for queries/actions/v1.
            const sql = sqlFor(msg.txnRefId as string | undefined);
            result = await executeDbOp(sql, meta.dbCache, msg as DbOp);
          } catch (err) {
            // executeDbOp wraps its own errors into {ok:false}, so we
            // should rarely land here. Anything that escapes is treated
            // as a non-retryable runtime error and forwarded as-is.
            result = {
              ok: false as const,
              error: String((err instanceof Error ? err.message : err) ?? "db error"),
            };
          }
          // Phase 9a: mark the txn conflicted when the result envelope
          // carries a retryable SQLSTATE. The outer retry loop checks
          // this on finalize() and re-invokes the handler.
          if (result && result.ok === false && typeof (result as { sqlState?: string }).sqlState === "string") {
            const code = (result as { sqlState: string }).sqlState;
            if (RETRYABLE_SQLSTATES.has(code)) {
              markConflict(msg.txnRefId as string | undefined, code, result.error);
            }
          }
          // Phase 9b.A — write tracking. Append the collection name to the
          // active txn's `writes` (mutations) or `reads` (everything else).
          // The op classification mirrors `DbOp.op`:
          //   writes: insert / insertMany / update / delete
          //   reads:  find / findOne / getById / count / search /
          //           vectorSearch / query
          // Skip when the entry is absent (v1 fetch handlers) or when the
          // op failed — a rolled-back/failed write never reaches commit
          // and therefore must not appear in CommitEvent.deps.
          if (result && result.ok === true) {
            const ref = msg.txnRefId as string | undefined;
            const entry = ref ? txnMap.get(ref) : undefined;
            if (entry) {
              const collection = msg.collection as string | undefined;
              if (typeof collection === "string" && collection.length > 0) {
                const isWrite = msg.op === "insert" || msg.op === "insertMany"
                  || msg.op === "update" || msg.op === "delete";
                if (isWrite) {
                  entry.writes.add(collection);
                } else {
                  // Track query plan reads via `plan.collection` — the
                  // worker passes `collection` redundantly so we don't have
                  // to dig into the plan here. Defensive against future op
                  // shapes that omit `collection`.
                  entry.reads.add(collection);
                }
              }
            }
          }
          try {
            worker.postMessage({ type: "dbResult", rpcId, result });
          } catch (postErr) {
            // Worker is gone — nothing to do; the pending invoke will
            // surface a timeout instead.
            console.debug("[runtime] dbResult postMessage failed:", postErr);
          }
        })();
        return;
      }

      if (msg.type === "runX") {
        // Phase 7: ctx.runQuery/runMutation/runAction RPC from the worker.
        // Resolve the target fn against the runtime's local script table
        // (in-process composition — same project, sibling fn) and forward
        // the args through the regular invoke pipeline so validation,
        // ctx wiring, and error semantics match a direct call.
        //
        // Cross-runtime composition (target in a different deno-runtime
        // pod) falls back to an HTTP self-call to the gateway's
        // /internal/invoke route. Out of scope for the in-process tests;
        // the gateway side already supports the route.
        const rpcId = msg.rpcId;
        if (typeof rpcId !== "number") return;
        (async () => {
          let result: { ok: true; data: unknown } | { ok: false; error: string; errorName?: string; issues?: unknown };
          try {
            const data = await dispatchRunX(meta.id, msg as RunXMessage);
            result = { ok: true, data };
          } catch (err) {
            const issues = (err as { issues?: unknown }).issues;
            const errorName = (err as { name?: string }).name;
            result = {
              ok: false,
              error: String((err instanceof Error ? err.message : err) ?? "runX error"),
              ...(errorName ? { errorName } : {}),
              ...(issues ? { issues } : {}),
            };
          }
          try {
            worker.postMessage({ type: "runXResult", rpcId, result });
          } catch (postErr) {
            console.debug("[runtime] runXResult postMessage failed:", postErr);
          }
        })();
        return;
      }

      if (msg.type === "scheduler") {
        // Phase 8: ctx.scheduler.{runAfter,runAt,cancel}.
        //
        // Phase 8.5: route the INSERT/UPDATE through the active mutation
        // txn when the caller is a mutation (txnMap has an entry for the
        // worker's txnRefId). Action handlers carry no txnRefId, so they
        // route through the pool and commit independently — Convex parity.
        const rpcId = msg.rpcId;
        if (typeof rpcId !== "number") return;
        (async () => {
          let result: { ok: true; data: unknown } | { ok: false; error: string };
          try {
            const sql = sqlFor(msg.txnRefId as string | undefined);
            const data = await dispatchScheduler(meta.id, msg, sql);
            result = { ok: true, data };
          } catch (err) {
            // Phase 9a: scheduler INSERTs ride the mutation txn under
            // SERIALIZABLE, so SSI can raise 40001 here too. Mark the
            // conflict so finalize() will retry, just like db ops.
            const code = (err as { code?: unknown }).code;
            if (typeof code === "string" && RETRYABLE_SQLSTATES.has(code)) {
              markConflict(msg.txnRefId as string | undefined, code,
                err instanceof Error ? err.message : String(err));
            }
            result = {
              ok: false,
              error: String((err instanceof Error ? err.message : err) ?? "scheduler error"),
            };
          }
          try {
            worker.postMessage({ type: "schedulerResult", rpcId, result });
          } catch (postErr) {
            console.debug("[runtime] schedulerResult postMessage failed:", postErr);
          }
        })();
        return;
      }

      if (typeof msg.reqId !== "number") return;
      const pending = meta.pending.get(msg.reqId);
      if (!pending) return; // late delivery after timeout — ignore
      meta.pending.delete(msg.reqId);
      clearTimeout(pending.timeout);
      metrics.invocationsTotal++;
      if (msg.type === "success") {
        pending.resolve({
          status: msg.status || 200,
          headers: msg.headers || {},
          body: msg.body || "",
        });
      } else {
        metrics.invocationsError++;
        pending.reject(new Error(msg.error || "worker error"));
      }
    };

    // Permanent error handler — fail every in-flight request and tear down
    // the dead worker so the next deploy can replace it cleanly.
    worker.onerror = (err) => {
      console.error(`[runtime] worker ${id} crashed: ${err.message || "unknown"}`);
      for (const [reqId, p] of meta.pending) {
        clearTimeout(p.timeout);
        p.reject(new Error(`worker crashed: ${err.message || "unknown"}`));
        meta.pending.delete(reqId);
      }
      try { worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on crash:", terminateErr); }
      this.scripts.delete(id);
    };

    metrics.deploysTotal++;
    console.log(`[runtime] deployed ${id} (${Object.keys(secrets).length} secrets)`);
    return { id, url: `/invoke/${id}` };
  }

  async invoke(id: string, req: InvokeRequest): Promise<InvokeResponse> {
    const script = this.scripts.get(id);
    if (!script) throw new Error(`function not found: ${id}`);

    // Phase 8.5: a nested invocation rides the parent's txn — never opens
    // its own. Detect that path up front so the retry loop below skips for
    // nested calls (the parent owns the retry boundary).
    const inheritedTxnRefId = extractInheritedTxnRefId(req);
    const isNested = inheritedTxnRefId !== "" && txnMap.has(inheritedTxnRefId);

    // Only TOP-LEVEL MUTATIONS retry. Queries, actions, http*, v1 handlers,
    // and nested-mutation aliases all run exactly once. This matches Convex
    // semantics: actions don't retry (they can have external side effects),
    // and nested mutations re-execute naturally when the outer parent's
    // retry replays the entire handler chain.
    if (script.kind !== "mutation" || isNested) {
      return await this.invokeOnce(script, req, inheritedTxnRefId);
    }

    // Top-level mutation retry loop.
    let attempt = 0;
    let lastConflict: { sqlState: string; message: string } | null = null;
    while (attempt < MUTATION_RETRY_MAX) {
      attempt++;
      const outcome = await this.invokeOnce(script, req, inheritedTxnRefId);
      // invokeOnce surfaces a retryable conflict via the sentinel object
      // (encoded into a private property so InvokeResponse stays clean).
      // Any other outcome (success OR non-retryable error) is returned as-is.
      const conflict = (outcome as InvokeResponse & { __conflict?: { sqlState: string; message: string } }).__conflict;
      if (!conflict) return outcome;
      lastConflict = conflict;
      metrics.mutationRetriesTotal++;
      if (attempt < MUTATION_RETRY_MAX) {
        const backoff = backoffForAttempt(attempt);
        console.log(
          `[runtime] mutation ${id} conflict on attempt ${attempt} ` +
          `(SQLSTATE ${conflict.sqlState}); retrying in ${backoff}ms`,
        );
        await new Promise((resolve) => setTimeout(resolve, backoff));
        continue;
      }
      // Exhausted — synthesise the ConflictError envelope and surface as 409.
      console.error(
        `[runtime] mutation ${id} exhausted ${MUTATION_RETRY_MAX} attempts ` +
        `on SQLSTATE ${conflict.sqlState}: ${conflict.message}`,
      );
      return {
        status: 409,
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          error: "MUTATION_CONFLICT",
          attempts: attempt,
          code: conflict.sqlState,
          message: conflict.message,
        }),
      };
    }
    // Defensive — unreachable because the loop always returns or sets
    // lastConflict before exhausting. Surfaces as a 500 if we get here.
    return {
      status: 500,
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        error: "internal: retry loop exited without resolution",
        lastConflict,
      }),
    };
  }

  /**
   * Run the worker exactly once, opening/closing the txn around it.
   * Top-level mutations may be retried by the caller; everything else
   * runs exactly once. On a retryable conflict, returns an InvokeResponse
   * with a `__conflict` discriminator so the caller can choose to retry.
   *
   * Phase 9b.A: the returned envelope carries `__reactiveReads` /
   * `__reactiveWrites` so `invokeWithReads` (the SubscriptionRegistry's
   * entry point) can pull them out without re-walking txnMap. Both are
   * always set — empty sets when no entry was allocated (v1 fetch
   * handlers) so callers can iterate without null checks.
   */
  private async invokeOnce(
    script: ScriptMetadata,
    req: InvokeRequest,
    inheritedTxnRefId: string,
  ): Promise<InvokeResponse & {
    __conflict?: { sqlState: string; message: string };
    __reactiveReads?: Set<string>;
    __reactiveWrites?: Set<string>;
  }> {
    const id = script.id;
    script.invocations++;
    const reqId = script.nextReqId++;

    // Phase 9b.A — parse projectId / fnId for CommitEvent payloads. The
    // script id is `${projectId}__${fnId}`; v1 fetch handlers don't follow
    // this shape but they also don't open mutations so the empty fallback
    // is safe.
    const sep = id.indexOf("__");
    const projectId = sep > 0 ? id.slice(0, sep) : "";
    const fnId = sep > 0 ? id.slice(sep + 2) : id;

    let activeTxnRefId = "";
    if (inheritedTxnRefId && txnMap.has(inheritedTxnRefId)) {
      // Alias entry — reuse the parent's connection, owned=false so we
      // don't double-commit. A new key keeps the map entry per-invocation
      // (so we can clear it on return without touching the parent).
      activeTxnRefId = newTxnRefId();
      const parent = txnMap.get(inheritedTxnRefId)!;
      txnMap.set(activeTxnRefId, {
        sql: parent.sql,
        owned: false,
        parentRefId: inheritedTxnRefId,
        openedAt: Date.now(),
        kind: script.kind,
        fnId,
        projectId,
        writes: new Set(),
        reads: new Set(),
      });
    } else if (script.kind === "mutation") {
      // Top-level mutation — open a fresh transaction. If the pool isn't
      // configured (EXCALIBASE_DB_URL unset, e.g. tests that don't touch
      // ctx.db), fall back to no-txn mode: the handler can still run, and
      // any db op it does attempt will surface its own error. This keeps
      // mutations that never call ctx.db / ctx.scheduler working.
      try {
        const txn = await openTxn();
        activeTxnRefId = newTxnRefId();
        txnMap.set(activeTxnRefId, {
          sql: txn,
          owned: true,
          openedAt: Date.now(),
          kind: "mutation",
          fnId,
          projectId,
          writes: new Set(),
          reads: new Set(),
        });
      } catch (err) {
        console.debug(`[runtime] mutation ${id} runs without txn:`,
          err instanceof Error ? err.message : err);
      }
    } else if (script.kind === "query") {
      // Phase 9b.A — queries get a tracking-only entry (sql:null) so the
      // db RPC dispatch can record `reads` for the SubscriptionRegistry.
      // No transaction is opened; `sqlFor()` falls back to the pool. The
      // dispatch site never tries to commit/rollback when owned=false AND
      // sql=null, so the entry is purely a side-table for collections
      // touched during this invocation.
      activeTxnRefId = newTxnRefId();
      txnMap.set(activeTxnRefId, {
        sql: null,
        owned: false,
        openedAt: Date.now(),
        kind: "query",
        fnId,
        projectId,
        writes: new Set(),
        reads: new Set(),
      });
    }

    // Captured for the caller — invokeWithReads pulls the entry's reads/
    // writes BEFORE finalize deletes the txnMap entry. Defaults to empty
    // sets so callers can always iterate even when no entry was allocated
    // (e.g. v1 fetch handlers).
    let capturedReads: Set<string> = new Set();
    let capturedWrites: Set<string> = new Set();
    // finalize closes the txn. On success path it tries to COMMIT and
    // surfaces any retryable conflict back to the retry loop via a
    // returned sentinel. On error path it rolls back. Either way, the
    // entry is removed from txnMap so a redeployed worker can't see
    // stale txn refs.
    //
    // Phase 9b.A: on the success path for top-level mutations, we capture
    // `writes` into a local set BEFORE deleting the entry, run COMMIT, and
    // — only if COMMIT succeeded — fire `emitCommit({...deps: [...writes]})`.
    // Rolled-back txns and non-success finalize paths emit nothing because
    // the writes never happened on the wire.
    const finalize = async (
      settled: "success" | "error",
    ): Promise<{ sqlState: string; message: string } | null> => {
      if (!activeTxnRefId) return null;
      const entry = txnMap.get(activeTxnRefId);
      txnMap.delete(activeTxnRefId);
      if (!entry) return null;
      // Capture reads/writes before the entry goes out of scope. Used by
      // `invokeWithReads` for the SubscriptionRegistry.
      capturedReads = entry.reads;
      capturedWrites = entry.writes;
      // Tracking-only entries (queries, aliases) — no txn to commit.
      if (!entry.owned || !entry.sql) return null;
      // A mid-flight db op may have stamped a conflict marker. In that
      // case we go straight to rollback (commit would fail anyway with
      // the same SQLSTATE) and report the conflict to the retry loop.
      if (entry.conflict && settled !== "success") {
        await rollbackTxn(entry.sql);
        return entry.conflict;
      }
      if (entry.conflict && settled === "success") {
        // Handler returned success but a prior db op was a retryable
        // conflict — the handler swallowed the error. Roll back and
        // surface the conflict; retrying the handler is safer than
        // letting the (likely-incomplete) "success" return commit.
        await rollbackTxn(entry.sql);
        return entry.conflict;
      }
      if (settled === "success") {
        try {
          await commitTxn(entry.sql);
          // Phase 9b.A: emit CommitEvent ONLY after a successful commit.
          // A rolled-back or conflict-surfaced txn never reaches here.
          if (entry.kind === "mutation" && entry.writes.size > 0) {
            emitCommit({
              projectId: entry.projectId,
              fnId: entry.fnId,
              runtimeId: id,
              deps: [...entry.writes],
              ts: Date.now(),
            });
          }
          return null;
        } catch (commitErr) {
          if (commitErr instanceof MutationConflict) {
            // COMMIT itself raised 40001/40P01 — classic SSI surface.
            // The connection is already released inside commitTxn.
            return { sqlState: commitErr.sqlState, message: commitErr.pgMessage };
          }
          console.warn(`[runtime] commit failed for ${id}:`, commitErr);
          return null;
        }
      }
      await rollbackTxn(entry.sql);
      return null;
    };

    let res: InvokeResponse;
    try {
      res = await new Promise<InvokeResponse>((resolve, reject) => {
        const timeout = setTimeout(() => {
          script.pending.delete(reqId);
          metrics.invocationsTotal++;
          metrics.invocationsError++;
          metrics.timeoutsTotal++;
          reject(new Error(`execution timeout (${INVOKE_TIMEOUT_MS / 1000}s)`));

          // If this was the last pending request and nothing succeeded since
          // the timeout fired, the worker is likely stuck (infinite loop, deadlock).
          // Terminate it to reclaim resources. The function can be redeployed
          // on the next deploy call.
          if (script.pending.size === 0) {
            console.error(`[runtime] terminating stuck worker ${id} (no pending requests after timeout)`);
            try { script.worker.terminate(); } catch (terminateErr) { console.debug("[runtime] terminate on invoke timeout:", terminateErr); }
            this.scripts.delete(id);
          }
        }, INVOKE_TIMEOUT_MS);
        script.pending.set(reqId, { resolve, reject, timeout });
        script.worker.postMessage({ type: "invoke", reqId, data: req, txnRefId: activeTxnRefId });
      });
    } catch (err) {
      await finalize("error");
      throw err;
    }

    // Phase 8.5: a v2 handler that threw is reported as a 200 envelope from
    // the worker with `{error: ...}` in the body (see __dispatchV2's
    // self.postMessage on the catch branches). Treat any non-2xx status,
    // or any 200 whose body has a top-level `error` field, as a rollback
    // trigger for the txn.
    let isError = res.status < 200 || res.status >= 300;
    if (!isError && res.body) {
      try {
        const parsed = JSON.parse(res.body) as { error?: unknown };
        if (parsed && typeof parsed.error === "string") isError = true;
      } catch (_) { /* non-JSON body — treat as success */ }
    }
    const conflict = await finalize(isError ? "error" : "success");
    if (conflict) {
      // Encode the conflict into the InvokeResponse so the retry loop
      // upstairs can pick it up without changing the public shape on
      // success paths.
      return Object.assign({}, res, {
        __conflict: conflict,
        __reactiveReads: capturedReads,
        __reactiveWrites: capturedWrites,
      });
    }
    return Object.assign({}, res, {
      __reactiveReads: capturedReads,
      __reactiveWrites: capturedWrites,
    });
  }

  /**
   * Phase 9b.A — invoke a deployed function and return both its response
   * AND the set of collections it read during execution. Used by the
   * SubscriptionRegistry for register / re-execution paths. The body
   * envelope here is the v2 `{args}` payload — same shape `dispatchRunX`
   * uses for nested calls.
   *
   * Unlike `invoke`, this never goes through the mutation retry loop
   * (subscriptions only target queries) so it's a thin wrapper around
   * `invokeOnce`. If a caller mistakenly registers a mutation as a
   * subscription, we still invoke it; the registry layer decides whether
   * to accept it.
   */
  async invokeWithReads(id: string, body: unknown): Promise<{
    status: number;
    data: unknown;
    error?: string;
    reads: Set<string>;
  }> {
    const script = this.scripts.get(id);
    if (!script) {
      return { status: 404, data: null, error: `function not found: ${id}`, reads: new Set() };
    }
    const invokeReq: InvokeRequest = {
      method: "POST",
      url: `/invoke/${id}`,
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ args: body ?? {}, runDepth: 0 }),
    };
    const outcome = await this.invokeOnce(script, invokeReq, "");
    const reads = outcome.__reactiveReads ?? new Set<string>();
    let data: unknown = null;
    let error: string | undefined;
    if (outcome.body) {
      try {
        const parsed = JSON.parse(outcome.body) as { data?: unknown; error?: unknown };
        if (parsed && typeof parsed === "object") {
          if (typeof parsed.error === "string") error = parsed.error;
          if ("data" in parsed) data = parsed.data;
        }
      } catch (_) {
        error = "non-JSON body";
      }
    }
    return { status: outcome.status, data, error, reads };
  }

  delete(id: string): boolean {
    const script = this.scripts.get(id);
    if (!script) return false;
    script.worker.terminate();
    this.scripts.delete(id);
    return true;
  }

  /**
   * Returns the recorded export kind for a deployed function id, or the
   * empty string when the function is unknown or its metadata never
   * arrived. Phase 8 read-only enforcement consults this to validate
   * ctx.runX targets against the caller's kind.
   */
  getKind(id: string): string {
    return this.scripts.get(id)?.kind ?? "";
  }

  list() {
    return Array.from(this.scripts.values()).map((s) => ({
      id: s.id,
      invocations: s.invocations,
      uptime: Date.now() - s.createdAt.getTime(),
      pending: s.pending.size,
    }));
  }

  // getLogs returns the ring buffer for a function, optionally filtered to
  // entries strictly newer than the given timestamp (milliseconds). If the
  // function doesn't exist, returns null so the caller can return 404.
  getLogs(id: string, sinceMs?: number): LogEntry[] | null {
    const script = this.scripts.get(id);
    if (!script) return null;
    if (typeof sinceMs === "number" && Number.isFinite(sinceMs)) {
      return script.logs.filter((l) => l.ts > sinceMs);
    }
    return script.logs.slice();
  }

  stats() {
    return {
      totalScripts: this.scripts.size,
      maxScripts: MAX_SCRIPTS,
      scripts: this.list(),
    };
  }
}

// --- Metrics (Prometheus text format) ---
// Simple counters tracked globally. No histogram (adds complexity for minimal value
// at this scale). Operators who need percentiles should use the /logs endpoint or
// instrument upstream in the Go handler.
const metrics = {
  invocationsTotal: 0,
  invocationsError: 0,
  deploysTotal: 0,
  timeoutsTotal: 0,
  /**
   * Phase 9a: total retry attempts triggered by 40001/40P01 across all
   * mutations. Each retry is one event; an exhausted retry-loop adds
   * `MUTATION_RETRY_MAX - 1` to this counter and yields a 409. A
   * surge here usually means under-isolated workload or a missing
   * predicate-supporting index — page operations.
   */
  mutationRetriesTotal: 0,
};

/**
 * Phase 9a: parse the per-invocation txnRefId out of a v2 request body.
 * The body is JSON `{args, txnRefId?, runDepth?}`. Anything else (non-JSON,
 * missing field) means "no parent txn" — caller is top-level.
 */
function extractInheritedTxnRefId(req: InvokeRequest): string {
  if (typeof req.body !== "string" || req.body.length === 0) return "";
  try {
    const parsed = JSON.parse(req.body) as { txnRefId?: unknown };
    if (typeof parsed.txnRefId === "string" && parsed.txnRefId.length > 0) {
      return parsed.txnRefId;
    }
  } catch (_) { /* not JSON — top-level path */ }
  return "";
}

/**
 * Phase 9a: exponential backoff with jitter. Formula:
 *   base * 2^(attempt-1) * (0.5 + random())
 * capped at MUTATION_RETRY_BACKOFF_CAP_MS. attempt is 1-indexed —
 * attempt=1 means "we just finished attempt 1, sleep before attempt 2".
 *
 * Jitter avoids retry storms where every contending mutation wakes at
 * the same moment and re-enters the same SSI race.
 */
function backoffForAttempt(attempt: number): number {
  const exp = MUTATION_RETRY_BACKOFF_MS * Math.pow(2, attempt - 1);
  const jittered = exp * (0.5 + Math.random());
  return Math.min(MUTATION_RETRY_BACKOFF_CAP_MS, Math.floor(jittered));
}

const runtime = new FunctionRuntime();

// Phase 9b.A — single-replica reactive subscriptions. The registry holds
// per-conn subscription state and re-executes query functions when a
// committed mutation's `deps` intersects a subscription's tracked reads.
// Wired to `onCommit` once at startup; Phase 9b.B will additionally wire
// a NATS publisher onto the same emitter so cross-replica fan-out works
// without changing this surface.
const reactiveRegistry = new SubscriptionRegistry({
  invokeWithReads: (id, args) => runtime.invokeWithReads(id, args),
});
onCommit((event) => reactiveRegistry.dispatchCommit(event));

/**
 * Best-effort callback: POST captured v2 export metadata back to the Go
 * provisioning service. The runtime id is `${projectId}__${fnId}`; the Go
 * route is `/internal/runtime/functions/{fnId}/metadata`, so we split.
 * Auth: X-Excalibase-Runtime-Token shared with the deploy/invoke RPCs.
 *
 * Failures are logged and swallowed — a deploy never blocks on metadata
 * capture, and Phase 3 codegen tolerates a missing exports array.
 */
async function forwardMetadataToProvisioning(runtimeID: string, exports: unknown): Promise<void> {
  if (PROVISIONING_URL === "") return;
  const sep = runtimeID.indexOf("__");
  if (sep < 0) {
    console.warn(`[runtime] metadata forward: runtime id ${runtimeID} missing __ separator`);
    return;
  }
  const projectId = runtimeID.slice(0, sep);
  const fnId = runtimeID.slice(sep + 2);
  const url = `${PROVISIONING_URL.replace(/\/$/, "")}/internal/runtime/functions/${fnId}/metadata`;
  const res = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Excalibase-Runtime-Token": RUNTIME_SECRET,
    },
    body: JSON.stringify({ projectId, exports }),
  });
  // Drain the body so the connection can be reused.
  await res.body?.cancel();
  if (!res.ok) {
    console.warn(`[runtime] metadata forward HTTP ${res.status} for ${runtimeID}`);
  }
}

const JSON_HEADERS: Record<string, string> = { "Content-Type": "application/json" };

// checkContentLength refuses oversized requests upfront via Content-Length,
// before the body is read into memory. Returns a 413 Response on overflow.
function checkContentLength(req: Request, max: number): Response | null {
  const cl = req.headers.get("content-length");
  if (cl) {
    const n = Number(cl);
    if (Number.isFinite(n) && n > max) {
      return Response.json({ error: "payload too large" }, { status: 413, headers: JSON_HEADERS });
    }
  }
  return null;
}

function notFound(): Response {
  return Response.json({ error: "not found" }, { status: 404, headers: JSON_HEADERS });
}

function badRequest(msg: string): Response {
  return Response.json({ error: msg }, { status: 400, headers: JSON_HEADERS });
}

async function handleHealth(): Promise<Response> {
  return Response.json(
    { status: "healthy", scripts: runtime.stats().totalScripts, uptime: performance.now() },
    { headers: JSON_HEADERS },
  );
}

async function handleDeploy(req: Request): Promise<Response> {
  const deployMax = MAX_CODE_SIZE + 16 * 1024;
  const tooBig = checkContentLength(req, deployMax);
  if (tooBig) return tooBig;
  const body = await req.text();
  if (body.length > deployMax) {
    return Response.json({ error: "payload too large" }, { status: 413, headers: JSON_HEADERS });
  }
  const parsed = JSON.parse(body) as DeployRequest;
  if (!parsed.id || !parsed.code) {
    return badRequest("missing id or code");
  }
  const result = await runtime.deploy(parsed);
  return Response.json(result, { status: 201, headers: JSON_HEADERS });
}

async function handleInvoke(req: Request, id: string): Promise<Response> {
  if (!VALID_ID.test(id)) return badRequest("invalid function id");
  const tooBig = checkContentLength(req, MAX_INVOKE_BODY);
  if (tooBig) return tooBig;
  const body = await req.text();
  if (body.length > MAX_INVOKE_BODY) {
    return Response.json({ error: "payload too large" }, { status: 413, headers: JSON_HEADERS });
  }
  const invokeReq = JSON.parse(body) as InvokeRequest;
  const result = await runtime.invoke(id, invokeReq);
  return Response.json(result, { headers: JSON_HEADERS });
}

function handleLogs(url: URL, id: string): Response {
  if (!VALID_ID.test(id)) return badRequest("invalid function id");
  const sinceRaw = url.searchParams.get("since");
  const sinceMs = sinceRaw ? Number(sinceRaw) : undefined;
  const logs = runtime.getLogs(id, sinceMs);
  if (logs === null) return notFound();
  return Response.json({ logs }, { headers: JSON_HEADERS });
}

function handleDelete(id: string): Response {
  if (!VALID_ID.test(id)) return badRequest("invalid function id");
  return runtime.delete(id)
    ? Response.json({ status: "deleted", id }, { headers: JSON_HEADERS })
    : notFound();
}

function handleMetrics(): Response {
  const stats = runtime.stats();
  const lines = [
    "# HELP excalibase_fn_scripts_active Number of deployed function workers",
    "# TYPE excalibase_fn_scripts_active gauge",
    `excalibase_fn_scripts_active ${stats.totalScripts}`,
    "",
    "# HELP excalibase_fn_scripts_max Maximum number of function workers",
    "# TYPE excalibase_fn_scripts_max gauge",
    `excalibase_fn_scripts_max ${stats.maxScripts}`,
    "",
    "# HELP excalibase_fn_invocations_total Total function invocations",
    "# TYPE excalibase_fn_invocations_total counter",
    `excalibase_fn_invocations_total ${metrics.invocationsTotal}`,
    "",
    "# HELP excalibase_fn_invocations_errors_total Total failed invocations",
    "# TYPE excalibase_fn_invocations_errors_total counter",
    `excalibase_fn_invocations_errors_total ${metrics.invocationsError}`,
    "",
    "# HELP excalibase_fn_deploys_total Total function deployments",
    "# TYPE excalibase_fn_deploys_total counter",
    `excalibase_fn_deploys_total ${metrics.deploysTotal}`,
    "",
    "# HELP excalibase_fn_timeouts_total Total invocation timeouts",
    "# TYPE excalibase_fn_timeouts_total counter",
    `excalibase_fn_timeouts_total ${metrics.timeoutsTotal}`,
    "",
    "# HELP excalibase_mutation_retries_total Total mutation retry attempts on 40001/40P01",
    "# TYPE excalibase_mutation_retries_total counter",
    `excalibase_mutation_retries_total ${metrics.mutationRetriesTotal}`,
    "",
    "# HELP excalibase_fn_uptime_seconds Runtime uptime in seconds",
    "# TYPE excalibase_fn_uptime_seconds gauge",
    `excalibase_fn_uptime_seconds ${Math.floor(performance.now() / 1000)}`,
    "",
  ];
  return new Response(lines.join("\n"), {
    status: 200,
    headers: { "Content-Type": "text/plain; version=0.0.4; charset=utf-8" },
  });
}

function isAuthorized(req: Request, url: URL): boolean {
  if (url.pathname === "/health") return true;
  const provided = req.headers.get("X-Runtime-Secret") ?? "";
  return constantTimeEqual(provided, RUNTIME_SECRET);
}

// dispatch matches a request to the right route handler. Each handler is a
// thin wrapper so the dispatcher itself stays under Sonar's complexity limit.
async function dispatch(req: Request, url: URL): Promise<Response> {
  if (url.pathname === "/health") return handleHealth();
  if (url.pathname === "/deploy" && req.method === "POST") return handleDeploy(req);
  if (url.pathname.startsWith("/invoke/") && req.method === "POST") {
    return handleInvoke(req, decodeURIComponent(url.pathname.slice("/invoke/".length)));
  }
  if (url.pathname.startsWith("/logs/") && req.method === "GET") {
    return handleLogs(url, decodeURIComponent(url.pathname.slice("/logs/".length)));
  }
  if (url.pathname.startsWith("/delete/") && req.method === "DELETE") {
    return handleDelete(decodeURIComponent(url.pathname.slice("/delete/".length)));
  }
  if (url.pathname === "/scripts") return Response.json({ scripts: runtime.list() }, { headers: JSON_HEADERS });
  if (url.pathname === "/stats") return Response.json(runtime.stats(), { headers: JSON_HEADERS });
  if (url.pathname === "/metrics") return handleMetrics();
  // Phase 9b.A debug endpoints — tests use these to assert internal state
  // without coupling to log scraping. NOT part of the public API; gated
  // behind the same X-Runtime-Secret check as everything else.
  if (url.pathname === "/reactive/debug") {
    return Response.json({ subscriptions: reactiveRegistry.size }, { headers: JSON_HEADERS });
  }
  if (url.pathname === "/reactive/debug/last-commit") {
    return Response.json(reactiveRegistry.getLastCommit() ?? {}, { headers: JSON_HEADERS });
  }
  return notFound();
}

Deno.serve({ port: PORT }, async (req: Request) => {
  const url = new URL(req.url);
  if (req.method === "OPTIONS") {
    return new Response(null, { status: 204, headers: JSON_HEADERS });
  }
  if (!isAuthorized(req, url)) {
    return Response.json({ error: "forbidden" }, { status: 403, headers: JSON_HEADERS });
  }
  try {
    return await dispatch(req, url);
  } catch (error: unknown) {
    // Never leak stack traces
    const msg = String((error instanceof Error ? error.message : null) ?? "internal error").slice(0, 500);
    return Response.json({ error: msg }, { status: 500, headers: JSON_HEADERS });
  }
});

// Phase 9b.A — sibling WebSocket listener.
//
// The Go gateway (`server-go/internal/handler/function.go::forwardToRuntime`)
// is HTTP-only — it has no WS-upgrade path. Rather than retrofit a WS
// proxy in Go for this phase, we open a SECOND port on the Deno runtime
// dedicated to reactive subscriptions. Clients (and eventually the SDK in
// Phase 9b.C) connect directly to this port; the Go gateway sits in front
// only for regular function invokes.
//
// Default port 8801, overridable via EXCALIBASE_DENO_WS_PORT. When unset,
// the listener is skipped entirely — older deployments that don't expose
// the WS port keep working without reactive support.
const WS_PORT_RAW = Deno.env.get("EXCALIBASE_DENO_WS_PORT") || "";
if (WS_PORT_RAW !== "") {
  const wsPort = Number(WS_PORT_RAW);
  if (!Number.isFinite(wsPort) || wsPort <= 0 || wsPort > 65535) {
    console.error(`FATAL: EXCALIBASE_DENO_WS_PORT="${WS_PORT_RAW}" is not a valid port`);
    Deno.exit(1);
  }
  Deno.serve({ port: wsPort }, (req: Request) => {
    // No X-Runtime-Secret auth here — WS clients are EXTERNAL (browsers /
    // SDKs), unlike the HTTP port which only the gateway should reach.
    // Auth lives in the JWT query param consumed by `tryUpgradeWatchSocket`.
    const upgraded = tryUpgradeWatchSocket(req, reactiveRegistry);
    if (upgraded) return upgraded;
    return new Response("not found", { status: 404 });
  });
  console.log(`[runtime] reactive WS listener on :${wsPort}`);
}

// Drain the postgres pool on SIGTERM/SIGINT so containers shut down cleanly.
// The handlers are best-effort; if Deno exits before they finish, postgres
// will close the sockets anyway.
const shutdown = async () => {
  try { await closePool(); } catch (_) { /* ignore */ }
  Deno.exit(0);
};
try { Deno.addSignalListener("SIGTERM", shutdown); } catch (_) { /* not all platforms */ }
try { Deno.addSignalListener("SIGINT", shutdown); } catch (_) { /* not all platforms */ }

console.log(`Excalibase Deno runtime on :${PORT}${V2_ENABLED ? " (functions v2 enabled)" : ""}`);

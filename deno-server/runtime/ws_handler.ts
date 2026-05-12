// Phase 9b.A — WebSocket handler for `/functions/v1/{projectId}/_watch`.
//
// Protocol (all frames are JSON text over the standard WS frame layer):
//
//   Inbound:
//     { op: "subscribe",   subId, ref: {moduleName, exportName}, args }
//     { op: "unsubscribe", subId }
//     { op: "ping" }
//   Outbound:
//     { op: "result", subId, data, pageStatus? }
//     { op: "error",  subId, code, message }
//     { op: "pong" }
//
// Subprotocol header (RFC 6455 §1.9): `excalibase-fn-v1`. The client MUST
// send this in the Sec-WebSocket-Protocol header; the server echoes it.
// Future protocol revisions will allocate a new subprotocol so old clients
// fail to negotiate rather than silently misbehave.
//
// Auth: the gateway pre-validated the JWT before forwarding HTTP-side
// traffic, but the WebSocket handshake reaches Deno directly (sibling
// port). Browsers can't set headers on `new WebSocket(...)`, so the JWT
// rides as a `?token=...` query string. We base64url-decode the payload
// to attach the claims to the conn record; signature verification is the
// Go gateway's job for HTTP and SHOULD be added here in Phase 9b.D when
// the SDK is wired (this phase has no SDK — only test traffic touches
// this surface, and tests use `makeUnsignedJwt`).
//
// Heartbeat: server sends `{op:"ping"}` every 30s. The client must reply
// with `{op:"pong"}`; if 90s elapse with no pong, the server closes the
// conn with code 1008 (policy violation).

import { SubscriptionRegistry, type FunctionRef } from "./reactive.ts";

// Subprotocol name. Picked to be specific enough that a different reactive
// protocol can co-exist behind the same handler in the future.
export const WS_SUBPROTOCOL = "excalibase-fn-v1";

// Heartbeat tunables. Both are well under typical 5-minute idle timeouts
// of load balancers (Envoy default 1h, AWS NLB 350s) so a long-lived
// subscription survives transient quiet periods.
const PING_INTERVAL_MS = 30_000;
const PONG_TIMEOUT_MS = 90_000;

interface ConnState {
  ws: WebSocket;
  /** Last `{op:"pong"}` arrival timestamp; updated on every pong. */
  lastPong: number;
  /** Cleared on close so we don't leak intervals. */
  pingInterval: number;
  /** Decoded JWT claims (or null when token missing/malformed). */
  claims: Record<string, unknown> | null;
  /** Stable adapter passed to SubscriptionRegistry as WsLike. */
  wsLike: { send: (s: string) => void; isClosed: () => boolean };
}

/**
 * base64url-decode the JWT payload. Returns null on a malformed token.
 * Exported for tests; production callers use `tryUpgradeWatchSocket`.
 */
export function decodeJwtClaims(token: string): Record<string, unknown> | null {
  if (typeof token !== "string" || token.length === 0) return null;
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  try {
    let p = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    const pad = p.length % 4;
    if (pad === 2) p += "==";
    else if (pad === 3) p += "=";
    else if (pad !== 0) return null;
    const json = atob(p);
    const parsed = JSON.parse(json);
    return (parsed && typeof parsed === "object") ? (parsed as Record<string, unknown>) : null;
  } catch (_) {
    return null;
  }
}

/**
 * Pull the projectId out of `/functions/v1/{projectId}/_watch`. Returns
 * null on any other path. Matched explicitly here so the dispatcher can
 * decide whether to upgrade or refuse.
 */
export function extractProjectIdFromWatchPath(pathname: string): string | null {
  const m = /^\/functions\/v1\/([a-zA-Z0-9_-]{1,128})\/_watch$/.exec(pathname);
  return m ? m[1] : null;
}

/**
 * Try to upgrade an HTTP request into a WebSocket bound to the reactive
 * registry. Returns null when the URL doesn't match — the caller falls
 * back to the regular HTTP dispatch.
 */
export function tryUpgradeWatchSocket(
  req: Request,
  registry: SubscriptionRegistry,
): Response | null {
  const url = new URL(req.url);
  const projectId = extractProjectIdFromWatchPath(url.pathname);
  if (projectId === null) return null;
  // Only GET is allowed for WS upgrade; reject other verbs explicitly so
  // a misconfigured client gets a clear error.
  if (req.method !== "GET") {
    return new Response("method not allowed", { status: 405 });
  }
  const upgrade = req.headers.get("upgrade")?.toLowerCase() ?? "";
  if (upgrade !== "websocket") {
    return new Response("upgrade required", { status: 426 });
  }
  // Negotiate the subprotocol. Deno's upgradeWebSocket picks the FIRST
  // value from `protocol` that's also in the client's offered list.
  const offered = (req.headers.get("sec-websocket-protocol") ?? "")
    .split(",").map((s) => s.trim());
  if (!offered.includes(WS_SUBPROTOCOL)) {
    return new Response(`required subprotocol: ${WS_SUBPROTOCOL}`, { status: 400 });
  }
  const token = url.searchParams.get("token") ?? "";
  const claims = decodeJwtClaims(token);

  const { socket, response } = Deno.upgradeWebSocket(req, {
    protocol: WS_SUBPROTOCOL,
  });
  attachConn(socket, projectId, claims, registry);
  return response;
}

function attachConn(
  socket: WebSocket,
  projectId: string,
  claims: Record<string, unknown> | null,
  registry: SubscriptionRegistry,
): void {
  let closed = false;
  const wsLike = {
    send: (s: string) => {
      if (closed) return;
      try { socket.send(s); } catch (_) { /* ignore */ }
    },
    isClosed: () => closed,
  };
  const state: ConnState = {
    ws: socket,
    lastPong: Date.now(),
    pingInterval: 0,
    claims,
    wsLike,
  };

  socket.onopen = () => {
    // Heartbeat loop. setInterval is fine here — we clear it on close.
    state.pingInterval = setInterval(() => {
      if (closed) return;
      // Server-initiated stall detection.
      if (Date.now() - state.lastPong > PONG_TIMEOUT_MS) {
        try { socket.close(1008, "ping timeout"); } catch (_) { /* ignore */ }
        return;
      }
      try { socket.send(JSON.stringify({ op: "ping" })); } catch (_) { /* ignore */ }
    }, PING_INTERVAL_MS);
  };

  const frameCtx: FrameContext = {
    wsLike,
    setPong: (ts) => { state.lastPong = ts; },
    projectId,
    claims,
  };
  socket.onmessage = (e) => {
    if (closed) return;
    const data = typeof e.data === "string" ? e.data : "";
    handleFrame(data, frameCtx, registry).catch((err) => {
      // Best-effort error frame — protocol failures shouldn't kill the conn.
      const msg = err instanceof Error ? err.message : String(err);
      try { socket.send(JSON.stringify({ op: "error", code: "BAD_FRAME", message: msg })); }
      catch (_) { /* ignore */ }
    });
  };

  socket.onclose = () => {
    closed = true;
    if (state.pingInterval) clearInterval(state.pingInterval);
    registry.cleanupConn(wsLike);
  };
  socket.onerror = () => {
    closed = true;
    if (state.pingInterval) clearInterval(state.pingInterval);
    registry.cleanupConn(wsLike);
  };
}

/**
 * Test-friendly bag used by `handleFrame` to talk to the conn. The
 * production caller (`attachConn`) constructs one of these per WebSocket;
 * unit tests build a fake.
 */
export interface FrameContext {
  wsLike: { send: (s: string) => void; isClosed: () => boolean };
  /** Mutated to Date.now() when a `{op:"pong"}` frame is received. */
  setPong: (ts: number) => void;
  /** Used by `subscribe` to scope the registration. */
  projectId: string;
  claims: Record<string, unknown> | null;
}

/**
 * Decode and dispatch a single text frame. Exported for unit testing —
 * the integration tests cover the full ws lifecycle via the subprocess
 * runtime, but the protocol branches matter enough to warrant direct
 * coverage too.
 */
export async function handleFrame(
  raw: string,
  ctx: FrameContext,
  registry: SubscriptionRegistry,
): Promise<void> {
  let msg: Record<string, unknown>;
  try {
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") throw new Error("frame must be a JSON object");
    msg = parsed as Record<string, unknown>;
  } catch (e) {
    throw new Error(`invalid frame: ${e instanceof Error ? e.message : String(e)}`);
  }
  const op = msg.op;
  if (op === "ping") {
    ctx.wsLike.send(JSON.stringify({ op: "pong" }));
    return;
  }
  if (op === "pong") {
    ctx.setPong(Date.now());
    return;
  }
  if (op === "subscribe") {
    const subId = msg.subId;
    const ref = msg.ref as FunctionRef | undefined;
    if (typeof subId !== "string" || subId.length === 0 ||
        !ref || typeof ref !== "object" ||
        typeof ref.moduleName !== "string" || ref.moduleName.length === 0) {
      throw new Error("subscribe: subId and ref.moduleName required");
    }
    const args = msg.args ?? {};
    void ctx.claims; // available for future per-sub auth checks.
    await registry.register(ctx.wsLike, subId, ref, args, ctx.projectId, null);
    return;
  }
  if (op === "unsubscribe") {
    const subId = msg.subId;
    if (typeof subId !== "string") throw new Error("unsubscribe: subId required");
    registry.unregister(ctx.wsLike, subId);
    return;
  }
  throw new Error(`unknown op: ${String(op)}`);
}

// Lightweight WebSocket test client tuned for the reactive subscription
// protocol. Wraps Deno's standard `WebSocket` so test bodies can `await`
// individual frames with a deadline instead of juggling onmessage callbacks.
//
// Phase 9b.A scope only — Phase 9b.B / .C may extend the protocol; this
// client treats payloads as opaque JSON objects so adding fields is a
// no-op here.

import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";

export interface WsClient {
  ws: WebSocket;
  inbox: Array<Record<string, unknown>>;
  closed: boolean;
  close: () => void;
}

export interface RuntimeWsBase {
  // Subset of RuntimeHandle we actually need — port + secret. Defined here
  // so the client doesn't import the harness's RuntimeHandle type (which
  // would create a cycle for harness.ts → ws_client.ts back).
  port: number;
  wsPort?: number;
  secret: string;
}

/**
 * Connect to /functions/v1/{projectId}/_watch?token=<jwt>. Returns a
 * WsClient with an inbox the test can drain via `recv`.
 *
 * The runtime listens for WS on the sibling port `wsPort` (separate from
 * the HTTP port). The harness's `startRuntime` sets `wsPort` when the
 * options request WS support.
 */
export async function openWs(
  rt: RuntimeWsBase,
  projectId: string,
  jwt: string,
  subprotocol = "excalibase-fn-v1",
): Promise<WsClient> {
  const wsPort = rt.wsPort ?? rt.port;
  const url = `ws://127.0.0.1:${wsPort}/functions/v1/${encodeURIComponent(projectId)}/_watch?token=${encodeURIComponent(jwt)}`;
  const ws = new WebSocket(url, subprotocol);
  const client: WsClient = {
    ws,
    inbox: [],
    closed: false,
    close: () => {
      try { ws.close(); } catch (_) { /* ignore */ }
      client.closed = true;
    },
  };
  ws.onmessage = (e) => {
    try {
      const data = typeof e.data === "string" ? e.data : "";
      const parsed = JSON.parse(data);
      if (parsed && typeof parsed === "object") {
        client.inbox.push(parsed as Record<string, unknown>);
      }
    } catch (_) {
      // ignore unparseable frames — protocol is JSON-only.
    }
  };
  ws.onclose = () => { client.closed = true; };
  ws.onerror = () => { client.closed = true; };

  // Wait for OPEN with a 5s deadline.
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    if (ws.readyState === WebSocket.OPEN) return client;
    if (ws.readyState === WebSocket.CLOSED || ws.readyState === WebSocket.CLOSING) {
      throw new Error("websocket closed before open");
    }
    await delay(20);
  }
  throw new Error("websocket failed to open within 5s");
}

/** Send a JSON message. */
export async function send(client: WsClient, msg: Record<string, unknown>): Promise<void> {
  client.ws.send(JSON.stringify(msg));
  // Yield to give the runtime a chance to process before the next assertion.
  await delay(1);
}

/**
 * Receive the next message, up to `timeoutMs`. Returns the message or null
 * on timeout. Drains from `inbox` first; otherwise polls the socket.
 */
export async function recv(client: WsClient, timeoutMs = 2000): Promise<Record<string, unknown> | null> {
  if (client.inbox.length > 0) return client.inbox.shift() ?? null;
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await delay(20);
    if (client.inbox.length > 0) return client.inbox.shift() ?? null;
    if (client.closed && client.inbox.length === 0) return null;
  }
  return null;
}

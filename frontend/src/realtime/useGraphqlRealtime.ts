import { useEffect, useRef, useState } from 'react';

/**
 * Wire-protocol contract (matches RealtimeWebSocketHandler at
 * /api/v1/realtime in excalibase-graphql; verified via
 * excalibase-graphql/e2e/nosql-subscription.test.js + client.js):
 *
 *   client → {"type":"connection_init","payload":{"Authorization":"Bearer ..."}}
 *   server → {"type":"connection_ack"}
 *   client → {"type":"subscribe","id":"<uuid>","source":"nosql",
 *             "collection":"<name>","schema":"<schema>","filter":{}}
 *   server → {"type":"next","id":"<uuid>","op":"insert"|"update"|"delete","doc":{...}}
 *   client → {"type":"complete","id":"<uuid>"}
 *
 * The handler also accepts the GraphQL-style {type:"connection_init",
 * payload:{Authorization}} envelope (Hasura/Apollo convention) — see
 * extractBearerToken in the handler. We use that exact shape here.
 */

export type RealtimeStatus = 'connecting' | 'live' | 'reconnecting' | 'offline';

export interface RealtimeRowEvent {
  readonly op: 'row-changed';
  readonly collection: string;
  readonly rowId: string;
  readonly data: Record<string, unknown>;
}

export interface UseGraphqlRealtimeOptions {
  readonly projectId: string;
  readonly graphqlWsUrl: string;
  readonly jwt: string;
  readonly collection?: string;
  readonly schema?: string;
  readonly source?: 'nosql' | 'rest';
  readonly onRowChanged?: (event: RealtimeRowEvent) => void;
}

export interface UseGraphqlRealtimeResult {
  readonly status: RealtimeStatus;
  readonly lastEvent: RealtimeRowEvent | null;
}

// Backoff bounds — mirror the SDK reconnect curve documented in Phase 9b.C
// (1s → 30s with ±20% jitter, doubling on each consecutive failure).
const INITIAL_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 30000;
const JITTER = 0.2;
const ACK_TIMEOUT_MS = 5000;

function computeBackoff(attempt: number): number {
  const exp = Math.min(MAX_BACKOFF_MS, INITIAL_BACKOFF_MS * 2 ** attempt);
  const jitter = 1 + (Math.random() * 2 - 1) * JITTER;
  return Math.floor(Math.max(INITIAL_BACKOFF_MS, exp * jitter));
}

function newSubscriptionId(): string {
  // Avoid pulling in crypto polyfills — short random suffix is plenty here
  // since the id is opaque to the server.
  return `sub-${Math.random().toString(36).slice(2, 10)}-${Date.now().toString(36)}`;
}

function extractRowId(doc: Record<string, unknown>): string {
  const id = doc._id ?? doc.id;
  if (id === null || id === undefined) return '';
  if (typeof id === 'string') return id;
  // String() only ever sees narrowed primitives here (no object → no
  // '[object Object]'); composite/object keys are JSON-serialized instead.
  if (typeof id === 'number' || typeof id === 'bigint' || typeof id === 'boolean') return String(id);
  return JSON.stringify(id);
}

interface NextFrame {
  readonly type: 'next';
  readonly id?: string;
  readonly op?: string;
  readonly doc?: Record<string, unknown>;
}

function isCdcNextFrame(value: unknown): value is NextFrame {
  if (typeof value !== 'object' || value === null) return false;
  const obj = value as { type?: unknown };
  return obj.type === 'next';
}

/**
 * Subscribes to graphql's CDC topic for a single collection and emits
 * row-level events. Auto-reconnects on socket drop. Closes cleanly on
 * unmount.
 *
 * NOTE: This hook intentionally returns ONLY normalized {op:"row-changed",
 * collection, rowId, data}. Heartbeats and non-doc frames are dropped at
 * the boundary so consumers can wire it directly to `refetch()` without
 * deduping noise.
 */
export function useGraphqlRealtime(
  options: UseGraphqlRealtimeOptions,
): UseGraphqlRealtimeResult {
  const { projectId, graphqlWsUrl, jwt, collection, schema, source, onRowChanged } = options;

  const [status, setStatus] = useState<RealtimeStatus>('connecting');
  const [lastEvent, setLastEvent] = useState<RealtimeRowEvent | null>(null);

  // Refs hold mutable state we don't want to retrigger the connect effect.
  const onRowChangedRef = useRef(onRowChanged);
  onRowChangedRef.current = onRowChanged;
  const attemptRef = useRef(0);
  const unmountedRef = useRef(false);

  useEffect(() => {
    unmountedRef.current = false;

    // Guard: no socket without auth + a collection to listen on.
    if (!jwt || !collection || !graphqlWsUrl) {
      setStatus('offline');
      return undefined;
    }
    // After the guard, narrow these once so the inner closures see plain
    // strings (TS won't propagate the narrowing into callbacks otherwise).
    const subCollection: string = collection;

    let socket: WebSocket | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let ackTimer: ReturnType<typeof setTimeout> | null = null;
    let currentSubId = '';

    const scheduleReconnect = (): void => {
      if (unmountedRef.current) return;
      setStatus('reconnecting');
      const delay = computeBackoff(attemptRef.current);
      attemptRef.current += 1;
      reconnectTimer = setTimeout(connect, delay);
    };

    function connect(): void {
      if (unmountedRef.current) return;
      setStatus(attemptRef.current === 0 ? 'connecting' : 'reconnecting');

      try {
        socket = new WebSocket(graphqlWsUrl);
      } catch {
        scheduleReconnect();
        return;
      }
      const ws = socket;

      // Hoisted out of onopen so the setTimeout callback isn't nested a level
      // deeper (keeps function nesting at or below the linter's limit).
      const onAckTimeout = (): void => {
        if (unmountedRef.current) return;
        setStatus('offline');
        try {
          ws.close();
        } catch {
          // Already closing.
        }
      };

      ws.onopen = () => {
        if (unmountedRef.current) return;
        ws.send(
          JSON.stringify({
            type: 'connection_init',
            payload: { Authorization: `Bearer ${jwt}` },
          }),
        );
        // Server must ack within ACK_TIMEOUT_MS; otherwise treat as a
        // failed handshake and reconnect.
        ackTimer = setTimeout(onAckTimeout, ACK_TIMEOUT_MS);
      };

      ws.onmessage = (event: MessageEvent) => {
        if (unmountedRef.current) return;
        let msg: unknown;
        try {
          msg = JSON.parse(typeof event.data === 'string' ? event.data : String(event.data));
        } catch {
          return;
        }

        const envelope = msg as { type?: string };
        if (envelope?.type === 'connection_ack') {
          if (ackTimer) {
            clearTimeout(ackTimer);
            ackTimer = null;
          }
          currentSubId = newSubscriptionId();
          ws.send(
            JSON.stringify({
              type: 'subscribe',
              id: currentSubId,
              source: source ?? 'nosql',
              collection: subCollection,
              schema: schema ?? 'nosql',
              filter: {},
            }),
          );
          attemptRef.current = 0;
          setStatus('live');
          return;
        }

        if (envelope?.type === 'connection_error') {
          setStatus('offline');
          return;
        }

        if (isCdcNextFrame(msg) && msg.id === currentSubId && msg.doc) {
          const op = String(msg.op ?? '').toLowerCase();
          if (op !== 'insert' && op !== 'update' && op !== 'delete') return;
          const ev: RealtimeRowEvent = {
            op: 'row-changed',
            collection: subCollection,
            rowId: extractRowId(msg.doc),
            data: msg.doc,
          };
          setLastEvent(ev);
          onRowChangedRef.current?.(ev);
        }
      };

      ws.onerror = () => {
        // onclose will fire right after; defer the reconnect there to
        // avoid scheduling twice.
      };

      ws.onclose = () => {
        if (ackTimer) {
          clearTimeout(ackTimer);
          ackTimer = null;
        }
        if (unmountedRef.current) return;
        scheduleReconnect();
      };
    }

    connect();

    return () => {
      unmountedRef.current = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (ackTimer) clearTimeout(ackTimer);
      if (socket) {
        try {
          if (currentSubId && socket.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ type: 'complete', id: currentSubId }));
          }
        } catch {
          // Already gone.
        }
        try {
          socket.close();
        } catch {
          // Already closed.
        }
      }
    };
    // projectId is included so re-tenanting (switching projects) forces a
    // fresh socket — even if the URL/jwt happen to match.
  }, [projectId, graphqlWsUrl, jwt, collection, schema, source]);

  return { status, lastEvent };
}

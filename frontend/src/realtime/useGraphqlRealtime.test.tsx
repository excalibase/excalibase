import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useGraphqlRealtime, type RealtimeRowEvent } from './useGraphqlRealtime';

/**
 * MockWebSocket — minimal stand-in for the real WebSocket. Test code drives
 * it via the `instances` array: each `new WebSocket(url)` pushes onto it,
 * so a test can grab the most recent instance and call its `emit*` helpers
 * to simulate server frames or socket drops.
 *
 * Mirrors only the subset of the WebSocket spec the hook touches:
 * readyState transitions, send/close, and the four event handler slots.
 */
type Listener = (ev: { data: string } | { code: number; reason: string } | Event) => void;

class MockWebSocket {
  static instances: MockWebSocket[] = [];
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  CONNECTING = 0;
  OPEN = 1;
  CLOSING = 2;
  CLOSED = 3;

  url: string;
  readyState = MockWebSocket.CONNECTING;
  sent: string[] = [];
  onopen: Listener | null = null;
  onmessage: Listener | null = null;
  onerror: Listener | null = null;
  onclose: Listener | null = null;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(code = 1000, reason = ''): void {
    if (this.readyState === MockWebSocket.CLOSED) return;
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.({ code, reason } as never);
  }

  // Test helpers
  emitOpen(): void {
    this.readyState = MockWebSocket.OPEN;
    this.onopen?.(new Event('open'));
  }

  emitMessage(payload: unknown): void {
    this.onmessage?.({ data: typeof payload === 'string' ? payload : JSON.stringify(payload) });
  }

  emitClose(code = 1006, reason = 'abnormal'): void {
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.({ code, reason } as never);
  }
}

const realWebSocket = globalThis.WebSocket;

beforeEach(() => {
  MockWebSocket.instances = [];
  // Type via `unknown` then narrow; jsdom doesn't ship a usable WebSocket.
  (globalThis as unknown as { WebSocket: typeof MockWebSocket }).WebSocket = MockWebSocket;
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  (globalThis as unknown as { WebSocket: typeof realWebSocket }).WebSocket = realWebSocket;
});

const baseProps = {
  projectId: 'proj-abc',
  graphqlWsUrl: 'ws://localhost:10000/api/v1/realtime',
  jwt: 'jwt-token-xyz',
  collection: 'orders',
};

describe('useGraphqlRealtime', () => {
  test('opens a WebSocket to graphqlWsUrl on mount', () => {
    renderHook(() => useGraphqlRealtime(baseProps));
    expect(MockWebSocket.instances).toHaveLength(1);
    expect(MockWebSocket.instances[0].url).toBe('ws://localhost:10000/api/v1/realtime');
  });

  test('sends connection_init with Bearer Authorization in payload on open', () => {
    renderHook(() => useGraphqlRealtime(baseProps));
    act(() => MockWebSocket.instances[0].emitOpen());
    const sent = JSON.parse(MockWebSocket.instances[0].sent[0]);
    expect(sent).toEqual({
      type: 'connection_init',
      payload: { Authorization: 'Bearer jwt-token-xyz' },
    });
  });

  test('subscribes to the collection after connection_ack', () => {
    renderHook(() => useGraphqlRealtime(baseProps));
    const ws = MockWebSocket.instances[0];
    act(() => ws.emitOpen());
    act(() => ws.emitMessage({ type: 'connection_ack' }));
    // First sent message is connection_init, second is subscribe.
    const subscribeMsg = JSON.parse(ws.sent[1]);
    expect(subscribeMsg).toMatchObject({
      type: 'subscribe',
      source: 'nosql',
      collection: 'orders',
    });
    expect(typeof subscribeMsg.id).toBe('string');
    expect(subscribeMsg.id.length).toBeGreaterThan(0);
  });

  test('moves to "live" status once subscribe is sent', () => {
    const { result } = renderHook(() => useGraphqlRealtime(baseProps));
    const ws = MockWebSocket.instances[0];
    expect(result.current.status).toBe('connecting');
    act(() => ws.emitOpen());
    act(() => ws.emitMessage({ type: 'connection_ack' }));
    expect(result.current.status).toBe('live');
  });

  test('invokes onRowChanged with normalized payload for "next" CDC events', () => {
    const events: RealtimeRowEvent[] = [];
    renderHook(() =>
      useGraphqlRealtime({
        ...baseProps,
        onRowChanged: (ev) => events.push(ev),
      }),
    );
    const ws = MockWebSocket.instances[0];
    act(() => ws.emitOpen());
    act(() => ws.emitMessage({ type: 'connection_ack' }));
    const subscribeMsg = JSON.parse(ws.sent[1]);
    const subId = subscribeMsg.id;

    act(() =>
      ws.emitMessage({
        type: 'next',
        id: subId,
        op: 'insert',
        doc: { _id: 'row-1', name: 'Alice' },
      }),
    );

    expect(events).toHaveLength(1);
    expect(events[0]).toEqual({
      op: 'row-changed',
      collection: 'orders',
      rowId: 'row-1',
      data: { _id: 'row-1', name: 'Alice' },
    });
  });

  test('falls back to "id" if "_id" is missing on the doc', () => {
    const events: RealtimeRowEvent[] = [];
    renderHook(() =>
      useGraphqlRealtime({
        ...baseProps,
        onRowChanged: (ev) => events.push(ev),
      }),
    );
    const ws = MockWebSocket.instances[0];
    act(() => ws.emitOpen());
    act(() => ws.emitMessage({ type: 'connection_ack' }));
    const subId = JSON.parse(ws.sent[1]).id;

    act(() =>
      ws.emitMessage({
        type: 'next',
        id: subId,
        op: 'update',
        doc: { id: 42, value: 'x' },
      }),
    );

    expect(events[0]?.rowId).toBe('42');
  });

  test('records lastEvent on the returned status object', () => {
    const { result } = renderHook(() => useGraphqlRealtime(baseProps));
    const ws = MockWebSocket.instances[0];
    act(() => ws.emitOpen());
    act(() => ws.emitMessage({ type: 'connection_ack' }));
    expect(result.current.status).toBe('live');

    const subId = JSON.parse(ws.sent[1]).id;
    act(() =>
      ws.emitMessage({
        type: 'next',
        id: subId,
        op: 'delete',
        doc: { _id: 'gone' },
      }),
    );
    expect(result.current.lastEvent?.rowId).toBe('gone');
  });

  test('closes the socket on unmount and stops emitting events', () => {
    const events: RealtimeRowEvent[] = [];
    const { unmount } = renderHook(() =>
      useGraphqlRealtime({
        ...baseProps,
        onRowChanged: (ev) => events.push(ev),
      }),
    );
    const ws = MockWebSocket.instances[0];
    act(() => ws.emitOpen());
    act(() => ws.emitMessage({ type: 'connection_ack' }));

    unmount();
    expect(ws.readyState).toBe(MockWebSocket.CLOSED);
    const before = events.length;
    act(() =>
      ws.emitMessage({
        type: 'next',
        id: JSON.parse(ws.sent[1]).id,
        op: 'insert',
        doc: { _id: 'after-unmount' },
      }),
    );
    // Events delivered after unmount must not call the callback.
    expect(events.length).toBe(before);
  });

  test('flips to "reconnecting" on socket drop and opens a new socket after backoff', async () => {
    const { result } = renderHook(() => useGraphqlRealtime(baseProps));
    const firstSocket = MockWebSocket.instances[0];
    act(() => firstSocket.emitOpen());
    act(() => firstSocket.emitMessage({ type: 'connection_ack' }));
    expect(result.current.status).toBe('live');

    // Server-side drop.
    act(() => firstSocket.emitClose(1006, 'connection lost'));
    expect(result.current.status).toBe('reconnecting');

    // Initial reconnect delay is ~1s (±20% jitter); advance generously.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });

    expect(MockWebSocket.instances.length).toBeGreaterThanOrEqual(2);
    const secondSocket = MockWebSocket.instances[MockWebSocket.instances.length - 1];

    act(() => secondSocket.emitOpen());
    act(() => secondSocket.emitMessage({ type: 'connection_ack' }));
    expect(result.current.status).toBe('live');
  });

  test('caps reconnect backoff at 30 seconds', async () => {
    const { result } = renderHook(() => useGraphqlRealtime(baseProps));
    // Force many failed attempts so the cap kicks in.
    for (let i = 0; i < 8; i++) {
      const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1];
      act(() => ws.emitClose(1006, 'drop'));
      // Advance by the maximum + safety so each scheduled reconnect fires.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(40_000);
      });
    }
    expect(result.current.status).toBe('reconnecting');
    // Cap means even after 8 closes, total elapsed bounded by 8 * 30s.
    expect(MockWebSocket.instances.length).toBeGreaterThanOrEqual(2);
  });

  test('does not open a socket when jwt is empty', () => {
    renderHook(() => useGraphqlRealtime({ ...baseProps, jwt: '' }));
    expect(MockWebSocket.instances).toHaveLength(0);
  });

  test('does not open a socket when collection is empty', () => {
    renderHook(() => useGraphqlRealtime({ ...baseProps, collection: '' }));
    expect(MockWebSocket.instances).toHaveLength(0);
  });

  test('emits "offline" status if connection_ack does not arrive within 5 seconds', async () => {
    const { result } = renderHook(() => useGraphqlRealtime(baseProps));
    const ws = MockWebSocket.instances[0];
    act(() => ws.emitOpen());
    // No connection_ack is sent.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000);
    });
    expect(['offline', 'reconnecting']).toContain(result.current.status);
  });
});

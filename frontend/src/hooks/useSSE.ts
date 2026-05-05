import { useState, useEffect, useRef } from 'react';
import { api } from '../api/client';

interface UseSSEOptions {
  enabled?: boolean;
}

interface UseSSEResult {
  isConnected: boolean;
  error: string | null;
}

/**
 * Shared hook for Server-Sent Events connections.
 * Manages EventSource lifecycle, connection state, and cleanup.
 *
 * @param path - API path relative to base URL (e.g. "/provision/{id}/metrics/stream")
 * @param onMessage - Callback invoked with parsed JSON data for each SSE message
 * @param options - { enabled } to conditionally connect
 */
export function useSSE<T>(
  path: string | null,
  onMessage: (data: T) => void,
  options?: UseSSEOptions,
): UseSSEResult {
  const [isConnected, setIsConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const onMessageRef = useRef(onMessage);
  onMessageRef.current = onMessage;

  const enabled = options?.enabled ?? true;

  useEffect(() => {
    if (!path || !enabled) return;

    const baseURL = api.defaults.baseURL || 'http://localhost:24005/api';
    const eventSource = new EventSource(`${baseURL}${path}`);

    eventSource.onopen = () => {
      setIsConnected(true);
      setError(null);
    };

    eventSource.onmessage = (event) => {
      try {
        const data: T = JSON.parse(event.data);
        onMessageRef.current(data);
      } catch {
        setError('Failed to parse server event');
      }
    };

    eventSource.onerror = () => {
      setIsConnected(false);
      setError('Connection lost');
      eventSource.close();
    };

    return () => {
      eventSource.close();
      setIsConnected(false);
    };
  }, [path, enabled]);

  return { isConnected, error };
}

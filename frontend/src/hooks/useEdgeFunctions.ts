import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { EdgeFunction, LogEntry, RuntimeStatus, SecretKey } from '../types/edgefn';

/**
 * All edge function operations are per-project. Pass the projectId from
 * useParams at the page level.
 */

function baseUrl(projectId: string) {
  return `/projects/${projectId}/functions`;
}

export function useEdgeFunctions(projectId: string) {
  return useQuery({
    queryKey: ['edge-functions', projectId],
    queryFn: async () => {
      const res = await api.get<EdgeFunction[]>(baseUrl(projectId));
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useEdgeFunction(projectId: string, fnId: string) {
  return useQuery({
    queryKey: ['edge-function', projectId, fnId],
    queryFn: async () => {
      const res = await api.get<EdgeFunction>(`${baseUrl(projectId)}/${fnId}`);
      return res.data;
    },
    enabled: !!projectId && !!fnId,
  });
}

export function useCreateEdgeFunction(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: {
      id: string;
      name: string;
      description?: string;
      files: { path: string; content: string }[];
    }) => {
      const res = await api.post<EdgeFunction>(baseUrl(projectId), body);
      return res.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['edge-functions', projectId] });
    },
  });
}

export function useDeleteEdgeFunction(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (fnId: string) => {
      await api.delete(`${baseUrl(projectId)}/${fnId}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['edge-functions', projectId] });
    },
  });
}

/**
 * Invoke returns the runtime's InvokeResponse body directly — the Go handler
 * forwards status/headers/body from the user handler's Response.
 */
export function useInvokeEdgeFunction(projectId: string) {
  return useMutation({
    mutationFn: async ({ fnId, body }: { fnId: string; body?: string }) => {
      const res = await api.post(`${baseUrl(projectId)}/${fnId}/invoke`, body ?? '', {
        headers: { 'Content-Type': 'application/json' },
        transformResponse: (raw) => raw, // keep as string, don't auto-parse
      });
      return res.data as string;
    },
  });
}

export function useRuntimeStatus(projectId: string) {
  return useQuery({
    queryKey: ['edge-functions-runtime', projectId],
    queryFn: async () => {
      const res = await api.get<RuntimeStatus>(`${baseUrl(projectId)}/runtime/status`);
      return res.data;
    },
    enabled: !!projectId,
    refetchInterval: 30000,
  });
}

// --- Logs ---

// useEdgeFunctionLogs polls the runtime's per-function ring buffer. The
// runtime stores the last 100 console.* lines per function and returns
// them in order. Polling runs every 2 seconds while the panel is mounted
// and a function id is set.
export function useEdgeFunctionLogs(projectId: string, fnId: string | null) {
  return useQuery({
    queryKey: ['edge-function-logs', projectId, fnId],
    queryFn: async () => {
      if (!fnId) return [] as LogEntry[];
      const res = await api.get<{ logs: LogEntry[] }>(`${baseUrl(projectId)}/${fnId}/logs`);
      return res.data.logs ?? [];
    },
    enabled: !!projectId && !!fnId,
    refetchInterval: 2000,
  });
}

// --- Secrets ---

export function useEdgeSecrets(projectId: string) {
  return useQuery({
    queryKey: ['edge-function-secrets', projectId],
    queryFn: async () => {
      const res = await api.get<SecretKey[]>(`${baseUrl(projectId)}/secrets`);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useSetEdgeSecret(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { key: string; value: string }) => {
      const res = await api.post(`${baseUrl(projectId)}/secrets`, body);
      return res.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['edge-function-secrets', projectId] });
    },
  });
}

export function useDeleteEdgeSecret(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (key: string) => {
      await api.delete(`${baseUrl(projectId)}/secrets/${key}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['edge-function-secrets', projectId] });
    },
  });
}

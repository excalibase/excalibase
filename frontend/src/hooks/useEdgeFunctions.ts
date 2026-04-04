import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { EdgeFunction, RuntimeStatus } from '../types/edgefn';

export function useEdgeFunctions() {
  return useQuery({
    queryKey: ['edge-functions'],
    queryFn: async () => {
      const res = await api.get<EdgeFunction[]>('/functions');
      return res.data;
    },
  });
}

export function useEdgeFunction(fnId: string) {
  return useQuery({
    queryKey: ['edge-function', fnId],
    queryFn: async () => {
      const res = await api.get<EdgeFunction>(`/functions/${fnId}`);
      return res.data;
    },
    enabled: !!fnId,
  });
}

export function useCreateEdgeFunction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { id: string; name: string; code: string; hookType?: string }) => {
      const res = await api.post<EdgeFunction>('/functions', body);
      return res.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['edge-functions'] });
    },
  });
}

export function useDeleteEdgeFunction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (fnId: string) => {
      await api.delete(`/functions/${fnId}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['edge-functions'] });
    },
  });
}

export function useInvokeEdgeFunction() {
  return useMutation({
    mutationFn: async ({ fnId, data }: { fnId: string; data?: unknown }) => {
      const res = await api.post(`/functions/${fnId}/invoke`, data ?? {});
      return res.data;
    },
  });
}

export function useRuntimeStatus() {
  return useQuery({
    queryKey: ['edge-functions-runtime'],
    queryFn: async () => {
      const res = await api.get<RuntimeStatus>('/functions/runtime/status');
      return res.data;
    },
    refetchInterval: 30000,
  });
}

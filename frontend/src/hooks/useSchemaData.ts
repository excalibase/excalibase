import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '../api/client';
import type { RowsResult } from '../types/schema';
import { onMutationError } from '../utils/mutationHelpers';

export function useRows(projectId: string, tableName: string, opts?: { limit?: number; offset?: number; sort?: string; order?: string; schema?: string }) {
  const schema = opts?.schema || 'public';
  return useQuery({
    queryKey: ['schema-rows', projectId, tableName, schema, opts?.limit, opts?.offset, opts?.sort, opts?.order],
    queryFn: async () => {
      const res = await api.get<RowsResult>(`/schema/${projectId}/tables/${tableName}/rows`, {
        params: { schema, limit: opts?.limit || 50, offset: opts?.offset || 0, sort: opts?.sort, order: opts?.order },
      });
      return res.data;
    },
    enabled: !!projectId && !!tableName,
  });
}

export function useInsertRow(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, data }: { tableName: string; data: Record<string, unknown> }) => {
      const res = await api.post(`/schema/${projectId}/tables/${tableName}/rows`, { data });
      return res.data;
    },
    onSuccess: () => { toast.success('Row inserted'); qc.invalidateQueries({ queryKey: ['schema-rows', projectId] }); },
    onError: onMutationError('insert row'),
  });
}

export function useUpdateRow(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, pk, data }: { tableName: string; pk: { column: string; value: string }; data: Record<string, unknown> }) => {
      await api.patch(`/schema/${projectId}/tables/${tableName}/rows`, { pk, data });
    },
    onSuccess: () => { toast.success('Row updated'); qc.invalidateQueries({ queryKey: ['schema-rows', projectId] }); },
    onError: onMutationError('update row'),
  });
}

export function useDeleteRow(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, pk }: { tableName: string; pk: { column: string; value: string } }) => {
      await api.delete(`/schema/${projectId}/tables/${tableName}/rows`, { data: { pk } });
    },
    onSuccess: () => { toast.success('Row deleted'); qc.invalidateQueries({ queryKey: ['schema-rows', projectId] }); },
    onError: onMutationError('delete row'),
  });
}

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '../api/client';
import type { TableInfo, ColumnInfo, RelationshipInfo } from '../types/schema';
import { onMutationError } from '../utils/mutationHelpers';

// --- Tables ---

export function useTables(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-tables', projectId, schema],
    queryFn: async () => {
      const res = await api.get<TableInfo[]>(`/schema/${projectId}/tables`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useColumns(projectId: string, tableName: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-columns', projectId, tableName, schema],
    queryFn: async () => {
      const res = await api.get<ColumnInfo[]>(`/schema/${projectId}/tables/${tableName}/columns`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId && !!tableName,
  });
}

export function useRelationships(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-relationships', projectId, schema],
    queryFn: async () => {
      const res = await api.get<RelationshipInfo[]>(`/schema/${projectId}/relationships`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useCreateTable(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; schema?: string; columns?: Array<{ name: string; type: string; nullable?: boolean; primaryKey?: boolean; unique?: boolean; default?: string }> }) => {
      await api.post(`/schema/${projectId}/tables`, { schema: 'public', ...body });
    },
    onSuccess: () => {
      toast.success('Table created');
      qc.invalidateQueries({ queryKey: ['schema-tables', projectId] });
    },
    onError: onMutationError('create table'),
  });
}

export function useUpdateTable(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, ...body }: { tableName: string; newName?: string; rlsEnabled?: boolean; comment?: string }) => {
      await api.patch(`/schema/${projectId}/tables/${tableName}`, body);
    },
    onSuccess: () => {
      toast.success('Table updated');
      qc.invalidateQueries({ queryKey: ['schema-tables', projectId] });
    },
    onError: onMutationError('update table'),
  });
}

export function useDropTable(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, cascade }: { tableName: string; cascade?: boolean }) => {
      await api.delete(`/schema/${projectId}/tables/${tableName}`, { params: { cascade } });
    },
    onSuccess: () => {
      toast.success('Table dropped');
      qc.invalidateQueries({ queryKey: ['schema-tables', projectId] });
    },
    onError: onMutationError('drop table'),
  });
}

// --- Columns ---

export function useAddColumn(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, ...body }: { tableName: string; name: string; type: string; nullable?: boolean; unique?: boolean; default?: string }) => {
      await api.post(`/schema/${projectId}/tables/${tableName}/columns`, body);
    },
    onSuccess: () => {
      toast.success('Column added');
      qc.invalidateQueries({ queryKey: ['schema-columns', projectId] });
      qc.invalidateQueries({ queryKey: ['schema-tables', projectId] });
    },
    onError: onMutationError('add column'),
  });
}

export function useAlterColumn(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, columnName, ...body }: { tableName: string; columnName: string; newName?: string; type?: string; nullable?: boolean; default?: string; dropDefault?: boolean }) => {
      await api.patch(`/schema/${projectId}/tables/${tableName}/columns/${columnName}`, body);
    },
    onSuccess: () => {
      toast.success('Column updated');
      qc.invalidateQueries({ queryKey: ['schema-columns', projectId] });
    },
    onError: onMutationError('alter column'),
  });
}

export function useDropColumn(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ tableName, columnName }: { tableName: string; columnName: string }) => {
      await api.delete(`/schema/${projectId}/tables/${tableName}/columns/${columnName}`);
    },
    onSuccess: () => {
      toast.success('Column dropped');
      qc.invalidateQueries({ queryKey: ['schema-columns', projectId] });
      qc.invalidateQueries({ queryKey: ['schema-tables', projectId] });
    },
    onError: onMutationError('drop column'),
  });
}

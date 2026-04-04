import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { api } from '../api/client';
import type {
  RoleInfo, ExtensionInfo, PolicyInfo, FunctionInfo,
  TriggerInfo, IndexInfo, PgTypeInfo,
} from '../types/schema';
import { onMutationError } from '../utils/mutationHelpers';

// --- Roles ---

export function useRoles(projectId: string) {
  return useQuery({
    queryKey: ['schema-roles', projectId],
    queryFn: async () => {
      const res = await api.get<RoleInfo[]>(`/schema/${projectId}/roles`);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useCreateRole(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; password?: string; login?: boolean }) => {
      await api.post(`/schema/${projectId}/roles`, body);
    },
    onSuccess: () => {
      toast.success('Role created');
      qc.invalidateQueries({ queryKey: ['schema-roles', projectId] });
    },
    onError: onMutationError('create role'),
  });
}

export function useDropRole(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (roleName: string) => {
      await api.delete(`/schema/${projectId}/roles/${roleName}`);
    },
    onSuccess: () => {
      toast.success('Role dropped');
      qc.invalidateQueries({ queryKey: ['schema-roles', projectId] });
    },
    onError: onMutationError('drop role'),
  });
}

// --- Extensions ---

export function useExtensions(projectId: string) {
  return useQuery({
    queryKey: ['schema-extensions', projectId],
    queryFn: async () => {
      const res = await api.get<ExtensionInfo[]>(`/schema/${projectId}/extensions`);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useCreateExtension(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; schema?: string }) => {
      await api.post(`/schema/${projectId}/extensions`, body);
    },
    onSuccess: () => {
      toast.success('Extension enabled');
      qc.invalidateQueries({ queryKey: ['schema-extensions', projectId] });
    },
    onError: onMutationError('enable extension'),
  });
}

export function useDropExtension(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ name, cascade }: { name: string; cascade?: boolean }) => {
      await api.delete(`/schema/${projectId}/extensions/${name}`, { params: { cascade } });
    },
    onSuccess: () => {
      toast.success('Extension disabled');
      qc.invalidateQueries({ queryKey: ['schema-extensions', projectId] });
    },
    onError: onMutationError('disable extension'),
  });
}

// --- Policies (RLS) ---

export function usePolicies(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-policies', projectId, schema],
    queryFn: async () => {
      const res = await api.get<PolicyInfo[]>(`/schema/${projectId}/policies`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useCreatePolicy(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; table: string; schema?: string; command: string; roles: string; using?: string; withCheck?: string; permissive?: boolean }) => {
      await api.post(`/schema/${projectId}/policies`, { schema: 'public', permissive: true, ...body });
    },
    onSuccess: () => {
      toast.success('Policy created');
      qc.invalidateQueries({ queryKey: ['schema-policies', projectId] });
    },
    onError: onMutationError('create policy'),
  });
}

export function useDropPolicy(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ table, name }: { table: string; name: string }) => {
      await api.delete(`/schema/${projectId}/policies/${name}`, { params: { table } });
    },
    onSuccess: () => {
      toast.success('Policy dropped');
      qc.invalidateQueries({ queryKey: ['schema-policies', projectId] });
    },
    onError: onMutationError('drop policy'),
  });
}

// --- Functions ---

export function useFunctions(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-functions', projectId, schema],
    queryFn: async () => {
      const res = await api.get<FunctionInfo[]>(`/schema/${projectId}/functions`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useCreateFunction(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; schema?: string; language: string; returnType: string; args?: string; body: string; volatility?: string }) => {
      await api.post(`/schema/${projectId}/functions`, { schema: 'public', volatility: 'VOLATILE', ...body });
    },
    onSuccess: () => {
      toast.success('Function created');
      qc.invalidateQueries({ queryKey: ['schema-functions', projectId] });
    },
    onError: onMutationError('create function'),
  });
}

export function useDropFunction(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ name, argTypes }: { name: string; argTypes?: string }) => {
      await api.delete(`/schema/${projectId}/functions/${name}`, { params: { argTypes } });
    },
    onSuccess: () => {
      toast.success('Function dropped');
      qc.invalidateQueries({ queryKey: ['schema-functions', projectId] });
    },
    onError: onMutationError('drop function'),
  });
}

// --- Triggers ---

export function useTriggers(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-triggers', projectId, schema],
    queryFn: async () => {
      const res = await api.get<TriggerInfo[]>(`/schema/${projectId}/triggers`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useCreateTrigger(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; table: string; schema?: string; event: string; timing: string; forEachRow?: boolean; function: string }) => {
      await api.post(`/schema/${projectId}/triggers`, { schema: 'public', ...body });
    },
    onSuccess: () => { toast.success('Trigger created'); qc.invalidateQueries({ queryKey: ['schema-triggers', projectId] }); },
    onError: onMutationError('create trigger'),
  });
}

export function useDropTrigger(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ name, table }: { name: string; table: string }) => {
      await api.delete(`/schema/${projectId}/triggers/${name}`, { params: { table } });
    },
    onSuccess: () => { toast.success('Trigger dropped'); qc.invalidateQueries({ queryKey: ['schema-triggers', projectId] }); },
    onError: onMutationError('drop trigger'),
  });
}

// --- Indexes ---

export function useIndexes(projectId: string, tableName: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-indexes', projectId, tableName, schema],
    queryFn: async () => {
      const res = await api.get<IndexInfo[]>(`/schema/${projectId}/tables/${tableName}/indexes`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId && !!tableName,
  });
}

export function useCreateIndex(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; table: string; schema?: string; columns: string[]; unique?: boolean; type?: string }) => {
      await api.post(`/schema/${projectId}/indexes`, { schema: 'public', type: 'btree', ...body });
    },
    onSuccess: () => { toast.success('Index created'); qc.invalidateQueries({ queryKey: ['schema-indexes', projectId] }); },
    onError: onMutationError('create index'),
  });
}

export function useDropIndex(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ name, schema }: { name: string; schema?: string }) => {
      await api.delete(`/schema/${projectId}/indexes/${name}`, { params: { schema: schema || 'public' } });
    },
    onSuccess: () => { toast.success('Index dropped'); qc.invalidateQueries({ queryKey: ['schema-indexes', projectId] }); },
    onError: onMutationError('drop index'),
  });
}

// --- PG Types ---

export function usePgTypes(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['schema-types', projectId, schema],
    queryFn: async () => {
      const res = await api.get<PgTypeInfo[]>(`/schema/${projectId}/types`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
  });
}

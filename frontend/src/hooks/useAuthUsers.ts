import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { AuthUser, AuthSession } from '../types/authUsers';

export function useAuthUsers(projectId: string) {
  return useQuery({
    queryKey: ['auth-users', projectId],
    queryFn: async () => {
      const res = await api.get<AuthUser[]>(`/projects/${projectId}/auth/users`);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useUpdateAuthUser(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ userId, ...body }: { userId: number; enabled?: boolean; role?: string }) => {
      await api.patch(`/projects/${projectId}/auth/users/${userId}`, body);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['auth-users', projectId] });
    },
  });
}

export function useDeleteAuthUser(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (userId: number) => {
      await api.delete(`/projects/${projectId}/auth/users/${userId}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['auth-users', projectId] });
    },
  });
}

export function useAuthSessions(projectId: string) {
  return useQuery({
    queryKey: ['auth-sessions', projectId],
    queryFn: async () => {
      const res = await api.get<AuthSession[]>(`/projects/${projectId}/auth/sessions`);
      return res.data;
    },
    enabled: !!projectId,
  });
}

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';

interface VaultStatus {
  initialized: boolean;
  sealed: boolean;
  threshold: number;
  shares: number;
  progress: number;
  type: string;
}

interface VaultInitResult {
  shares: string[];
  threshold: number;
}

interface VaultUnsealResult {
  sealed: boolean;
  progress: number;
  threshold: number;
}

interface VaultListResponse {
  paths: string[];
}

export function useVaultStatus() {
  return useQuery<VaultStatus>({
    queryKey: ['vault', 'status'],
    queryFn: async () => {
      const { data } = await api.get('/vault/status');
      return data;
    },
    staleTime: 5_000,
    refetchInterval: (q) => (q.state.data?.sealed ? 5_000 : false),
  });
}

export function useInitVault() {
  const queryClient = useQueryClient();
  return useMutation<VaultInitResult, Error, { shares: number; threshold: number }>({
    mutationFn: async ({ shares, threshold }) => {
      const { data } = await api.post<VaultInitResult>('/vault/init', { shares, threshold });
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vault', 'status'] });
    },
  });
}

export function useUnsealVault() {
  const queryClient = useQueryClient();
  return useMutation<VaultUnsealResult, Error, string>({
    mutationFn: async (share) => {
      const { data } = await api.post<VaultUnsealResult>('/vault/unseal', { share });
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vault', 'status'] });
    },
  });
}

export function useVaultSecretsList(prefix?: string) {
  return useQuery<string[]>({
    queryKey: ['vault', 'secrets', prefix ?? ''],
    queryFn: async () => {
      const params = prefix ? `?prefix=${encodeURIComponent(prefix)}` : '';
      const { data } = await api.get<VaultListResponse>(`/vault/secrets-list${params}`);
      return data.paths;
    },
    staleTime: 30_000,
  });
}

export function useVaultSecret(path: string | null) {
  return useQuery<Record<string, string>>({
    queryKey: ['vault', 'secret', path],
    queryFn: async () => {
      if (!path) throw new Error('No path');
      const { data } = await api.get(`/vault/secrets/${path}`);
      return data;
    },
    enabled: !!path,
    staleTime: 0, // always fresh when revealed
  });
}

export function useDeleteVaultSecret() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (path: string) => {
      await api.delete(`/vault/secrets/${path}`);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['vault', 'secrets'] });
    },
  });
}

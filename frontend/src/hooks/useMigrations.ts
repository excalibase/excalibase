import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';

export interface MigrationRecord {
  id: string;
  projectId: string;
  version: string;
  name: string;
  description?: string;
  sql: string;
  status: 'APPLIED' | 'FAILED';
  appliedAt: string;
  executionTimeMs: number;
  errorMessage?: string;
  checksum: string;
}

export interface MigrationRequest {
  version: string;
  name: string;
  description?: string;
  sql: string;
}

export const useMigrations = (projectId: string) => {
  return useQuery({
    queryKey: ['migrations', projectId],
    queryFn: async () => {
      const response = await api.get<MigrationRecord[]>(`/provision/${projectId}/migrations`);
      return response.data;
    },
    enabled: !!projectId,
  });
};

export const useApplyMigration = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (request: MigrationRequest) => {
      const response = await api.post<MigrationRecord>(
        `/provision/${projectId}/migrations`,
        request
      );
      return response.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['migrations', projectId] });
    },
  });
};

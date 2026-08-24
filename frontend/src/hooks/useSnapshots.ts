import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';

export interface SnapshotInfo {
  snapshotId: string;
  projectId: string;
  sizeBytes: number;
  format: string;
  schemaOnly: boolean;
  dataOnly: boolean;
  createdAt: string;
  downloadUrl: string;
}

export interface SnapshotExportRequest {
  format?: 'custom' | 'plain';
  schemaOnly?: boolean;
  dataOnly?: boolean;
  tables?: string[];
}

export const useSnapshots = (projectId: string) => {
  return useQuery({
    queryKey: ['snapshots', projectId],
    queryFn: async () => {
      const response = await api.get<SnapshotInfo[]>(`/provision/${projectId}/snapshot`);
      return response.data;
    },
    enabled: !!projectId,
  });
};

export const useExportSnapshot = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (request: SnapshotExportRequest = {}) => {
      const response = await api.post<SnapshotInfo>(`/provision/${projectId}/snapshot/export`, request);
      return response.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['snapshots', projectId] });
    },
  });
};

export const useDeleteSnapshot = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (snapshotId: string) => {
      await api.delete(`/provision/${projectId}/snapshot/${snapshotId}`);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['snapshots', projectId] });
    },
  });
};

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// A project's S3 access keys (EXC-630). The secret is in the create answer only.
export interface S3Permissions {
  read: boolean;
  write: boolean;
  delete: boolean;
}

export interface S3Key extends S3Permissions {
  id: string;
  name: string;
  accessKeyId: string;
  // Empty means every bucket of the project.
  buckets: string[];
  createdAt: string;
  expiresAt?: string;
  lastUsedAt?: string;
}

export interface S3KeyList {
  keys: S3Key[];
  endpoint: string;
  region: string;
}

export interface CreatedS3Key extends S3Key {
  secretAccessKey: string;
  endpoint: string;
  region: string;
}

export interface CreateS3KeyRequest {
  name: string;
  buckets: string[];
  permissions: S3Permissions;
  expiresInDays?: number;
}

const keysPath = (projectId: string) => `/projects/${projectId}/storage/s3-keys`;

export function useS3Keys(projectId: string) {
  return useQuery({
    queryKey: ['s3-keys', projectId],
    queryFn: async () => (await api.get<S3KeyList>(keysPath(projectId))).data,
  });
}

export function useCreateS3Key(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateS3KeyRequest) => (await api.post<CreatedS3Key>(keysPath(projectId), body)).data,
    onSuccess: () => client.invalidateQueries({ queryKey: ['s3-keys', projectId] }),
  });
}

export function useRevokeS3Key(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await api.delete(`${keysPath(projectId)}/${id}`);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: ['s3-keys', projectId] }),
  });
}

export function permissionsText(key: S3Permissions): string {
  return (['read', 'write', 'delete'] as const).filter((p) => key[p]).join(', ');
}

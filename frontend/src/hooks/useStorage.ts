import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import { api } from '../api/client';
import { uploadFailure, type UploadStep } from '../utils/uploadFailure';

// Mirrors server-side storagesvc.Bucket. The two booleans (public,
// fileSize/MIME constraints) are what the studio surfaces; the rest is
// metadata for display.
// What app users may do in a bucket, by role (EXC-560). '' grants nothing.
export type AccessScope = '' | 'own' | 'all';
export type BucketAccess = Record<string, { read?: AccessScope; write?: AccessScope; delete?: AccessScope }>;

export interface Bucket {
  id: string;
  projectId: string;
  name: string;
  public: boolean;
  fileSizeLimit?: number;
  allowedMimeTypes?: string[];
  access?: BucketAccess;
  createdAt: string;
  updatedAt: string;
}

export const useUpdateBucketAccess = (projectId: string, bucket: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (access: BucketAccess) =>
      (await api.put<Bucket>(`/projects/${projectId}/storage/buckets/${bucket}/access`, { access })).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['storage', projectId, 'buckets'] }),
  });
};

export interface StorageObject {
  id: string;
  bucketId: string;
  key: string;
  size: number;
  mimeType?: string;
  etag?: string;
  ownerId?: string;
  createdAt: string;
  updatedAt: string;
}

export interface ListObjectsResponse {
  objects: StorageObject[];
  nextCursor?: string;
}

export interface UploadURLResponse {
  // Names the staged upload this URL authorises. The bytes land in a staging
  // area, not on the object's key, so this is what confirm accepts.
  uploadId: string;
  url: string;
  method: 'PUT';
  headers: Record<string, string>;
  expiresAt: string;
}

export interface DownloadURLResponse {
  url: string;
  expiresAt?: string;
  public: boolean;
}

export const useBuckets = (projectId: string) =>
  useQuery({
    queryKey: ['storage', projectId, 'buckets'],
    queryFn: async () => (await api.get<Bucket[]>(`/projects/${projectId}/storage/buckets`)).data,
    enabled: !!projectId,
    staleTime: 30_000,
  });

export const useCreateBucket = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: { name: string; public?: boolean; fileSizeLimit?: number; allowedMimeTypes?: string[] }) =>
      (await api.post<Bucket>(`/projects/${projectId}/storage/buckets`, body)).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['storage', projectId, 'buckets'] }),
  });
};

export const useDeleteBucket = (projectId: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (name: string) => {
      await api.delete(`/projects/${projectId}/storage/buckets/${name}`);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['storage', projectId] }),
  });
};

export const useObjects = (projectId: string, bucket: string, prefix: string = '') =>
  useQuery({
    queryKey: ['storage', projectId, 'objects', bucket, prefix],
    queryFn: async () =>
      (
        await api.get<ListObjectsResponse>(`/projects/${projectId}/storage/buckets/${bucket}/objects`, {
          params: { prefix, limit: 100 },
        })
      ).data,
    enabled: !!projectId && !!bucket,
  });

async function step<T>(name: UploadStep, run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (err) {
    throw uploadFailure(name, err);
  }
}

// useUploadFile combines: mint signed URL → PUT bytes to R2 → confirm.
// Two network calls land on Excalibase (mint + confirm); the bytes go
// directly to R2 to avoid streaming-cost-multipliers.
export const useUploadFile = (projectId: string, bucket: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ file, key }: { file: File; key: string }) => {
      // Each step reports itself on failure, so a refusal says which request
      // failed and with what status (UploadStepError).
      const sign = await step('sign', () =>
        api.post<UploadURLResponse>(`/projects/${projectId}/storage/buckets/${bucket}/upload-url`, {
          key,
          mimeType: file.type || 'application/octet-stream',
          size: file.size,
        }),
      );

      // PUT directly to R2. Use bare axios (no auth interceptor) so the
      // Excalibase Authorization header doesn't leak to Cloudflare. The
      // answer is read as text: a refusal is an S3 XML error body.
      await step('put', () =>
        axios.put(sign.data.url, file, {
          headers: sign.data.headers,
          responseType: 'text',
          // Disable axios's default JSON transform — file goes raw.
          transformRequest: [(data) => data],
        }),
      );

      // Confirm names the staged upload. Size and content type are read back
      // from the object store, so sending them here would achieve nothing.
      const confirm = await step('confirm', () =>
        api.post<StorageObject>(
          `/projects/${projectId}/storage/buckets/${bucket}/confirm-upload`,
          { key, uploadId: sign.data.uploadId },
        ),
      );
      return confirm.data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['storage', projectId, 'objects', bucket] }),
  });
};

export const useDeleteObject = (projectId: string, bucket: string) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (key: string) => {
      await api.delete(`/projects/${projectId}/storage/buckets/${bucket}/objects/${encodeURIComponent(key)}`);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['storage', projectId, 'objects', bucket] }),
  });
};

// useDownloadURL returns a one-shot signed (or public) URL for the user
// to click. Not a useQuery hook because we don't want to cache short-TTL
// URLs that would expire mid-cache.
export const useDownloadURL = (projectId: string, bucket: string) =>
  useMutation({
    mutationFn: async (key: string) =>
      (
        await api.get<DownloadURLResponse>(
          `/projects/${projectId}/storage/buckets/${bucket}/download-url/${encodeURIComponent(key)}`,
        )
      ).data,
  });

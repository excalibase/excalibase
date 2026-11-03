import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// A database's read-only address to its replicas, as GET
// /api/projects/{projectId}/db-read-address serves it (EXC-666). The control
// plane decides every part; Studio prints its answer.

export type ReadAddressReason = 'one-copy' | 'single-host';

export interface ReadAddressInternal {
  host: string;
  port: number;
  connectionString: string;
}

export interface ReadAddress {
  projectId: string;
  engine?: string;
  // Copies serving reads beside the primary, read from the cluster.
  replicas: number;
  // Why there is no read address; absent when there is one.
  reason?: ReadAddressReason;
  singleHost?: boolean;
  internal?: ReadAddressInternal;
  publicOffered: boolean;
  // The primary public port, which the read port needs open.
  publicOpen: boolean;
  enabled: boolean;
  available: boolean;
  host?: string;
  port?: number;
  connectionString?: string;
  canChange: boolean;
}

const key = (projectId: string) => ['read-address', projectId];

export const getReadAddress = async (projectId: string): Promise<ReadAddress> =>
  (await api.get<ReadAddress>(`/projects/${projectId}/db-read-address`)).data;

export const useReadAddress = (projectId: string, enabled: boolean) =>
  useQuery({
    queryKey: key(projectId),
    queryFn: () => getReadAddress(projectId),
    enabled: enabled && !!projectId,
    retry: false,
    staleTime: 30_000,
  });

// Opening the read port is an Admin decision; the server refuses anyone else.
export const setReadAddress = async (projectId: string, enabled: boolean): Promise<ReadAddress> =>
  (await api.put<ReadAddress>(`/projects/${projectId}/db-read-address`, { enabled })).data;

export const useSetReadAddress = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) => setReadAddress(projectId, enabled),
    onSuccess: (view) => queryClient.setQueryData(key(projectId), view),
  });
};

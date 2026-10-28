import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// Who may reach a project's public database port (EXC-665), as
// GET /api/projects/{projectId}/db-endpoint/allowed-sources serves it.
// unset: never chosen (a port opened before allow-lists admits any address);
// any: every address by choice; listed: only `sources`; host: a single host's
// database, on its loopback only.
export type SourceAccess = 'unset' | 'any' | 'listed' | 'host';

export interface AllowedSources {
  projectId: string;
  access: SourceAccess;
  sources: string[];
  max: number;
  publicEnabled: boolean;
  singleHost?: boolean;
  canChange: boolean;
  // The address this browser reaches the platform from, for "Add my address".
  callerAddress: string;
}

export interface AllowedSourcesChange {
  allowAny: boolean;
  sources: string[];
}

const path = (projectId: string) => `/projects/${projectId}/db-endpoint/allowed-sources`;
const key = (projectId: string) => ['allowed-sources', projectId];

export const useAllowedSources = (projectId: string, enabled: boolean) =>
  useQuery({
    queryKey: key(projectId),
    queryFn: async () => (await api.get<AllowedSources>(path(projectId))).data,
    enabled,
    retry: false,
    staleTime: 30_000,
  });

export const useSetAllowedSources = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (change: AllowedSourcesChange) =>
      (await api.put<AllowedSources>(path(projectId), change.allowAny ? { allowAny: true } : { sources: change.sources })).data,
    onSuccess: (saved) => queryClient.setQueryData(key(projectId), saved),
  });
};

// A bare address is the host itself; the server checks everything else.
export const hostRange = (address: string): string =>
  address.includes('/') ? address : `${address}/${address.includes(':') ? 128 : 32}`;

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// A project's private network between its Containers apps (EXC-524), as
// GET /api/projects/{projectId}/app-network serves it. Off by default.
export interface AppNetwork {
  projectId: string;
  // The recorded choice.
  privateNetwork: boolean;
  // Observation: whether the cluster holds the network policy right now.
  applied: boolean;
  // Whether this caller may change it (Admin and up); the server refuses writes anyway.
  canChange?: boolean;
}

const appNetworkKey = (projectId: string) => ['app-network', projectId];

export const useAppNetwork = (projectId: string) =>
  useQuery({
    queryKey: appNetworkKey(projectId),
    queryFn: async () => (await api.get<AppNetwork>(`/projects/${projectId}/app-network`)).data,
  });

export const useSetAppNetwork = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (privateNetwork: boolean) =>
      (await api.put<AppNetwork>(`/projects/${projectId}/app-network`, { privateNetwork })).data,
    onSuccess: (network) => queryClient.setQueryData(appNetworkKey(projectId), network),
  });
};

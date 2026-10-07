import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import { runtimeStatusKey } from '../hooks/useEdgeFunctions';

// The outside hosts a project's edge functions may call (EXC-348). The server
// pins a host without a port to :443 and returns the canonical list.
export interface FunctionEgress {
  allowedHosts: string[];
  defaultHosts: string[];
  effectiveHosts: string[];
}

const egressKey = (projectId: string) => ['function-egress', projectId];

export const useFunctionEgress = (projectId: string) =>
  useQuery({
    queryKey: egressKey(projectId),
    queryFn: async () => (await api.get<FunctionEgress>(`/projects/${projectId}/functions/egress`)).data,
    enabled: !!projectId,
    retry: false,
  });

// Saving replaces the list and redeploys the project's functions with it.
export const useSetFunctionEgress = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (allowedHosts: string[]) =>
      (await api.put<FunctionEgress>(`/projects/${projectId}/functions/egress`, { allowedHosts })).data,
    onSuccess: (egress) => {
      queryClient.setQueryData(egressKey(projectId), egress);
      // The runtime restarts with the new list (EXC-569).
      queryClient.invalidateQueries({ queryKey: runtimeStatusKey(projectId) });
    },
  });
};

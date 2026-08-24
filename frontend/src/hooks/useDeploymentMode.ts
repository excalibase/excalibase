import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';

export type DeploymentMode = 'selfhosted' | 'cloud';

interface ConfigResponse {
  deploymentMode: DeploymentMode;
}

export function useDeploymentMode(): DeploymentMode {
  const { data } = useQuery<ConfigResponse>({
    queryKey: ['config'],
    queryFn: async () => {
      const { data } = await api.get<ConfigResponse>('/config');
      return data;
    },
    staleTime: Infinity, // mode doesn't change at runtime
  });

  return data?.deploymentMode ?? 'selfhosted';
}

export function isSelfHosted(mode: DeploymentMode): boolean {
  return mode === 'selfhosted';
}

export function isCloud(mode: DeploymentMode): boolean {
  return mode === 'cloud';
}

import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';

export type DeploymentMode = 'selfhosted' | 'cloud';

interface ConfigResponse {
  deploymentMode: DeploymentMode;
  appHosting?: boolean;
}

function useStudioConfig() {
  return useQuery<ConfigResponse>({
    queryKey: ['config'],
    queryFn: async () => {
      const { data } = await api.get<ConfigResponse>('/config');
      return data;
    },
    staleTime: Infinity, // mode doesn't change at runtime
  });
}

export function useDeploymentMode(): DeploymentMode {
  const { data } = useStudioConfig();
  return data?.deploymentMode ?? 'selfhosted';
}

// Off until the server says otherwise, so Studio never links to a route the
// server has not mounted.
export function useAppHostingEnabled(): { enabled: boolean; isLoading: boolean } {
  const { data, isLoading } = useStudioConfig();
  return { enabled: data?.appHosting === true, isLoading };
}

export function isSelfHosted(mode: DeploymentMode): boolean {
  return mode === 'selfhosted';
}

export function isCloud(mode: DeploymentMode): boolean {
  return mode === 'cloud';
}

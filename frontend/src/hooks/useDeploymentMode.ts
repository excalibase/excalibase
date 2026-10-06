import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';

export type DeploymentMode = 'selfhosted' | 'cloud';

// Features that ship dark and are turned on per installation (EXC-554).
export type Feature = 'mcp' | 'pipeline';

interface ConfigResponse {
  deploymentMode: DeploymentMode;
  appHosting?: boolean;
  customDomains?: boolean;
  features?: Partial<Record<Feature, boolean>>;
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

// Off unless the server has an ACME issuer for custom domains.
export function useCustomDomainsEnabled(): boolean {
  const { data } = useStudioConfig();
  return data?.customDomains === true;
}

// Off until the server says the feature is on, so Studio never shows a
// page whose calls the server answers with 404.
export function useFeatureEnabled(feature: Feature): { enabled: boolean; isLoading: boolean } {
  const { data, isLoading } = useStudioConfig();
  return { enabled: data?.features?.[feature] === true, isLoading };
}

export function isSelfHosted(mode: DeploymentMode): boolean {
  return mode === 'selfhosted';
}

export function isCloud(mode: DeploymentMode): boolean {
  return mode === 'cloud';
}

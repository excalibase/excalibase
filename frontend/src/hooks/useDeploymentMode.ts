import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';

export type DeploymentMode = 'selfhosted' | 'cloud';

// Features that ship dark and are turned on per installation (EXC-554).
export type Feature =
  | 'mcp'
  | 'pipeline'
  | 'appstorage'
  | 'mysql'
  | 'slowqueries'
  | 'requestmetrics'
  | 'advisors'
  | 'metricsendpoint'
  | 'logdrains'
  | 'authoauth'
  | 'authotp'
  | 'authuser'
  | 'authguest'
  | 'authhooks';

interface ConfigResponse {
  deploymentMode: DeploymentMode;
  appHosting?: boolean;
  // Static frontend hosting: on only where SITES_ENABLED and a Cloudflare account are set (EXC-353).
  sites?: boolean;
  customDomains?: boolean;
  // Sites may take custom domains: the Cloudflare zone is set up for them (EXC-614).
  siteCustomDomains?: boolean;
  // Git builds of sites are switched on (EXC-354); single-host still refuses them by name.
  siteGitBuilds?: boolean;
  features?: Partial<Record<Feature, boolean>>;
  // The public API base the SDK, REST and GraphQL calls go to.
  apiUrl?: string;
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

// Off until the server reports sites, so prod and installs without Cloudflare never show the tab.
export function useSitesEnabled(): { enabled: boolean; isLoading: boolean } {
  const { data, isLoading } = useStudioConfig();
  return { enabled: data?.sites === true, isLoading };
}

// Off until the server reports a zone set up for site custom domains.
export function useSiteCustomDomainsEnabled(): { enabled: boolean; isLoading: boolean } {
  const { data, isLoading } = useStudioConfig();
  return { enabled: data?.siteCustomDomains === true, isLoading };
}

// Off until the server switches git builds on (SITES_GIT_BUILDS_ENABLED).
export function useSiteGitBuildsEnabled(): boolean {
  const { data } = useStudioConfig();
  return data?.siteGitBuilds === true;
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

// Every feature the server reports on; empty until it answers.
export function useFeatures(): Partial<Record<Feature, boolean>> {
  const { data } = useStudioConfig();
  return data?.features ?? {};
}

export function isSelfHosted(mode: DeploymentMode): boolean {
  return mode === 'selfhosted';
}

export function isCloud(mode: DeploymentMode): boolean {
  return mode === 'cloud';
}

// The public API base for connect snippets; undefined when the server names none.
export function useApiUrl(): { apiUrl: string | undefined; isLoading: boolean } {
  const { data, isLoading } = useStudioConfig();
  const apiUrl = data?.apiUrl?.trim();
  return { apiUrl: apiUrl || undefined, isLoading };
}

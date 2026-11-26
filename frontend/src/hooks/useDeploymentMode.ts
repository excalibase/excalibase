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
  | 'sitepreviews'
  // On only on a single-host Docker/Podman install (EXC-670).
  | 'hostmetrics'
  | 'sitesserver'
  | 'authhooks'
  | 'authmfa'
  | 'dballowlist'
  | 'dbreadaddress'
  | 'dbcopy'
  | 'majorupgrade'
  | 'appgitbuilds'
  | 's3'
  | 'onetimecredentials'
  // Analytics tabs in the admin console (EXC-898); staff only, off until turned on.
  | 'adminanalytics'
  // The credit ledger: the sign-up credit, the balance and the statement (EXC-866).
  | 'credits'
  // Unverified accounts cannot run containers until they verify with GitHub (EXC-899).
  | 'limitedtrial'
  // The cloud-console layout of Studio (EXC-762); off until the owner turns it on.
  | 'studio_v2';

interface ConfigResponse {
  deploymentMode: DeploymentMode;
  appHosting?: boolean;
  // Static frontend hosting: on only where SITES_ENABLED and a Cloudflare account are set (EXC-353).
  sites?: boolean;
  customDomains?: boolean;
  // Sites may take custom domains: the Cloudflare zone is set up for them (EXC-614).
  siteCustomDomains?: boolean;
  // Git builds of sites are switched on (EXC-354); single-host builds only with FEATURE_SINGLEHOSTBUILDS (EXC-671), else refuses by name.
  siteGitBuilds?: boolean;
  features?: Partial<Record<Feature, boolean>>;
  // The public API base the SDK, REST and GraphQL calls go to.
  apiUrl?: string;
  // Where the operator console is also served (EXC-757); absent when it is not.
  adminHost?: string;
  // False when the server has the console switched off (a cloud install with no admin host).
  adminConsole?: boolean;
}

// Browser tests start the dev server with VITE_FORCE_V2=1 to run the whole suite on
// the new layout; a production build ignores it, so it can never ship on.
function withForcedStudioV2(config: ConfigResponse): ConfigResponse {
  if (!import.meta.env.DEV || import.meta.env.VITE_FORCE_V2 !== '1') return config;
  return { ...config, features: { ...config.features, studio_v2: true } };
}

function useStudioConfig() {
  return useQuery<ConfigResponse>({
    queryKey: ['config'],
    queryFn: async () => {
      const { data } = await api.get<ConfigResponse>('/config');
      return data;
    },
    select: withForcedStudioV2,
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

// The new Studio layout: off while the server has not answered, so a signed-in
// page never flashes the new shell before the flag is known.
export function useStudioV2(): { enabled: boolean; isLoading: boolean } {
  return useFeatureEnabled('studio_v2');
}

// The flag for a page that waits for it: loading only until the first answer or
// failure. A failed /config is retried each time a page mounts, and that retry
// must not put a spinner over a page whose layout already settled on "off".
export function useStudioV2Settling(): { enabled: boolean; loading: boolean } {
  const { data, isLoading, errorUpdateCount } = useStudioConfig();
  return { enabled: data?.features?.studio_v2 === true, loading: isLoading && errorUpdateCount === 0 };
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

// useIsAdminHost is true on the hostname the operator console is served at, so
// the root page there is the console and not a customer's dashboard.
export function useIsAdminHost(currentHost: string = globalThis.location?.hostname ?? ''): boolean {
  const { data } = useStudioConfig();
  const adminHost = data?.adminHost?.trim().toLowerCase();
  return !!adminHost && currentHost.toLowerCase() === adminHost;
}

// AdminRouteRule says whether the console's pages may be shown on this host:
// with an admin host configured they are served there and nowhere else, so any
// other host gets the page a missing route gets (EXC-757).
export type AdminRouteRule = 'loading' | 'allowed' | 'refused';

export function useAdminRouteRule(currentHost: string = globalThis.location?.hostname ?? ''): AdminRouteRule {
  const { data, isLoading } = useStudioConfig();
  if (isLoading) return 'loading';
  if (data?.adminConsole === false) return 'refused';
  const adminHost = data?.adminHost?.trim().toLowerCase();
  if (!adminHost) return 'allowed';
  return currentHost.toLowerCase() === adminHost ? 'allowed' : 'refused';
}

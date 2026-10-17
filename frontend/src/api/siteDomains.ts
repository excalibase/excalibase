import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// Custom domains of a site (EXC-614): the user's own hostname, through Cloudflare for SaaS.
export type SiteDomainStatus = 'PENDING' | 'ACTIVE' | 'FAILED';
export type SiteDomainValidation = 'http' | 'txt';

export interface DnsInstruction {
  type: 'CNAME' | 'TXT';
  name: string;
  value: string;
  purpose: 'routing' | 'verification' | 'ownership' | 'certificate';
}

export interface SiteDomain {
  id: string;
  siteId: string;
  projectId: string;
  hostname: string;
  validation: SiteDomainValidation;
  status: SiteDomainStatus;
  hostnameStatus: string;
  certificateStatus: string;
  records: DnsInstruction[];
  errors: string[];
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  checkedAt: string;
  activatedAt?: string;
}

export interface SiteDomainInput {
  hostname: string;
  validation: SiteDomainValidation;
}

// The server checks waiting domains every two minutes; the list follows while one waits.
const WAITING_REFETCH_MS = 20_000;

const domainsBase = (projectId: string, siteId: string) =>
  `/projects/${projectId}/sites/${siteId}/domains`;
const domainsKey = (projectId: string, siteId: string) =>
  ['sites', projectId, siteId, 'domains'] as const;

export const listSiteDomains = async (projectId: string, siteId: string): Promise<SiteDomain[]> =>
  (await api.get<SiteDomain[]>(domainsBase(projectId, siteId))).data;

export const addSiteDomain = async (
  projectId: string,
  siteId: string,
  input: SiteDomainInput,
): Promise<SiteDomain> => (await api.post<SiteDomain>(domainsBase(projectId, siteId), input)).data;

export const checkSiteDomain = async (
  projectId: string,
  siteId: string,
  domainId: string,
): Promise<SiteDomain> =>
  (await api.post<SiteDomain>(`${domainsBase(projectId, siteId)}/${domainId}/refresh`)).data;

export const removeSiteDomain = async (
  projectId: string,
  siteId: string,
  domainId: string,
): Promise<void> => {
  await api.delete(`${domainsBase(projectId, siteId)}/${domainId}`);
};

export const useSiteDomains = (projectId: string, siteId: string, enabled: boolean) =>
  useQuery({
    queryKey: domainsKey(projectId, siteId),
    queryFn: () => listSiteDomains(projectId, siteId),
    enabled: enabled && !!projectId && !!siteId,
    refetchInterval: (query) =>
      query.state.data?.some((domain) => domain.status === 'PENDING') ? WAITING_REFETCH_MS : false,
  });

const useDomainChange = <T>(
  projectId: string,
  siteId: string,
  change: (input: T) => Promise<unknown>,
) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: change,
    onSettled: () => qc.invalidateQueries({ queryKey: domainsKey(projectId, siteId) }),
  });
};

export const useAddSiteDomain = (projectId: string, siteId: string) =>
  useDomainChange(projectId, siteId, (input: SiteDomainInput) =>
    addSiteDomain(projectId, siteId, input),
  );

export const useCheckSiteDomain = (projectId: string, siteId: string) =>
  useDomainChange(projectId, siteId, (domainId: string) =>
    checkSiteDomain(projectId, siteId, domainId),
  );

export const useRemoveSiteDomain = (projectId: string, siteId: string) =>
  useDomainChange(projectId, siteId, (domainId: string) =>
    removeSiteDomain(projectId, siteId, domainId),
  );

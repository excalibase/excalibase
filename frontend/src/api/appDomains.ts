import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

export type DomainStatus = 'pending' | 'issuing' | 'active' | 'issue_failed' | 'detached';

export interface AppDomain {
  id: string;
  hostname: string;
  status: DomainStatus;
  failureReason?: string;
  consecutiveFailures: number;
  cnameTarget: string;
}

const base = (projectId: string, appId: string) => `/projects/${projectId}/apps/${appId}/domains`;
const key = (projectId: string, appId: string) => ['apps', projectId, appId, 'domains'] as const;

// Polls while a certificate is being issued, so the status moves on its own.
export const useAppDomains = (projectId: string, appId: string, pollMs = 5000) =>
  useQuery({
    queryKey: key(projectId, appId),
    queryFn: async () => (await api.get<AppDomain[]>(`${base(projectId, appId)}/`)).data,
    refetchInterval: (query) =>
      query.state.data?.some((d) => d.status === 'issuing') ? pollMs : false,
  });

// The app's own hostname's certificate; 'none' until the app is deployed, and for an internal service.
export interface HostCertificate {
  hostname?: string;
  status: Exclude<DomainStatus, 'pending' | 'detached'> | 'none';
  failureReason?: string;
}

export const useAppHostCertificate = (projectId: string, appId: string, pollMs = 5000) =>
  useQuery({
    queryKey: ['apps', projectId, appId, 'certificate'] as const,
    queryFn: async () =>
      (await api.get<HostCertificate>(`/projects/${projectId}/apps/${appId}/certificate`)).data,
    refetchInterval: (query) => (query.state.data?.status === 'issuing' ? pollMs : false),
  });

const useDomainMutation = <T>(
  projectId: string,
  appId: string,
  run: (arg: T) => Promise<unknown>,
) => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: run,
    onSettled: () => qc.invalidateQueries({ queryKey: key(projectId, appId) }),
  });
};

export const useAddAppDomain = (projectId: string, appId: string) =>
  useDomainMutation(projectId, appId, (hostname: string) =>
    api.post(`${base(projectId, appId)}/`, { hostname }),
  );

export const useVerifyAppDomain = (projectId: string, appId: string) =>
  useDomainMutation(projectId, appId, (id: string) =>
    api.post(`${base(projectId, appId)}/${id}/verify`),
  );

export const useRemoveAppDomain = (projectId: string, appId: string) =>
  useDomainMutation(projectId, appId, (id: string) =>
    api.delete(`${base(projectId, appId)}/${id}`),
  );

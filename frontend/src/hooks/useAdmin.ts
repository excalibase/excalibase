import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';

// AdminProject is the cross-org row returned by /api/admin/projects.
// cpuCores/memBytes are absent when Prometheus isn't wired in the backend.
export interface AdminProject {
  projectId: string;
  projectName: string;
  orgId: string;
  namespace: string;
  status: string;
  tier: string;
  dbType: string;
  createdAt: string | { time: string };
  deletionProtection: boolean;
  cpuCores?: number;
  memBytes?: number;
}

// ClusterCapacity mirrors the JSON shape from /api/capacity. The "tiers"
// map is keyed by lowercase tier name.
export interface ClusterCapacity {
  allocatableCpuMilli: number;
  allocatableMemBytes: number;
  usableCpuMilli: number;
  usableMemBytes: number;
  headroomPercent: number;
  requestedCpuMilli: number;
  requestedMemBytes: number;
  freeCpuMilli: number;
  freeMemBytes: number;
  projects: { total: number; byTier: Record<string, number> };
  tiers: Record<
    string,
    { projectsCanFit: number; perProjectCpuMilli: number; perProjectMemBytes: number; limitedBy: 'cpu' | 'memory'; currentlyProvisioned: number }
  >;
}

export const useAdminProjects = () =>
  useQuery({
    queryKey: ['admin', 'projects'],
    queryFn: async () => (await api.get<AdminProject[]>('/admin/projects')).data,
    staleTime: 15_000,
  });

export const useClusterCapacity = () =>
  useQuery({
    queryKey: ['admin', 'capacity'],
    queryFn: async () => (await api.get<ClusterCapacity>('/capacity')).data,
    staleTime: 30_000,
  });

export const useForceDropProject = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (projectId: string) => {
      await api.delete(`/admin/projects/${projectId}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'projects'] });
      qc.invalidateQueries({ queryKey: ['admin', 'capacity'] });
      qc.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

export const useRevokeOrg = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (orgId: string) => {
      await api.delete(`/admin/orgs/${orgId}?cascade=true`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'projects'] });
      qc.invalidateQueries({ queryKey: ['admin', 'capacity'] });
      qc.invalidateQueries({ queryKey: ['orgs'] });
    },
  });
};

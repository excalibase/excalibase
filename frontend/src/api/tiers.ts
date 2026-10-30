import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import type { TierType } from '../types';

// TierConfig mirrors the backend tier_configs row (GET /api/tiers,
// PUT /api/admin/tiers/{tier}). Specs are CPU/RAM/storage quantity strings as
// CNPG expects them ("0.5", "4Gi", "50Gi"). instances is the CNPG replica count
// (1 = no HA on the current single-node alpha).
export interface TierConfig {
  tier: TierType;
  // Sent back as If-Match: a save made from an older version is refused (412).
  version: number;
  maxProjects: number;
  instances: number;
  storageSize: string;
  // The most a project on the plan may grow its disk to; storageSize is where it starts.
  maxStorageSize: string;
  // The largest disk one container may have on the plan; 0Gi offers none.
  maxAppDiskSize: string;
  // How many containers one project may hold on the plan; 0 offers none.
  maxApps: number;
  memory: string;
  cpu: string;
  backupEnabled: boolean;
  // Idle days before an ACTIVE project is auto-paused (warning one day earlier); 0 = never.
  autoPauseAfterDays: number;
  // A project's Sites on the plan (EXC-620): how many, each deploy's size and file
  // limits, and deploys an hour. 0 sites offers none.
  maxSites: number;
  maxSiteDeployBytes: number;
  maxSiteFiles: number;
  maxSiteFileBytes: number;
  maxSiteDeploysPerHour: number;
  // Custom domains one project's sites may hold (EXC-614); 0 offers none.
  maxSiteCustomDomains: number;
  // One git build of a site on the plan (EXC-354): its CPU, memory, disk and time,
  // and how many of a project's builds run at once. Builds count toward deploys an hour.
  siteBuildCpuMillis?: number;
  siteBuildMemory?: string;
  siteBuildDisk?: string;
  siteBuildTimeoutSeconds?: number;
  siteBuildConcurrency?: number;
  // One git build of an app on the plan (EXC-547), a separate allowance from what
  // the project runs, plus the largest image a build may hand to a deploy.
  appBuildCpuMillis?: number;
  appBuildMemory?: string;
  appBuildDisk?: string;
  appBuildTimeoutSeconds?: number;
  appBuildConcurrency?: number;
  appBuildMaxImage?: string;
  // How many app builds one project may queue in any hour.
  appBuildMaxPerHour?: number;
}

export type TierConfigInput = Omit<TierConfig, 'tier' | 'version'>;

// Read is available to any authenticated user (the provision page selector).
export const listTiers = async (): Promise<TierConfig[]> =>
  (await api.get<TierConfig[]>('/tiers')).data;

// Edit is platform_admin only (PermManageSetup), enforced server-side.
export const updateTier = async (
  tier: TierType,
  body: TierConfigInput,
  version: number,
): Promise<TierConfig> =>
  (
    await api.put<TierConfig>(`/admin/tiers/${tier}`, body, {
      headers: { 'If-Match': String(version) },
    })
  ).data;

export const useTiers = () =>
  useQuery({
    queryKey: ['tiers'],
    queryFn: listTiers,
    staleTime: 60_000,
  });

export const useUpdateTier = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ tier, body, version }: { tier: TierType; body: TierConfigInput; version: number }) =>
      updateTier(tier, body, version),
    // A refused save reloads too, so the next edit starts from the current version.
    onSettled: () => qc.invalidateQueries({ queryKey: ['tiers'] }),
  });
};

const TIER_ORDER: Record<string, number> = { FREE: 0, STANDARD: 1, ENTERPRISE: 2 };

// sortTiers gives a stable FREE → STANDARD → ENTERPRISE order for display
// (the list endpoint returns map order, which is unspecified).
export const sortTiers = (tiers: TierConfig[]): TierConfig[] =>
  [...tiers].sort((a, b) => (TIER_ORDER[a.tier] ?? 99) - (TIER_ORDER[b.tier] ?? 99));

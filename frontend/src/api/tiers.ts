import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import type { TierType } from '../types';

// TierConfig mirrors the backend tier_configs row (GET /api/tiers,
// PUT /api/admin/tiers/{tier}). Specs are CPU/RAM/storage quantity strings as
// CNPG expects them ("0.5", "4Gi", "50Gi"). instances is the CNPG replica count
// (1 = no HA on the current single-node alpha).
export interface TierConfig {
  tier: TierType;
  maxProjects: number;
  instances: number;
  storageSize: string;
  memory: string;
  cpu: string;
  backupEnabled: boolean;
  // Idle days before an ACTIVE project is auto-paused (warning one day earlier); 0 = never.
  autoPauseAfterDays: number;
}

export type TierConfigInput = Omit<TierConfig, 'tier'>;

// Read is available to any authenticated user (the provision page selector).
export const listTiers = async (): Promise<TierConfig[]> =>
  (await api.get<TierConfig[]>('/tiers')).data;

// Edit is platform_admin only (PermManageSetup), enforced server-side.
export const updateTier = async (tier: TierType, body: TierConfigInput): Promise<TierConfig> =>
  (await api.put<TierConfig>(`/admin/tiers/${tier}`, body)).data;

export const useTiers = () =>
  useQuery({
    queryKey: ['tiers'],
    queryFn: listTiers,
    staleTime: 60_000,
  });

export const useUpdateTier = () => {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ tier, body }: { tier: TierType; body: TierConfigInput }) => updateTier(tier, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tiers'] }),
  });
};

const TIER_ORDER: Record<string, number> = { FREE: 0, STANDARD: 1, ENTERPRISE: 2 };

// sortTiers gives a stable FREE → STANDARD → ENTERPRISE order for display
// (the list endpoint returns map order, which is unspecified).
export const sortTiers = (tiers: TierConfig[]): TierConfig[] =>
  [...tiers].sort((a, b) => (TIER_ORDER[a.tier] ?? 99) - (TIER_ORDER[b.tier] ?? 99));

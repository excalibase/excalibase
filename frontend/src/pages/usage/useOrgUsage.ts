import { useCurrentOrg, useMyProjects } from '../../hooks/useMemberOrgs';
import { useTiers } from '../../api/tiers';
import { useProjectServices } from '../home/useHomeData';
import { orgUsage, type OrgUsage } from './usageModel';
import type { TierConfig } from '../../api/tiers';

export interface OrgUsageState {
  orgName?: string;
  tierName?: string;
  tier?: TierConfig;
  usage?: OrgUsage;
  isLoading: boolean;
  failed: boolean;
}

// The current org's plan and use, from the lists the Studio already reads. Nothing here is metered live.
export function useOrgUsage(): OrgUsageState {
  const { org, isLoading: orgLoading, error: orgError } = useCurrentOrg();
  const mine = useMyProjects();
  const tiers = useTiers();
  const projects = org ? mine.data.filter((p) => p.orgId === org.id) : [];
  const { byProject } = useProjectServices(projects);
  const tier = tiers.data?.find((t) => t.tier === org?.tier);
  const failed = Boolean(orgError ?? mine.error);
  return {
    orgName: org?.name,
    tierName: org?.tier,
    tier,
    usage: org && !failed ? orgUsage(projects, tier, byProject) : undefined,
    isLoading: orgLoading || mine.isLoading,
    failed,
  };
}

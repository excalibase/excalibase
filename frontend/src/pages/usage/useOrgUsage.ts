import { useCurrentOrg, useMyProjects } from '../../hooks/useMemberOrgs';
import { isSelfHosted, useAppHostingEnabled, useDeploymentMode } from '../../hooks/useDeploymentMode';
import { useTiers } from '../../api/tiers';
import { useProjectServices } from '../home/useHomeData';
import { orgUsage, type OrgUsage, type UsageOptions } from './usageModel';
import type { TierConfig } from '../../api/tiers';

export interface OrgUsageState {
  orgName?: string;
  tierName?: string;
  tier?: TierConfig;
  usage?: OrgUsage;
  isLoading: boolean;
  failed: boolean;
}

// What the install changes about the numbers; empty until the config answers, so a limit never flashes.
export function useUsageOptions(): UsageOptions & { ready: boolean } {
  const mode = useDeploymentMode();
  const apps = useAppHostingEnabled();
  const ready = !apps.isLoading;
  return { selfHosted: ready && isSelfHosted(mode), appsOn: apps.enabled, ready };
}

// The current org's plan and use, from the lists the Studio already reads. Nothing here is metered live.
export function useOrgUsage(): OrgUsageState {
  const { org, isLoading: orgLoading, error: orgError } = useCurrentOrg();
  const mine = useMyProjects();
  const tiers = useTiers();
  const options = useUsageOptions();
  const projects = org ? mine.data.filter((p) => p.orgId === org.id) : [];
  const { byProject } = useProjectServices(projects);
  const tier = tiers.data?.find((t) => t.tier === org?.tier);
  const failed = Boolean(orgError ?? mine.error);
  return {
    orgName: org?.name,
    tierName: org?.tier,
    tier,
    usage: org && !failed ? orgUsage(projects, tier, byProject, options) : undefined,
    isLoading: orgLoading || mine.isLoading,
    failed,
  };
}

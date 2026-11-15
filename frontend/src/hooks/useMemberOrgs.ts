import { useQuery } from '@tanstack/react-query';
import { listMyOrgs, type Org } from '../api/orgs';
import { useAuthStore } from '../stores/auth-store';
import { useOrgSelection } from '../stores/org-selection';
import { useInstances } from './useProvisioning';
import { useRouteProjectId } from './useRouteProjectId';
import type { DatabaseInstance } from '../types';

// GET /orgs answers a platform admin with EVERY org, and marks only the orgs they
// belong to with a role. An org the caller is not a member of is never offered
// here: it is kept when the listing gives a role, or when the caller owns it.
export function memberOrgsOf(orgs: readonly Org[], userId: string | undefined): Org[] {
  if (!userId) return [];
  return orgs.filter((org) => Boolean(org.role) || org.ownerId === userId);
}

export function useMemberOrgs() {
  const userId = useAuthStore((s) => s.user?.id);
  const query = useQuery({ queryKey: ['orgs', 'mine'], queryFn: listMyOrgs, staleTime: 60_000 });
  return { ...query, orgs: memberOrgsOf(query.data ?? [], userId) };
}

// The projects of the orgs the person belongs to; the same listing an admin gets
// from /provision is narrowed here, since other customers live in the admin console only.
export function useMyProjects() {
  const instances = useInstances();
  const orgs = useMemberOrgs();
  const ids = new Set(orgs.orgs.map((o) => o.id));
  const data: DatabaseInstance[] = (instances.data ?? []).filter((p) => ids.has(p.orgId));
  return {
    data,
    isLoading: instances.isLoading || orgs.isLoading,
    error: instances.error ?? orgs.error,
    refetch: () => { void instances.refetch(); void orgs.refetch(); },
  };
}

// On a project page the org is the project's; elsewhere the person's last pick, else their first org.
export function useCurrentOrg(): { org: Org | undefined; orgs: Org[]; isLoading: boolean; error: unknown } {
  const { orgs, isLoading, error } = useMemberOrgs();
  const selected = useOrgSelection((s) => s.orgId);
  const projectId = useRouteProjectId();
  const { data: instances } = useInstances();
  const projectOrg = projectId ? instances?.find((p) => p.projectId === projectId)?.orgId : undefined;
  const org = orgs.find((o) => o.id === projectOrg) ?? orgs.find((o) => o.id === selected) ?? orgs[0];
  return { org, orgs, isLoading, error };
}

export function planLabel(tier: string): string {
  return tier.charAt(0).toUpperCase() + tier.slice(1).toLowerCase();
}

import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import { listMyOrgs } from '../api/orgs';
import { useAuthStore } from '../stores/auth-store';
import type { DatabaseInstance } from '../types';

// The caller's org role on a project, as the control plane's
// RequireProjectRole reads it: the membership role in the project's org
// (Owner ⊇ Admin ⊇ Developer ⊇ Viewer), everything for a platform admin.
// Only hides controls; the server enforces the same rule on every call.

const RANK: Record<string, number> = { viewer: 1, developer: 2, admin: 3, owner: 4 };

export interface ProjectRole {
  role: string | null;
  canDevelop: boolean;
  canAdmin: boolean;
  isLoading: boolean;
}

export function useProjectRole(projectId: string): ProjectRole {
  const platformAdmin = useAuthStore((s) => s.user?.role === 'platform_admin');
  const project = useQuery({
    queryKey: ['instance', projectId],
    queryFn: async () => (await api.get<DatabaseInstance>(`/provision/${projectId}`)).data,
    enabled: !!projectId && !platformAdmin,
    staleTime: 30_000,
  });
  const orgs = useQuery({
    queryKey: ['my-orgs'],
    queryFn: listMyOrgs,
    enabled: !platformAdmin,
    staleTime: 60_000,
  });

  if (platformAdmin) return { role: 'owner', canDevelop: true, canAdmin: true, isLoading: false };

  const orgId = project.data?.orgId;
  const role = orgs.data?.find((org) => org.id === orgId)?.role ?? null;
  const rank = role ? (RANK[role] ?? 0) : 0;
  return {
    role,
    canDevelop: rank >= RANK.developer,
    canAdmin: rank >= RANK.admin,
    isLoading: project.isLoading || orgs.isLoading,
  };
}

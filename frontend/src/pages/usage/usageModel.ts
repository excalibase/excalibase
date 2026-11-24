import type { TierConfig } from '../../api/tiers';
import type { DatabaseInstance } from '../../types';
import type { LoadedServices } from '../home/useHomeData';

const GONE = new Set(['PENDING_DELETION', 'DELETING', 'DEPROVISIONED', 'BACKUPS_PENDING_DELETE']);
const UNITS: Record<string, number> = { Mi: 1 / 1024, Gi: 1, Ti: 1024 };

// A Kubernetes-style quantity ("5Gi", "512Mi") in gibibytes; null when absent or not one.
export function parseGiB(quantity: string | undefined): number | null {
  const match = /^(\d+(?:\.\d+)?)(Mi|Gi|Ti)$/.exec(quantity ?? '');
  return match ? Number(match[1]) * UNITS[match[2]] : null;
}

export interface ProjectUsage { projectId: string; name: string; apps: number | null; storageGiB: number | null }

export interface OrgUsage {
  // limit null: the plan sets none, or the plan list is not loaded (see limitsKnown).
  projects: { used: number; limit: number | null };
  databases: number;
  storageGiB: number | null;
  perProject: ProjectUsage[];
  limitsKnown: boolean;
}

export function orgUsage(projects: readonly DatabaseInstance[], tier: TierConfig | undefined, services: ReadonlyMap<string, LoadedServices>): OrgUsage {
  const live = projects.filter((p) => !GONE.has(p.status));
  const perProject = live.map((p) => ({
    projectId: p.projectId,
    name: p.projectName || p.projectId,
    apps: services.get(p.projectId)?.apps?.length ?? null,
    storageGiB: parseGiB(p.storageSize),
  }));
  const sizes = perProject.map((p) => p.storageGiB).filter((v): v is number => v !== null);
  return {
    projects: { used: live.length, limit: tier && tier.maxProjects > 0 ? tier.maxProjects : null },
    databases: live.filter((p) => p.noDatabase !== true).length,
    storageGiB: sizes.length > 0 ? sizes.reduce((a, b) => a + b, 0) : null,
    perProject,
    limitsKnown: tier !== undefined,
  };
}

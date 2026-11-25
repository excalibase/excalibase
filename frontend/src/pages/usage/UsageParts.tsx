import type { ReactNode } from 'react';
import type { OrgUsage } from './usageModel';
import type { TierConfig } from '../../api/tiers';

export function Section({ title, children }: { readonly title: string; readonly children: ReactNode }) {
  return (
    <section aria-label={title} className="space-y-3 rounded-xl border border-border-primary bg-surface-card p-4 text-sm">
      <h2 className="text-[15px] font-semibold text-text-primary">{title}</h2>
      {children}
    </section>
  );
}

export function Line({ label, value }: { readonly label: string; readonly value: ReactNode }) {
  return (
    <div className="flex justify-between gap-3 border-b border-border-primary py-1.5 text-text-primary last:border-0">
      <span>{label}</span><span className="font-medium">{value}</span>
    </div>
  );
}

export const gib = (value: number) => `${Number.isInteger(value) ? value : value.toFixed(1)} GiB`;

export function projectsValue(projects: OrgUsage['projects'], limitsKnown: boolean): string {
  if (projects.limit !== null) return `${projects.used} of ${projects.limit}`;
  return limitsKnown ? `${projects.used} (no limit)` : String(projects.used);
}

export function appsValue(apps: number | null, tier: TierConfig | undefined): string {
  if (apps === null) return '—';
  return tier ? `${apps} of ${tier.maxApps}` : String(apps);
}

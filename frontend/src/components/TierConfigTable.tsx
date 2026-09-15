import { useState } from 'react';
import { Loader2, Save } from 'lucide-react';
import { Button } from './Button';
import { useTiers, useUpdateTier, sortTiers, type TierConfig, type TierConfigInput } from '../api/tiers';

// TierConfigTable is the platform-admin editor for the tier_configs table.
// Tier specs (CPU/RAM/storage/maxProjects/instances/backup) are now DB-backed,
// so edits here take effect on the next provision/scale with no redeploy.
// Listing is available to operators; only platform_admin (canMutate) can save.
export function TierConfigTable({ canMutate }: { readonly canMutate: boolean }) {
  const { data: tiers, isLoading } = useTiers();
  const update = useUpdateTier();

  // Local draft of edits keyed by tier. A row with no entry renders the server
  // value (see `toInput` fallback below), so there's no need to pre-seed from
  // an effect — edits merge onto the row's current effective value.
  const [draft, setDraft] = useState<Record<string, TierConfigInput>>({});

  const toInput = (t: TierConfig): TierConfigInput => ({
    maxProjects: t.maxProjects, instances: t.instances, storageSize: t.storageSize,
    memory: t.memory, cpu: t.cpu, backupEnabled: t.backupEnabled,
    autoPauseAfterDays: t.autoPauseAfterDays ?? 0,
  });

  const setField = (tier: string, base: TierConfigInput, patch: Partial<TierConfigInput>) =>
    setDraft((d) => ({ ...d, [tier]: { ...base, ...patch } }));

  const isDirty = (t: TierConfig): boolean => {
    const d = draft[t.tier];
    if (d == null) return false;
    return (
      d.cpu !== t.cpu || d.memory !== t.memory || d.storageSize !== t.storageSize ||
      d.maxProjects !== t.maxProjects || d.instances !== t.instances || d.backupEnabled !== t.backupEnabled ||
      d.autoPauseAfterDays !== (t.autoPauseAfterDays ?? 0)
    );
  };

  const save = async (t: TierConfig) => {
    const body = draft[t.tier];
    if (body == null) return;
    try {
      await update.mutateAsync({ tier: t.tier, body });
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } }; message?: string };
      alert(`Save failed: ${err?.response?.data?.error ?? err?.message ?? 'unknown error'}`);
    }
  };

  return (
    <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
      <div className="px-6 py-4 border-b border-border-primary">
        <h2 className="text-lg font-semibold">Tier configuration</h2>
        <p className="text-xs text-text-tertiary mt-0.5">
          DB-backed CPU / RAM / storage per tier. Changes apply to new provisions and tier scales — no redeploy.
          {!canMutate && ' Read-only (platform_admin required to edit).'}
        </p>
      </div>
      {isLoading && (
        <div className="flex items-center justify-center py-12">
          <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
        </div>
      )}
      {!isLoading && (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary">
              <th className="text-left px-6 py-3 font-medium">Tier</th>
              <th className="text-left px-4 py-3 font-medium">vCPU</th>
              <th className="text-left px-4 py-3 font-medium">Memory</th>
              <th className="text-left px-4 py-3 font-medium">Storage</th>
              <th className="text-left px-4 py-3 font-medium">Instances</th>
              <th className="text-left px-4 py-3 font-medium">Max projects</th>
              <th className="text-left px-4 py-3 font-medium">Backup</th>
              <th className="text-left px-4 py-3 font-medium" title="Idle days before auto-pause (0 = never)">Auto-pause (days)</th>
              <th className="px-4 py-3" />
            </tr>
          </thead>
          <tbody>
            {sortTiers(tiers ?? []).map((t) => {
              const d = draft[t.tier] ?? toInput(t);
              return (
                <tr key={t.tier} className="border-b border-border-primary last:border-b-0">
                  <td className="px-6 py-3 font-medium uppercase text-xs">{t.tier}</td>
                  <td className="px-4 py-3"><QtyInput value={d.cpu} disabled={!canMutate} onChange={(v) => setField(t.tier, d, { cpu: v })} placeholder="0.5" /></td>
                  <td className="px-4 py-3"><QtyInput value={d.memory} disabled={!canMutate} onChange={(v) => setField(t.tier, d, { memory: v })} placeholder="4Gi" /></td>
                  <td className="px-4 py-3"><QtyInput value={d.storageSize} disabled={!canMutate} onChange={(v) => setField(t.tier, d, { storageSize: v })} placeholder="50Gi" /></td>
                  <td className="px-4 py-3"><NumInput value={d.instances} disabled={!canMutate} min={1} onChange={(v) => setField(t.tier, d, { instances: v })} /></td>
                  <td className="px-4 py-3"><NumInput value={d.maxProjects} disabled={!canMutate} min={0} onChange={(v) => setField(t.tier, d, { maxProjects: v })} /></td>
                  <td className="px-4 py-3">
                    <input
                      type="checkbox"
                      checked={d.backupEnabled}
                      disabled={!canMutate}
                      onChange={(e) => setField(t.tier, d, { backupEnabled: e.target.checked })}
                      className="h-4 w-4 accent-accent-primary disabled:opacity-50"
                    />
                  </td>
                  <td className="px-4 py-3"><NumInput value={d.autoPauseAfterDays} disabled={!canMutate} min={0} onChange={(v) => setField(t.tier, d, { autoPauseAfterDays: v })} /></td>
                  <td className="px-4 py-3 text-right">
                    <Button
                      size="sm"
                      disabled={!canMutate || !isDirty(t) || update.isPending}
                      onClick={() => save(t)}
                    >
                      <Save className="w-3.5 h-3.5 mr-1" />
                      Save
                    </Button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

function QtyInput({ value, onChange, disabled, placeholder }: { readonly value: string; readonly onChange: (v: string) => void; readonly disabled?: boolean; readonly placeholder?: string }) {
  return (
    <input
      type="text"
      value={value}
      placeholder={placeholder}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
      className="w-20 px-2 py-1 rounded-md bg-bg-tertiary border border-border-primary text-text-primary disabled:opacity-50 focus:outline-none focus:border-accent-primary"
    />
  );
}

function NumInput({ value, onChange, disabled, min }: { readonly value: number; readonly onChange: (v: number) => void; readonly disabled?: boolean; readonly min?: number }) {
  return (
    <input
      type="number"
      value={value}
      min={min}
      disabled={disabled}
      onChange={(e) => onChange(Number(e.target.value))}
      className="w-16 px-2 py-1 rounded-md bg-bg-tertiary border border-border-primary text-text-primary disabled:opacity-50 focus:outline-none focus:border-accent-primary"
    />
  );
}

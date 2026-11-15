import { useState } from 'react';
import { Loader2, ShieldAlert } from 'lucide-react';
import { Button } from '../Button';
import { serverErrorMessage } from '../../utils/serverError';
import { useConsoleOrgs } from '../../hooks/useAdminConsole';
import { useRevokeOrg, useSetOrgTier } from '../../hooks/useAdmin';
import { AccountStatusBadge } from './AccountStatusBadge';
import { ReasonDialog } from './ReasonDialog';

// Every org with who owns it, what it holds and what it uses.
export function OrgsTable({ canMutate }: { readonly canMutate: boolean }) {
  const [showSuspended, setShowSuspended] = useState(false);
  const { data, isLoading, error } = useConsoleOrgs({ includeSuspended: showSuspended, limit: 100 });
  const revoke = useRevokeOrg();
  const setTier = useSetOrgTier();
  const [planFor, setPlanFor] = useState<{ id: string; slug: string; tier: string } | null>(null);
  const [chosenTier, setChosenTier] = useState('FREE');
  const [pending, setPending] = useState<{ id: string; slug: string; ownerEmail?: string; projects: number } | null>(null);

  const confirmPlan = async (reason: string) => {
    if (!planFor || !canMutate) return;
    try {
      await setTier.mutateAsync({ orgId: planFor.id, tier: chosenTier, reason });
      setPlanFor(null);
    } catch {
      // the dialog stays open and shows setTier.error
    }
  };

  const confirmRevoke = async (reason: string) => {
    if (!pending || !canMutate) return;
    try {
      await revoke.mutateAsync({ orgId: pending.id, reason });
      setPending(null);
    } catch {
      // the dialog stays open and shows revoke.error
    }
  };

  return (
    <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
      <div className="px-6 py-4 border-b border-border-primary flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold">Orgs</h2>
          <p className="text-xs text-text-tertiary mt-0.5">Revoking cascades through every project in the org.</p>
        </div>
        <div className="flex items-center gap-4">
          <label className="flex items-center gap-1.5 text-sm text-text-secondary">
            <input type="checkbox" checked={showSuspended} onChange={(event) => setShowSuspended(event.target.checked)} />
            Show suspended
          </label>
          <span className="text-sm text-text-secondary">{data?.total ?? 0}</span>
        </div>
      </div>
      {isLoading && (
        <div className="flex justify-center py-10">
          <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
        </div>
      )}
      {error && <p role="alert" className="px-6 py-4 text-sm text-red-400">{serverErrorMessage(error, 'The orgs could not be read')}</p>}
      {data && (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary">
              <th className="text-left px-4 py-3 font-medium">Org</th>
              <th className="text-left px-4 py-3 font-medium">Owner</th>
              <th className="text-left px-4 py-3 font-medium">Tier</th>
              <th className="text-right px-4 py-3 font-medium">Projects</th>
              <th className="text-right px-4 py-3 font-medium">Apps running</th>
              <th className="text-left px-4 py-3 font-medium">Usage</th>
              <th className="px-4 py-3" />
            </tr>
          </thead>
          <tbody>
            {data.items.map((org) => (
              <tr key={org.id} className="border-b border-border-primary last:border-b-0 hover:bg-bg-hover">
                <td className="px-4 py-3">
                  <div className="font-medium">{org.name}</div>
                  <div className="text-xs text-text-tertiary font-mono">{org.slug}</div>
                </td>
                <td className="px-4 py-3">
                  <div>{org.ownerEmail}</div>
                  <AccountStatusBadge status={org.ownerStatus} />
                </td>
                <td className="px-4 py-3 uppercase text-xs text-text-secondary">{org.tier}</td>
                <td className="px-4 py-3 text-right text-text-secondary">{org.projectCount}</td>
                <td className="px-4 py-3 text-right text-text-secondary">{org.runningApps}</td>
                <td className="px-4 py-3 text-text-secondary">
                  {org.cpuCores || org.memoryMb ? `${org.cpuCores.toFixed(2)} cores · ${Math.round(org.memoryMb)} MiB` : '—'}
                </td>
                <td className="px-4 py-3 text-right space-x-2">
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={!canMutate || setTier.isPending}
                    onClick={() => {
                      setTier.reset();
                      setChosenTier(org.tier.toUpperCase());
                      setPlanFor({ id: org.id, slug: org.slug, tier: org.tier });
                    }}
                  >
                    Change plan
                  </Button>
                  <Button size="sm" variant="danger" disabled={!canMutate || revoke.isPending} onClick={() => { revoke.reset(); setPending({ id: org.id, slug: org.slug, ownerEmail: org.ownerEmail, projects: org.projectCount }); }}>
                    <ShieldAlert className="w-3.5 h-3.5 mr-1" />
                    Revoke
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {planFor && (
        <ReasonDialog
          title={`Change plan for "${planFor.slug}"`}
          description={`Currently ${planFor.tier.toUpperCase()}. A plan that needs more nodes than the platform has is refused.`}
          confirmLabel="Change plan"
          busy={setTier.isPending}
          error={setTier.error ? serverErrorMessage(setTier.error, 'The plan change failed') : undefined}
          onConfirm={confirmPlan}
          onCancel={() => setPlanFor(null)}
        >
          <label className="block text-sm text-text-secondary">
            Plan
            <select
              className="mt-1 w-full rounded-lg bg-bg-secondary border border-border-primary p-2 text-text-primary"
              value={chosenTier}
              onChange={(event) => setChosenTier(event.target.value)}
            >
              {['FREE', 'STANDARD', 'ENTERPRISE'].map((tier) => (
                <option key={tier} value={tier}>
                  {tier}
                </option>
              ))}
            </select>
          </label>
        </ReasonDialog>
      )}
      {pending && (
        <ReasonDialog
          title={`Revoke org "${pending.slug}"`}
          description={`Drops ${pending.projects} project(s), deletes the org, and suspends its owner${pending.ownerEmail ? ` (${pending.ownerEmail})` : ''} so they cannot sign in again. Not reversible.`}
          confirmLabel="Revoke"
          danger
          busy={revoke.isPending}
          error={revoke.error ? serverErrorMessage(revoke.error, 'Revoke failed') : undefined}
          onConfirm={confirmRevoke}
          onCancel={() => setPending(null)}
        />
      )}
    </div>
  );
}

import { useMemo, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ShieldAlert } from 'lucide-react';
import { DataTable, type Column, type FilterDef } from '../../ui/DataTable';
import { Button } from '../../Button';
import { serverErrorMessage } from '../../../utils/serverError';
import { useConsoleOrgs } from '../../../hooks/useAdminConsole';
import { revokeOrgCascade, useRevokeOrg, useSetOrgTier } from '../../../hooks/useAdmin';
import type { ConsoleOrg } from '../../../api/adminConsole';
import { AccountStatusBadge } from '../AccountStatusBadge';
import { ReasonDialog } from '../ReasonDialog';
import { BulkDialog, BulkOutcomeNotice, type BulkOutcome, type BulkRequest } from './BulkAction';
import { refreshAdmin } from './adminRows';
import { ORG_SORTS } from './listQuery';
import { useServerList } from './useServerList';
import { parseInstant } from '../../../utils/instant';

const serverSorted = () => '';
const anyRow = () => true;
const PLANS = ['FREE', 'STANDARD', 'ENTERPRISE'];
const planName = (tier: string) => tier.charAt(0).toUpperCase() + tier.slice(1).toLowerCase();

export const ORG_FILTERS: FilterDef<ConsoleOrg>[] = [
  { id: 'tier', label: 'Plan', matches: anyRow, options: PLANS.map((value) => ({ value, label: planName(value) })) },
  {
    id: 'ownerStatus', label: 'Owner', matches: anyRow,
    options: [{ value: 'active', label: 'Active' }, { value: 'suspended', label: 'Suspended' }, { value: 'disabled', label: 'Disabled' }],
  },
];

export function orgColumns(): Column<ConsoleOrg>[] {
  return [
    {
      id: 'name', header: 'Organization', locked: true, sortValue: serverSorted, text: (org) => org.name,
      cell: (org) => (<div><div className="font-medium">{org.name}</div><div className="font-mono text-xs text-text-tertiary">{org.slug}</div></div>),
    },
    {
      id: 'owner', header: 'Owner', sortValue: serverSorted,
      cell: (org) => (<div><div>{org.ownerEmail}</div><AccountStatusBadge status={org.ownerStatus} /></div>),
    },
    { id: 'plan', header: 'Plan', sortValue: serverSorted, cell: (org) => planName(org.tier) },
    { id: 'projects', header: 'Projects', sortValue: serverSorted, cell: (org) => org.projectCount },
    { id: 'apps', header: 'Apps running', sortValue: serverSorted, cell: (org) => org.runningApps },
    {
      id: 'usage', header: 'Usage',
      cell: (org) => (org.cpuCores || org.memoryMb ? `${org.cpuCores.toFixed(2)} cores · ${Math.round(org.memoryMb)} MiB` : '—'),
    },
    { id: 'created', header: 'Created', sortValue: serverSorted, defaultHidden: true, cell: (org) => (org.createdAt ? new Date(parseInstant(org.createdAt)).toLocaleDateString() : '—') },
  ];
}

// The render props live outside the component so they are not rebuilt as new components on every render.
function orgBulkActions(offer: (selected: ConsoleOrg[], clear: () => void) => void) {
  return (selected: ConsoleOrg[], clear: () => void) => (
    <Button size="sm" variant="danger" onClick={() => offer(selected, clear)}>Revoke…</Button>
  );
}

function orgRowActions(canMutate: boolean, on: { changePlan: (org: ConsoleOrg) => void; revoke: (org: ConsoleOrg) => void }) {
  return (org: ConsoleOrg) => (
    <span className="inline-flex gap-2">
      <Button size="sm" variant="secondary" disabled={!canMutate} onClick={() => on.changePlan(org)}>Change plan</Button>
      <Button size="sm" variant="danger" disabled={!canMutate} onClick={() => on.revoke(org)}>
        <ShieldAlert className="w-3.5 h-3.5 mr-1" aria-hidden="true" />Revoke
      </Button>
    </span>
  );
}

// Every organization, paged and searched on the server.
export function OrgsAdminTable({ canMutate }: { readonly canMutate: boolean }) {
  const list = useServerList(ORG_SORTS);
  const { data, isLoading, error, refetch } = useConsoleOrgs({
    q: list.params.q as string | undefined,
    sort: list.params.sort as string | undefined,
    order: list.params.order as 'asc' | 'desc' | undefined,
    tier: list.params.tier as string | undefined,
    ownerStatus: list.params.ownerStatus as string | undefined,
    includeSuspended: true,
    limit: list.params.limit as number,
    offset: list.params.offset as number,
  });
  const client = useQueryClient();
  const revoke = useRevokeOrg();
  const setTier = useSetOrgTier();
  const [planFor, setPlanFor] = useState<ConsoleOrg | null>(null);
  const [chosenTier, setChosenTier] = useState('FREE');
  const [revoking, setRevoking] = useState<ConsoleOrg | null>(null);
  const [bulk, setBulk] = useState<BulkRequest<ConsoleOrg> | null>(null);
  const [outcome, setOutcome] = useState<BulkOutcome | null>(null);
  const columns = useMemo(orgColumns, []);

  const confirmPlan = async (reason: string) => {
    if (!planFor) return;
    try {
      await setTier.mutateAsync({ orgId: planFor.id, tier: chosenTier, reason });
      setPlanFor(null);
    } catch {
      // the dialog stays open and shows setTier.error
    }
  };
  const confirmRevoke = async (reason: string) => {
    if (!revoking) return;
    try {
      await revoke.mutateAsync({ orgId: revoking.id, reason });
      setRevoking(null);
    } catch {
      // the dialog stays open and shows revoke.error
    }
  };
  const offerBulkRevoke = (items: ConsoleOrg[], clear: () => void) => {
    if (items.length === 0) return;
    const projects = items.reduce((sum, org) => sum + org.projectCount, 0);
    setBulk({
      title: `Revoke ${items.length} organization${items.length === 1 ? '' : 's'}`,
      description: `Drops ${projects} project(s), deletes the organizations and suspends their owners so they cannot sign in again. Not reversible.`,
      confirmLabel: 'Revoke', danger: true, items, act: (org, reason) => revokeOrgCascade(org.id, reason),
      label: (org) => org.slug, verb: 'Revoked', noun: 'organizations',
      onSettled: () => { clear(); refreshAdmin(client); },
    });
  };

  return (
    <div data-testid="orgs-table" className="space-y-3">
      <p className="text-xs text-text-tertiary">Revoking cascades through every project in the organization. Every action asks for a reason that goes to the audit log.</p>
      {outcome && <BulkOutcomeNotice outcome={outcome} onDismiss={() => setOutcome(null)} />}
      <DataTable
        label="Organizations"
        columns={columns}
        rows={data?.items ?? []}
        getRowId={(org) => org.id}
        isLoading={isLoading}
        error={error}
        onRetry={refetch}
        filters={ORG_FILTERS}
        paging={list.paging(data?.total ?? 0)}
        storageKey="admin-orgs"
        selectable={canMutate}
        bulkActions={orgBulkActions(offerBulkRevoke)}
        rowActions={orgRowActions(canMutate, {
          changePlan: (org) => { setTier.reset(); setChosenTier(org.tier.toUpperCase()); setPlanFor(org); },
          revoke: (org) => { revoke.reset(); setRevoking(org); },
        })}
        emptyState="No organizations yet."
      />
      {planFor && (
        <ReasonDialog
          title={`Change plan for "${planFor.slug}"`}
          description={`Currently ${planFor.tier.toUpperCase()}. A plan that needs more capacity than the platform has is refused.`}
          confirmLabel="Change plan"
          busy={setTier.isPending}
          error={setTier.error ? serverErrorMessage(setTier.error, 'The plan change failed') : undefined}
          onConfirm={confirmPlan}
          onCancel={() => setPlanFor(null)}
        >
          <label className="block text-sm text-text-secondary">
            <span>Plan</span>
            <select className="mt-1 w-full rounded-lg bg-bg-secondary border border-border-primary p-2 text-text-primary" value={chosenTier} onChange={(event) => setChosenTier(event.target.value)}>
              {PLANS.map((tier) => <option key={tier} value={tier}>{tier}</option>)}
            </select>
          </label>
        </ReasonDialog>
      )}
      {revoking && (
        <ReasonDialog
          title={`Revoke org "${revoking.slug}"`}
          description={`Drops ${revoking.projectCount} project(s), deletes the org, and suspends its owner (${revoking.ownerEmail}) so they cannot sign in again. Not reversible.`}
          confirmLabel="Revoke"
          danger
          busy={revoke.isPending}
          error={revoke.error ? serverErrorMessage(revoke.error, 'Revoke failed') : undefined}
          onConfirm={confirmRevoke}
          onCancel={() => setRevoking(null)}
        />
      )}
      {bulk && <BulkDialog request={bulk} onFinished={(done) => { setBulk(null); setOutcome(done); }} onCancel={() => setBulk(null)} />}
    </div>
  );
}

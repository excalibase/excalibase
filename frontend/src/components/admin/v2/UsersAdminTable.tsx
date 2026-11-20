import { useMemo, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { DataTable, type Column, type FilterDef } from '../../ui/DataTable';
import { Button } from '../../Button';
import { serverErrorMessage } from '../../../utils/serverError';
import { useAccountAction, useConsoleUsers } from '../../../hooks/useAdminConsole';
import { suspendUser, unsuspendUser, type ConsoleUser, type SuspendResult } from '../../../api/adminConsole';
import { AccountStatusBadge } from '../AccountStatusBadge';
import { ReasonDialog } from '../ReasonDialog';
import { BulkDialog, BulkOutcomeNotice, type BulkOutcome, type BulkRequest } from './BulkAction';
import { refreshAdmin, suspendableUsers } from './adminRows';
import { USER_SORTS } from './listQuery';
import { useServerList } from './useServerList';

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : '—');
// Sorting happens on the server; a column only needs a value to show its sort button.
const serverSorted = () => '';
const anyRow = () => true;

export const USER_FILTERS: FilterDef<ConsoleUser>[] = [
  {
    id: 'status', label: 'Status', matches: anyRow,
    options: [{ value: 'active', label: 'Active' }, { value: 'suspended', label: 'Suspended' }, { value: 'disabled', label: 'Disabled' }],
  },
  {
    id: 'role', label: 'Role', matches: anyRow,
    options: [{ value: 'user', label: 'Customer' }, { value: 'platform_admin', label: 'Platform admin' }, { value: 'platform_operator', label: 'Platform operator' }, { value: 'platform_viewer', label: 'Platform viewer' }],
  },
];

export function userColumns(): Column<ConsoleUser>[] {
  return [
    {
      id: 'email', header: 'Email', locked: true, sortValue: serverSorted, text: (user) => user.email,
      cell: (user) => (
        <div>
          <div className="font-medium">{user.email}</div>
          <div className="flex gap-2 text-xs text-text-tertiary">
            {user.tempMail && <span className="text-amber-400">temp mail</span>}
            {!user.emailVerified && <span>unverified</span>}
            {user.role !== 'user' && <span>{user.role}</span>}
          </div>
        </div>
      ),
    },
    { id: 'created', header: 'Signed up', sortValue: serverSorted, cell: (user) => when(user.signedUpAt) },
    {
      id: 'lastLogin', header: 'Last sign-in', sortValue: serverSorted,
      cell: (user) => (<div><div>{when(user.lastLoginAt)}</div><div className="font-mono text-xs text-text-tertiary">{user.lastLoginIp ?? ''}</div></div>),
    },
    {
      id: 'status', header: 'Status',
      cell: (user) => (
        <div>
          <AccountStatusBadge status={user.status} />
          {user.suspendedReason && <div className="mt-1 max-w-[16rem] text-xs text-text-tertiary">{user.suspendedReason}</div>}
        </div>
      ),
    },
    { id: 'orgs', header: 'Orgs', sortValue: serverSorted, cell: (user) => user.orgCount },
    { id: 'projects', header: 'Projects', sortValue: serverSorted, cell: (user) => user.projectCount },
    { id: 'apps', header: 'Apps running', sortValue: serverSorted, cell: (user) => user.runningApps },
  ];
}

type UserAction = 'suspend' | 'unsuspend';
type Pending = { user: ConsoleUser; action: 'suspend' | 'unsuspend' } | null;

// Every account on the platform, paged and searched on the server.
export function UsersAdminTable() {
  const list = useServerList(USER_SORTS);
  const { data, isLoading, error, refetch } = useConsoleUsers({
    q: list.params.q as string | undefined,
    sort: list.params.sort as string | undefined,
    order: list.params.order as 'asc' | 'desc' | undefined,
    status: list.params.status as string | undefined,
    role: list.params.role as string | undefined,
    includeSuspended: true,
    limit: list.params.limit as number,
    offset: list.params.offset as number,
  });
  const client = useQueryClient();
  const suspend = useAccountAction('suspend');
  const unsuspend = useAccountAction('unsuspend');
  const [pending, setPending] = useState<Pending>(null);
  const [bulk, setBulk] = useState<BulkRequest<ConsoleUser> | null>(null);
  const [outcome, setOutcome] = useState<BulkOutcome | null>(null);
  const [result, setResult] = useState<SuspendResult | null>(null);
  const columns = useMemo(userColumns, []);
  const mutation = pending?.action === 'unsuspend' ? unsuspend : suspend;

  const confirm = async (reason: string) => {
    if (!pending) return;
    const done = await mutation.mutateAsync({ userId: pending.user.id, reason }).catch(() => null);
    if (done) {
      setResult(done);
      setPending(null);
    }
  };

  const offerBulk = (action: UserAction, selected: ConsoleUser[], clear: () => void) => {
    const items = suspendableUsers(selected).filter((user) => (action === 'suspend' ? user.status !== 'suspended' : user.status === 'suspended'));
    if (items.length === 0) return;
    const word = action === 'suspend' ? 'Suspend' : 'Unsuspend';
    setBulk({
      title: `${word} ${items.length} account${items.length === 1 ? '' : 's'}`,
      description: action === 'suspend' ? 'The accounts are blocked at once and their apps are scaled to zero.' : 'The accounts are reactivated and the apps the suspension stopped are resumed.',
      confirmLabel: word, danger: action === 'suspend', items,
      act: (user, reason) => (action === 'suspend' ? suspendUser : unsuspendUser)(user.id, reason),
      label: (user) => user.email, verb: action === 'suspend' ? 'Suspended' : 'Unsuspended', noun: 'accounts',
      onSettled: () => { clear(); refreshAdmin(client); },
    });
  };

  return (
    <div data-testid="users-table" className="space-y-3">
      <p className="text-xs text-text-tertiary">Suspending blocks the account and stops its apps. Every action asks for a reason that goes to the audit log.</p>
      {outcome && <BulkOutcomeNotice outcome={outcome} onDismiss={() => setOutcome(null)} />}
      {result && <ResultNotice result={result} onDismiss={() => setResult(null)} />}
      <DataTable
        label="Users"
        columns={columns}
        rows={data?.items ?? []}
        getRowId={(user) => user.id}
        isLoading={isLoading}
        error={error}
        onRetry={refetch}
        filters={USER_FILTERS}
        paging={list.paging(data?.total ?? 0)}
        storageKey="admin-users"
        selectable
        bulkActions={userBulkActions(offerBulk)}
        rowActions={userRowActions((user, action) => { mutation.reset(); setPending({ user, action }); })}
        emptyState="No users yet."
      />
      {pending && (
        <ReasonDialog
          title={`${pending.action === 'suspend' ? 'Suspend' : 'Unsuspend'} ${pending.user.email}`}
          description={pending.action === 'suspend' ? 'The account is blocked at once and its apps are scaled to zero.' : 'The account is reactivated and the apps the suspension stopped are resumed.'}
          confirmLabel={pending.action === 'suspend' ? 'Suspend' : 'Unsuspend'}
          danger={pending.action === 'suspend'}
          busy={mutation.isPending}
          error={mutation.error ? serverErrorMessage(mutation.error, 'The request failed') : undefined}
          onConfirm={confirm}
          onCancel={() => setPending(null)}
        />
      )}
      {bulk && <BulkDialog request={bulk} onFinished={(done) => { setBulk(null); setOutcome(done); }} onCancel={() => setBulk(null)} />}
    </div>
  );
}

// The render props live outside the component so they are not rebuilt as new components on every render.
function userBulkActions(offer: (action: UserAction, selected: ConsoleUser[], clear: () => void) => void) {
  return (selected: ConsoleUser[], clear: () => void) => (
    <>
      <Button size="sm" variant="danger" onClick={() => offer('suspend', selected, clear)}>Suspend…</Button>
      <Button size="sm" variant="secondary" onClick={() => offer('unsuspend', selected, clear)}>Unsuspend…</Button>
    </>
  );
}

function userRowActions(act: (user: ConsoleUser, action: UserAction) => void) {
  return (user: ConsoleUser) => {
    if (user.role.startsWith('platform_')) return null;
    const suspended = user.status === 'suspended';
    return (
      <Button size="sm" variant={suspended ? 'secondary' : 'danger'} onClick={() => act(user, suspended ? 'unsuspend' : 'suspend')}>
        {suspended ? 'Unsuspend' : 'Suspend'}
      </Button>
    );
  };
}

function ResultNotice({ result, onDismiss }: { readonly result: SuspendResult; readonly onDismiss: () => void }) {
  const failed = result.appsFailed ?? [];
  return (
    <output className={`flex justify-between rounded-lg border border-border-primary px-3 py-2 text-sm ${failed.length ? 'text-amber-400' : 'text-green-400'}`}>
      <span>
        Account is now {result.status}; {(result.appsStopped ?? []).length} app(s) stopped, {(result.appsRestored ?? []).length} restored.
        {failed.map((app) => ` ${app.appName}: ${app.reason}.`)}
      </span>
      <button type="button" className="text-text-tertiary" onClick={onDismiss}>Dismiss</button>
    </output>
  );
}

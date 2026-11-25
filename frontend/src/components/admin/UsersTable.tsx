import { useState } from 'react';
import { Loader2, Search } from 'lucide-react';
import { Button } from '../Button';
import { serverErrorMessage } from '../../utils/serverError';
import { useAccountAction, useConsoleUsers } from '../../hooks/useAdminConsole';
import type { ConsoleUser, SuspendResult } from '../../api/adminConsole';
import { AccountStatusBadge } from './AccountStatusBadge';
import { SuspendReasonCode, type SuspendReasonCodeValue } from './SuspendReasonCode';
import { ReasonDialog } from './ReasonDialog';
import { useDebounced } from './useDebounced';
import { formatInstant } from '../../utils/instant';

type Pending = { user: ConsoleUser; action: 'suspend' | 'unsuspend' } | null;

const when = (iso?: string) => (iso ? formatInstant(iso) : '—');

// Every account on the platform: who it is, whether it looks throwaway, where
// it last signed in from, what it owns and runs, and the one action on abuse.
export function UsersTable() {
  const [search, setSearch] = useState('');
  const [showSuspended, setShowSuspended] = useState(false);
  const [pending, setPending] = useState<Pending>(null);
  const [outcome, setOutcome] = useState<SuspendResult | null>(null);
  const query = useDebounced(search, 250);
  const { data, isLoading, error } = useConsoleUsers({ q: query, includeSuspended: showSuspended, limit: 50 });
  const suspend = useAccountAction('suspend');
  const unsuspend = useAccountAction('unsuspend');
  const [reasonCode, setReasonCode] = useState<SuspendReasonCodeValue>('other');
  const mutation = pending?.action === 'unsuspend' ? unsuspend : suspend;

  const confirm = async (reason: string) => {
    if (!pending) return;
    const result = await mutation.mutateAsync({ userId: pending.user.id, reason, reasonCode: pending.action === 'suspend' ? reasonCode : undefined }).catch(() => null);
    if (result) {
      setOutcome(result);
      setPending(null);
    }
  };

  return (
    <div data-testid="users-table" className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
      <div className="px-6 py-4 border-b border-border-primary flex flex-wrap items-center gap-3 justify-between">
        <div>
          <h2 className="text-lg font-semibold">Users</h2>
          <p className="text-xs text-text-tertiary mt-0.5">Suspending blocks the account and stops its apps.</p>
        </div>
        <div className="flex items-center gap-4">
          <label className="flex items-center gap-1.5 text-sm text-text-secondary">
            <input type="checkbox" checked={showSuspended} onChange={(event) => setShowSuspended(event.target.checked)} />
            Show suspended
          </label>
          <div className="relative">
            <Search className="w-4 h-4 absolute left-2 top-2.5 text-text-tertiary" />
            <input
              className="pl-8 pr-2 py-2 text-sm rounded-lg bg-bg-secondary border border-border-primary"
              placeholder="Search email, domain or IP"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
          <span className="text-sm text-text-secondary">{data?.total ?? 0}</span>
        </div>
      </div>
      {outcome && <Outcome result={outcome} onDismiss={() => setOutcome(null)} />}
      {isLoading && (
        <div className="flex justify-center py-10">
          <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
        </div>
      )}
      {error && <p role="alert" className="px-6 py-4 text-sm text-red-400">{serverErrorMessage(error, 'The users could not be read')}</p>}
      {data && data.items.length === 0 && <p className="text-center py-10 text-text-secondary">No users match.</p>}
      {data && data.items.length > 0 && (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary">
              <th className="text-left px-4 py-3 font-medium">Email</th>
              <th className="text-left px-4 py-3 font-medium">Signed up</th>
              <th className="text-left px-4 py-3 font-medium">Last sign-in</th>
              <th className="text-left px-4 py-3 font-medium">Status</th>
              <th className="text-right px-4 py-3 font-medium">Orgs</th>
              <th className="text-right px-4 py-3 font-medium">Projects</th>
              <th className="text-right px-4 py-3 font-medium">Apps running</th>
              <th className="px-4 py-3" />
            </tr>
          </thead>
          <tbody>
            {data.items.map((user) => (
              <UserRow key={user.id} user={user} onAct={(action) => { mutation.reset(); setPending({ user, action }); }} />
            ))}
          </tbody>
        </table>
      )}
      {pending && (
        <ReasonDialog
          title={`${pending.action === 'suspend' ? 'Suspend' : 'Unsuspend'} ${pending.user.email}`}
          description={
            pending.action === 'suspend'
              ? 'The account is blocked at once and its apps are scaled to zero.'
              : 'The account is reactivated and the apps the suspension stopped are resumed.'
          }
          confirmLabel={pending.action === 'suspend' ? 'Suspend' : 'Unsuspend'}
          danger={pending.action === 'suspend'}
          busy={mutation.isPending}
          error={mutation.error ? serverErrorMessage(mutation.error, 'The request failed') : undefined}
          onConfirm={confirm}
          onCancel={() => setPending(null)}
        >
          {pending.action === 'suspend' && <SuspendReasonCode value={reasonCode} onChange={setReasonCode} />}
        </ReasonDialog>
      )}
    </div>
  );
}

function UserRow({ user, onAct }: { readonly user: ConsoleUser; readonly onAct: (action: 'suspend' | 'unsuspend') => void }) {
  const isSuspended = user.status === 'suspended';
  return (
    <tr className="border-b border-border-primary last:border-b-0 hover:bg-bg-hover align-top">
      <td className="px-4 py-3">
        <div className="font-medium">{user.email}</div>
        <div className="flex gap-2 text-xs text-text-tertiary">
          {user.tempMail && <span className="text-amber-400">temp mail</span>}
          {!user.emailVerified && <span>unverified</span>}
          {user.role !== 'user' && <span>{user.role}</span>}
        </div>
      </td>
      <td className="px-4 py-3 text-text-secondary">{when(user.signedUpAt)}</td>
      <td className="px-4 py-3 text-text-secondary">
        <div>{when(user.lastLoginAt)}</div>
        <div className="font-mono text-xs">{user.lastLoginIp ?? ''}</div>
      </td>
      <td className="px-4 py-3">
        <AccountStatusBadge status={user.status} />
        {user.suspendedReason && <div className="text-xs text-text-tertiary mt-1 max-w-[16rem]">{user.suspendedReason}</div>}
      </td>
      <td className="px-4 py-3 text-right text-text-secondary">{user.orgCount}</td>
      <td className="px-4 py-3 text-right text-text-secondary">{user.projectCount}</td>
      <td className="px-4 py-3 text-right text-text-secondary">{user.runningApps}</td>
      <td className="px-4 py-3 text-right">
        {user.role.startsWith('platform_') ? null : (
          <Button size="sm" variant={isSuspended ? 'secondary' : 'danger'} onClick={() => onAct(isSuspended ? 'unsuspend' : 'suspend')}>
            {isSuspended ? 'Unsuspend' : 'Suspend'}
          </Button>
        )}
      </td>
    </tr>
  );
}

function Outcome({ result, onDismiss }: { readonly result: SuspendResult; readonly onDismiss: () => void }) {
  const failed = result.appsFailed ?? [];
  const tone = failed.length ? 'text-amber-400' : 'text-green-400';
  return (
    <div role="status" className={`px-6 py-3 text-sm border-b border-border-primary flex justify-between ${tone}`}>
      <span>
        Account is now {result.status}; {(result.appsStopped ?? []).length} app(s) stopped, {(result.appsRestored ?? []).length} restored.
        {failed.map((app) => ` ${app.appName}: ${app.reason}.`)}
      </span>
      <button className="text-text-tertiary" onClick={onDismiss}>
        Dismiss
      </button>
    </div>
  );
}

import { useState } from 'react';
import { Link, Navigate, useParams } from 'react-router-dom';
import { ChevronLeft, Loader2 } from 'lucide-react';
import { Button } from '../components/Button';
import { ReasonDialog } from '../components/admin/ReasonDialog';
import { SuspendReasonCode, type SuspendReasonCodeValue } from '../components/admin/SuspendReasonCode';
import { AccountStatusBadge } from '../components/admin/AccountStatusBadge';
import { OrgsCard, ProfileCard, ProjectsCard, RecentAuditCard, SignInCard, SuspensionCard, TrustCard } from '../components/admin/user/UserDetailSections';
import { useAccountAction, useUserDetail } from '../hooks/useAdminConsole';
import type { SuspendResult } from '../api/adminConsole';
import { serverErrorMessage } from '../utils/serverError';
import { useAuthStore } from '../stores/auth-store';

const BACK = '/admin?tab=users';

function BackLink() {
  return (
    <Link to={BACK} className="inline-flex items-center gap-1 text-sm text-text-secondary hover:text-text-primary">
      <ChevronLeft className="h-4 w-4" aria-hidden="true" />Back to users
    </Link>
  );
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

function AccountAction({ suspended, onOpen }: { readonly suspended: boolean; readonly onOpen: (action: 'suspend' | 'unsuspend') => void }) {
  if (suspended) return <Button size="sm" variant="secondary" onClick={() => onOpen('unsuspend')}>Unsuspend account</Button>;
  return <Button size="sm" variant="danger" onClick={() => onOpen('suspend')}>Suspend account</Button>;
}

// One account for the platform admin: who they are, how far they are trusted,
// what they own, why they were suspended, and what was done to them. The two
// actions are the console's own endpoints, and each asks for a written reason
// in a confirmation before anything is sent.
export function AdminUserDetailPage() {
  const role = useAuthStore((state) => state.user?.role);
  // Only a platform admin reads accounts; the server answers anyone else as if the page did not exist.
  if (role !== 'platform_admin') return <Navigate to="/" replace />;
  return <AccountDetail />;
}

function AccountDetail() {
  const { userId = '' } = useParams();
  const { data, isLoading, error } = useUserDetail(userId);
  const suspend = useAccountAction('suspend');
  const unsuspend = useAccountAction('unsuspend');
  const [confirming, setConfirming] = useState<'suspend' | 'unsuspend' | null>(null);
  const [reasonCode, setReasonCode] = useState<SuspendReasonCodeValue>('other');
  const [result, setResult] = useState<SuspendResult | null>(null);
  const mutation = confirming === 'unsuspend' ? unsuspend : suspend;

  if (isLoading) return <div className="flex justify-center p-12"><Loader2 className="h-5 w-5 animate-spin text-accent-primary" aria-label="Loading" /></div>;
  if (error || !data) {
    const missing = (error as { response?: { status?: number } } | null)?.response?.status === 404;
    return (
      <div className="mx-auto max-w-5xl space-y-3">
        <BackLink />
        <p role="alert" className="text-sm text-red-400">{missing ? 'That account was not found.' : serverErrorMessage(error, 'The account could not be read')}</p>
      </div>
    );
  }

  const { profile, suspension } = data;
  const isStaff = profile.role.startsWith('platform_');
  const open = (action: 'suspend' | 'unsuspend') => { mutation.reset(); setConfirming(action); };
  const confirm = async (reason: string) => {
    if (!confirming) return;
    const done = await mutation.mutateAsync({ userId: profile.id, reason, reasonCode: confirming === 'suspend' ? reasonCode : undefined }).catch(() => null);
    if (done) {
      setResult(done);
      setConfirming(null);
    }
  };
  const actions = isStaff ? null : <AccountAction suspended={suspension.suspended} onOpen={open} />;
  const word = confirming === 'unsuspend' ? 'Unsuspend' : 'Suspend';

  return (
    <div className="mx-auto max-w-5xl space-y-4" data-testid="admin-user-detail">
      <BackLink />
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="min-w-0 break-words text-xl font-semibold text-text-primary">{profile.email}</h1>
        <AccountStatusBadge status={profile.status} />
      </div>
      {result && <ResultNotice result={result} onDismiss={() => setResult(null)} />}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <ProfileCard profile={profile} />
        <TrustCard detail={data} />
        <div className="lg:col-span-2"><SuspensionCard detail={data} actions={actions} /></div>
        <OrgsCard orgs={data.orgs} />
        <ProjectsCard projects={data.projects} />
        <SignInCard methods={data.signIn} />
        <RecentAuditCard rows={data.audit} userId={profile.id} />
      </div>
      {confirming && (
        <ReasonDialog
          title={`${word} ${profile.email}`}
          description={confirming === 'suspend' ? 'The account is blocked at once and its apps are scaled to zero.' : 'The account is reactivated and the apps the suspension stopped are resumed.'}
          confirmLabel={word}
          danger={confirming === 'suspend'}
          busy={mutation.isPending}
          error={mutation.error ? serverErrorMessage(mutation.error, 'The request failed') : undefined}
          onConfirm={confirm}
          onCancel={() => setConfirming(null)}
        >
          {confirming === 'suspend' && <SuspendReasonCode value={reasonCode} onChange={setReasonCode} />}
        </ReasonDialog>
      )}
    </div>
  );
}

import { Loader2, ShieldCheck } from 'lucide-react';
import { Link, useSearchParams } from 'react-router-dom';
import { trustFailure, useStartGitHubVerify, useTrust, type TrustStatus } from '../../api/trust';
import { useFeatureEnabled } from '../../hooks/useDeploymentMode';
import { Button } from '../Button';

// What the return from GitHub says, before the status reloads: the server sends the browser back to /?verify=<outcome>.
const RETURNED: Record<string, string> = {
  cancelled: 'GitHub sign-in was cancelled. Nothing changed.',
  state: 'That verification link expired. Start again.',
  too_soon: 'Verification was tried a moment ago. Wait a few minutes and try again.',
  error: 'Something went wrong on our side. Try again in a few minutes.',
  off: 'Verification is not available right now.',
  held: 'An administrator has limited this account. Contact support.',
};

function rule(policy: TrustStatus['policy']): string {
  return `Your GitHub account must be at least ${policy.minAgeDays} days old and show activity: ${policy.minPublicEvents} public events in the last ${policy.activityWindowDays} days, or ${policy.minRepos} public repositories, one older than ${policy.repoMinAgeDays} days.`;
}

function blocker(status: TrustStatus): string | null {
  if (!status.emailVerified) return 'Verify your email address first.';
  if (!status.githubConfigured) return 'GitHub sign-in is not set up on this platform, so accounts cannot be verified here.';
  return null;
}

function Result({ status, returned }: { readonly status: TrustStatus; readonly returned: string | null }) {
  const attempt = status.lastAttempt;
  const note = returned ? RETURNED[returned] : undefined;
  return (
    <>
      {note && <p role="status" className="text-text-secondary">{note}</p>}
      {attempt && !attempt.passed && attempt.missing.length > 0 && (
        <div role="alert" className="space-y-1 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-amber-300">
          <p className="font-medium">Not verified yet</p>
          <ul className="list-disc pl-5">{attempt.missing.map((line) => <li key={line}>{line}</li>)}</ul>
        </div>
      )}
    </>
  );
}

// "Verify your account": shown to an account that is limited, on Home and where a container request was refused.
// It renders nothing while the trial is off, while loading, and once the account is verified.
export function VerifyAccountCard() {
  const trialOn = useFeatureEnabled('limitedtrial').enabled;
  const { data: status } = useTrust(trialOn);
  const start = useStartGitHubVerify();
  const [params] = useSearchParams();
  if (!status || status.state === 'full') return null;
  const blocked = blocker(status);
  const waiting = (status.retryAfterSeconds ?? 0) > 0;
  return (
    <section aria-label="Verify your account" data-testid="verify-account-card" className="space-y-3 rounded-xl border border-border-primary bg-surface-card p-4 text-sm">
      <h2 className="flex items-center gap-2 text-[15px] font-semibold text-text-primary">
        <ShieldCheck className="h-4 w-4" aria-hidden="true" /> Verify your account
      </h2>
      <p className="text-text-secondary">
        Running containers needs a verified account. Your databases are not affected. {rule(status.policy)}
      </p>
      <Result status={status} returned={params.get('verify')} />
      {blocked && <p className="text-text-secondary">{blocked} {!status.emailVerified && <Link to="/account" className="text-purple-400 hover:underline">Open your account</Link>}</p>}
      <div className="space-y-2">
        <Button type="button" onClick={() => start.mutate()} disabled={Boolean(blocked) || waiting || start.isPending}>
          {start.isPending && <Loader2 className="mr-1 inline h-4 w-4 animate-spin" aria-hidden="true" />}
          Verify with GitHub
        </Button>
        {waiting && <p className="text-text-secondary">You can try again in a few minutes.</p>}
        {start.error && <p role="alert" className="text-red-400">{trustFailure(start.error)}</p>}
      </div>
    </section>
  );
}

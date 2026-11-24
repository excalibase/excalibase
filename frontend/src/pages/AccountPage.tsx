import { useState } from 'react';
import { Link } from 'react-router-dom';
import { KeyRound, Loader2 } from 'lucide-react';
import { mailFailure, sendPasswordReset, sendVerificationEmail, useMe } from '../api/account';
import { Button } from '../components/Button';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { useAuthStore } from '../stores/auth-store';
import { Line, Section } from './usage/UsageParts';

interface MailAction {
  readonly run: () => Promise<string>;
  readonly failure: string;
}

// One button that sends a mail and then says what happened; failures stay on screen as an alert.
function MailButton({ label, action }: { readonly label: string; readonly action: MailAction }) {
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const click = async () => {
    setBusy(true);
    setError(null);
    try {
      setDone(await action.run());
    } catch (err) {
      setDone(null);
      setError(mailFailure(err, action.failure));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-2">
      <Button type="button" onClick={click} disabled={busy}>
        {busy && <Loader2 className="w-4 h-4 animate-spin" aria-hidden="true" />} {label}
      </Button>
      {done && <output className="block text-text-secondary">{done}</output>}
      {error && <p role="alert" className="text-red-400">{error}</p>}
    </div>
  );
}

const verifyAction: MailAction = {
  failure: 'Could not send the verification email. Try again later.',
  run: async () => {
    await sendVerificationEmail();
    return 'The verification email is on its way.';
  },
};

const resetAction = (email: string): MailAction => ({
  failure: 'Could not send the reset link. Try again later.',
  run: async () => {
    const { expiresInMinutes } = await sendPasswordReset(email);
    const expiry = typeof expiresInMinutes === 'number' ? ` It expires in ${expiresInMinutes} minutes.` : '';
    return `If the address can receive mail, a reset link is on its way.${expiry}`;
  },
});

function verification(verifiedAt: string | undefined, known: boolean): string | null {
  if (!known) return null;
  return verifiedAt ? 'Verified' : 'Not verified';
}

// The person's own account (studio_v2). Passwords are never typed here: changing one goes through the
// emailed reset link, the same flow as "Forgot password", so no password reaches this page.
export function AccountPage() {
  useDocumentTitle('Account');
  const stored = useAuthStore((s) => s.user);
  const me = useMe();
  const profile = me.data ?? stored;
  const status = verification(me.data?.emailVerifiedAt, me.data !== undefined);
  return (
    <div className="mx-auto max-w-3xl space-y-4" data-testid="account-page">
      <h1 className="text-xl font-semibold text-text-primary">Account</h1>
      {profile && (
        <Section title="Profile">
          <Line label="Username" value={profile.username} />
          <Line label="Email" value={profile.email} />
          {status && <Line label="Email status" value={status} />}
          <Line label="Role" value={profile.role} />
          {status === 'Not verified' && <MailButton label="Send verification email" action={verifyAction} />}
        </Section>
      )}
      {profile && (
        <Section title="Password">
          <p className="text-text-secondary">We email a link to {profile.email}. Open it to choose a new password.</p>
          <MailButton label="Email me a reset link" action={resetAction(profile.email)} />
        </Section>
      )}
      <Section title="Access keys">
        <p className="text-text-secondary">Keys for the CLI, CI and coding tools. Each acts with your own role.</p>
        <Link to="/account/tokens" className="inline-flex items-center gap-2 text-purple-400 hover:underline">
          <KeyRound className="w-4 h-4" aria-hidden="true" /> Manage access keys
        </Link>
      </Section>
    </div>
  );
}

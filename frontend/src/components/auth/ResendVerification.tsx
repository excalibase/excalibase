import { useState } from 'react';
import { api } from '../../api/client';
import { Button } from '../Button';
import { serverErrorMessage } from '../../utils/serverError';

const inputClass =
  'w-full px-3 py-2.5 bg-bg-secondary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500 focus:border-transparent transition-colors';

// Mails a fresh verification link. The server answers the same for every
// address, so this never tells anyone whether an account exists.
// `email` is an address the page already knows (no field shown); `initialEmail`
// is a likely one the user can still change.
export function ResendVerification({ email: knownEmail, initialEmail }: { readonly email?: string; readonly initialEmail?: string }) {
  const [email, setEmail] = useState(knownEmail ?? initialEmail ?? '');
  const [state, setState] = useState<'idle' | 'sending' | 'sent' | 'failed'>('idle');
  const [failure, setFailure] = useState('');

  const resend = async () => {
    if (!email.trim()) return;
    setState('sending');
    try {
      await api.post('/email/verify/resend', { email: email.trim() });
      setState('sent');
    } catch (err) {
      setFailure(serverErrorMessage(err, 'Could not send a new link. Try again later.'));
      setState('failed');
    }
  };

  return (
    <div className="space-y-3">
      {!knownEmail && (
        <div>
          <label htmlFor="resend-email" className="block text-sm font-medium text-text-secondary mb-1.5">Your email</label>
          <input id="resend-email" type="email" autoComplete="email" value={email}
            onChange={(e) => setEmail(e.target.value)} className={inputClass} placeholder="you@company.com" />
        </div>
      )}
      <Button type="button" onClick={resend} disabled={state === 'sending' || !email.trim()} className="w-full">
        Send a new link
      </Button>
      {state === 'sent' && <p className="text-sm text-text-secondary">If that address has an unverified account, a new link is on its way.</p>}
      {state === 'failed' && <p role="alert" className="text-sm text-red-400" data-testid="resend-error">{failure}</p>}
    </div>
  );
}

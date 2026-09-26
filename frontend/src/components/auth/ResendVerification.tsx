import { useState } from 'react';
import { api } from '../../api/client';
import { Button } from '../Button';

const inputClass =
  'w-full px-3 py-2.5 bg-bg-secondary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500 focus:border-transparent transition-colors';

// Mails a fresh verification link. The server answers the same for every
// address, so this never tells anyone whether an account exists.
export function ResendVerification({ email: knownEmail }: { readonly email?: string }) {
  const [email, setEmail] = useState(knownEmail ?? '');
  const [state, setState] = useState<'idle' | 'sending' | 'sent' | 'failed'>('idle');

  const resend = async () => {
    if (!email.trim()) return;
    setState('sending');
    try {
      await api.post('/email/verify/resend', { email: email.trim() });
      setState('sent');
    } catch {
      setState('failed');
    }
  };

  return (
    <div className="space-y-3">
      {!knownEmail && (
        <div>
          <label htmlFor="resend-email" className="block text-sm font-medium text-text-secondary mb-1.5">Your email</label>
          <input id="resend-email" type="email" autoComplete="email" value={email}
            onChange={(e) => setEmail(e.target.value)} className={inputClass} placeholder="john@company.com" />
        </div>
      )}
      <Button type="button" onClick={resend} disabled={state === 'sending' || !email.trim()} className="w-full">
        Send a new link
      </Button>
      {state === 'sent' && <p className="text-sm text-text-secondary">If that address has an unverified account, a new link is on its way.</p>}
      {state === 'failed' && <p className="text-sm text-red-400">Could not send a new link. Try again later.</p>}
    </div>
  );
}

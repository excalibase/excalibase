import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { api } from '../api/client';
import { ResendVerification } from '../components/auth/ResendVerification';

type VerifyState = { kind: 'checking' } | { kind: 'verified' } | { kind: 'failed'; message: string };

export function VerifyEmailPage() {
  const [searchParams] = useSearchParams();
  const token = searchParams.get('token') ?? '';
  const [state, setState] = useState<VerifyState>(
    token ? { kind: 'checking' } : { kind: 'failed', message: 'This verification link is incomplete.' },
  );

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api.post('/email/verify/confirm', { token })
      .then(() => { if (!cancelled) setState({ kind: 'verified' }); })
      .catch((err: unknown) => {
        const axiosErr = err as { response?: { data?: { error?: string } } };
        if (!cancelled) setState({ kind: 'failed', message: axiosErr.response?.data?.error || 'Could not verify your email.' });
      });
    return () => { cancelled = true; };
  }, [token]);

  if (state.kind === 'checking') {
    return (
      <div className="flex items-center gap-2 text-text-secondary text-sm">
        <Loader2 className="w-4 h-4 animate-spin" /> Verifying your email…
      </div>
    );
  }
  if (state.kind === 'verified') {
    return (
      <div data-testid="email-verified" className="space-y-4">
        <h2 className="text-lg font-semibold text-text-primary">Email verified</h2>
        <p className="text-sm text-text-secondary">Your account is ready.</p>
        <Link to="/login" className="text-purple-400 hover:text-purple-300 transition-colors text-sm">Sign in</Link>
      </div>
    );
  }
  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold text-text-primary">Could not verify your email</h2>
      <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">{state.message}</div>
      <ResendVerification />
    </div>
  );
}

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { api } from '../api/client';
import { Button } from '../components/Button';
import { useDocumentTitle } from '../hooks/useDocumentTitle';

interface ResetSendResponse {
  expiresInMinutes?: number;
}

function sendFailure(err: unknown): string {
  const status = (err as { response?: { status?: number } }).response?.status;
  if (status === 429) return 'Too many reset requests. Wait a while and try again.';
  return 'Could not send the reset link. Try again later.';
}

// Asks for a reset link. The server answers the same for every address, so
// the page never says whether an account exists.
export function ForgotPasswordPage() {
  useDocumentTitle('Forgot password');
  const [email, setEmail] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState<ResetSendResponse | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const address = email.trim();
    if (!address) return;
    setError(null);
    setLoading(true);
    try {
      const { data } = await api.post<ResetSendResponse>('/email/reset/send', { email: address });
      setSent(data ?? {});
    } catch (err: unknown) {
      setError(sendFailure(err));
    } finally {
      setLoading(false);
    }
  };

  const backToSignIn = (
    <p className="text-center text-sm text-text-secondary">
      <Link to="/login" className="text-purple-400 hover:text-purple-300 transition-colors">Back to sign in</Link>
    </p>
  );

  if (sent) {
    const minutes = sent.expiresInMinutes;
    return (
      <div className="space-y-5">
        <h2 className="text-lg font-semibold text-text-primary">Check your email</h2>
        <p data-testid="reset-link-sent" className="text-sm text-text-secondary">
          If an account exists for that address, we've sent a link.
          {typeof minutes === 'number' && ` It expires in ${minutes} minutes.`}
        </p>
        {backToSignIn}
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      <div>
        <h2 className="text-lg font-semibold text-text-primary mb-1">Forgot password</h2>
        <p className="text-sm text-text-secondary">Enter your account's email and we'll send you a link to choose a new password.</p>
      </div>
      {error && (
        <div role="alert" className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">
          {error}
        </div>
      )}
      <div>
        <label htmlFor="email" className="block text-sm font-medium text-text-secondary mb-1.5">Email</label>
        <input id="email" type="email" autoComplete="email" value={email}
          onChange={(e) => setEmail(e.target.value)} disabled={loading} placeholder="you@company.com"
          className="w-full px-3 py-2.5 bg-bg-secondary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500 focus:border-transparent transition-colors" />
      </div>
      <Button type="submit" disabled={loading || !email.trim()} className="w-full flex items-center justify-center gap-2">
        {loading && <Loader2 className="w-4 h-4 animate-spin" />}
        Send reset link
      </Button>
      {backToSignIn}
    </form>
  );
}

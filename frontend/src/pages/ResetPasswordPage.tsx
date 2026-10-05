import { useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { api } from '../api/client';
import { Button } from '../components/Button';

export function ResetPasswordPage() {
  const [searchParams] = useSearchParams();
  const token = searchParams.get('token') ?? '';
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(token ? null : 'This reset link is incomplete.');
  const [loading, setLoading] = useState(false);
  const [revokedTokens, setRevokedTokens] = useState<number | null>(null);
  const [username, setUsername] = useState('');

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !password) return;
    setError(null);
    setLoading(true);
    try {
      const { data } = await api.post<{ accessTokensRevoked?: number; username?: string }>('/email/reset/confirm', { token, newPassword: password });
      setUsername(data?.username ?? '');
      setRevokedTokens(data?.accessTokensRevoked ?? 0);
    } catch (err: unknown) {
      const axiosErr = err as { response?: { data?: { error?: string } } };
      setError(axiosErr.response?.data?.error || 'Could not reset your password.');
    } finally {
      setLoading(false);
    }
  };

  if (revokedTokens !== null) {
    return (
      <div data-testid="password-reset" className="space-y-4">
        <h2 className="text-lg font-semibold text-text-primary">Password updated</h2>
        <p data-testid="tokens-revoked" className="text-sm text-text-secondary">
          Every session on this account was signed out.
          {revokedTokens > 0 && ` ${revokedTokens} personal access ${revokedTokens === 1 ? 'token was' : 'tokens were'} revoked; create new ones for your scripts and CI.`}
        </p>
        {username && (
          <p data-testid="sign-in-as" className="text-sm text-text-secondary">
            Sign in as <span className="font-medium text-text-primary">{username}</span> or with your e-mail.
          </p>
        )}
        <Link to="/login" className="text-purple-400 hover:text-purple-300 transition-colors text-sm">Sign in</Link>
      </div>
    );
  }
  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      <h2 className="text-lg font-semibold text-text-primary">Choose a new password</h2>
      {error && <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">{error}</div>}
      <div>
        <label htmlFor="new-password" className="block text-sm font-medium text-text-secondary mb-1.5">New password</label>
        <input id="new-password" type="password" autoComplete="new-password" value={password}
          onChange={(e) => setPassword(e.target.value)} disabled={loading || !token}
          className="w-full px-3 py-2.5 bg-bg-secondary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500 focus:border-transparent transition-colors" />
      </div>
      <Button type="submit" disabled={loading || !token || !password} className="w-full flex items-center justify-center gap-2">
        {loading && <Loader2 className="w-4 h-4 animate-spin" />}
        Set password
      </Button>
    </form>
  );
}

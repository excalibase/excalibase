import { useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { api } from '../api/client';
import { Button } from '../components/Button';
import { NewPasswordFields, newPasswordReady } from '../components/auth/NewPasswordFields';

export function ResetPasswordPage() {
  const [searchParams] = useSearchParams();
  const token = searchParams.get('token') ?? '';
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(token ? null : 'This reset link is incomplete.');
  const [loading, setLoading] = useState(false);
  const [revokedTokens, setRevokedTokens] = useState<number | null>(null);
  const [username, setUsername] = useState('');

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !newPasswordReady(password, confirm)) return;
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
      <NewPasswordFields id="new-password" label="New password" password={password} confirm={confirm}
        onPasswordChange={setPassword} onConfirmChange={setConfirm} disabled={loading || !token} />
      <Button type="submit" disabled={loading || !token || !newPasswordReady(password, confirm)} className="w-full flex items-center justify-center gap-2">
        {loading && <Loader2 className="w-4 h-4 animate-spin" />}
        Set password
      </Button>
    </form>
  );
}

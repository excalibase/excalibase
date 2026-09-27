import { useState } from 'react';
import { useNavigate, Link, useSearchParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { api } from '../api/client';
import { useAuthStore, type AuthUser } from '../stores/auth-store';
import { Button } from '../components/Button';
import { ResendVerification } from '../components/auth/ResendVerification';
import { ProviderButtons, ProviderRefusal } from '../components/auth/ProviderSignIn';

interface LoginResponse {
  user: AuthUser;
}

export function LoginPage() {
  const navigate = useNavigate();
  const setAuth = useAuthStore((s) => s.setAuth);
  const [searchParams] = useSearchParams();
  const inviteToken = searchParams.get('invite') ?? '';
  const oauthError = searchParams.get('oauth_error');

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [unverified, setUnverified] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setUnverified(false);

    if (!username.trim() || !password.trim()) {
      setError('Username and password are required');
      return;
    }

    setLoading(true);
    try {
      const response = await api.post<LoginResponse>('/auth/login', {
        username: username.trim(),
        password,
      });
      // The server set the httpOnly session cookie on this response; only the
      // profile is kept here.
      setAuth(response.data.user);
    } catch (err: unknown) {
      const axiosErr = err as { response?: { status?: number; data?: { error?: string; code?: string } } };
      if (axiosErr.response?.data?.code === 'email_not_verified') {
        setUnverified(true);
      } else if (axiosErr.response?.status === 401) {
        setError('Invalid username or password');
      } else {
        setError(axiosErr.response?.data?.error || 'Login failed. Please try again.');
      }
      setLoading(false);
      return;
    }
    if (!inviteToken) {
      setLoading(false);
      navigate('/', { replace: true });
      return;
    }
    try {
      await api.post('/orgs/invites/accept', { token: inviteToken });
      navigate('/orgs', { replace: true });
    } catch (err: unknown) {
      const axiosErr = err as { response?: { data?: { error?: string } } };
      setError(axiosErr.response?.data?.error || 'Could not accept the invite');
    } finally {
      setLoading(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      <div>
        <h2 className="text-lg font-semibold text-text-primary mb-1">Sign in</h2>
        <p className="text-sm text-text-secondary">Enter your credentials to continue</p>
      </div>

      <ProviderRefusal reason={oauthError} />

      {unverified && (
        <div data-testid="email-not-verified" className="space-y-3 px-4 py-3 rounded-lg bg-amber-500/10 border border-amber-500/30 text-sm text-amber-300">
          <p>Confirm your email address before signing in. Use the link we sent you, or ask for a new one.</p>
          <ResendVerification />
        </div>
      )}

      {error && (
        <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">
          {error}
        </div>
      )}

      <div className="space-y-4">
        <div>
          <label htmlFor="username" className="block text-sm font-medium text-text-secondary mb-1.5">
            Username
          </label>
          <input
            id="username"
            type="text"
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="w-full px-3 py-2.5 bg-bg-secondary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500 focus:border-transparent transition-colors"
            placeholder="Enter username"
            disabled={loading}
          />
        </div>

        <div>
          <label htmlFor="password" className="block text-sm font-medium text-text-secondary mb-1.5">
            Password
          </label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full px-3 py-2.5 bg-bg-secondary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500 focus:border-transparent transition-colors"
            placeholder="Enter password"
            disabled={loading}
          />
        </div>
      </div>

      <Button
        type="submit"
        disabled={loading}
        className="w-full flex items-center justify-center gap-2"
      >
        {loading && <Loader2 className="w-4 h-4 animate-spin" />}
        {loading ? 'Signing in...' : 'Sign in'}
      </Button>

      <ProviderButtons invite={inviteToken || undefined} />

      <p className="text-center text-sm text-text-secondary">
        Don't have an account?{' '}
        <Link to={inviteToken ? `/register?invite=${encodeURIComponent(inviteToken)}` : '/register'} className="text-purple-400 hover:text-purple-300 transition-colors">Register</Link>
      </p>
    </form>
  );
}

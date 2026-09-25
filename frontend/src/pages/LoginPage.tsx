import { useState } from 'react';
import { useNavigate, Link, useSearchParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { api } from '../api/client';
import { useAuthStore, type AuthUser } from '../stores/auth-store';
import { Button } from '../components/Button';

interface LoginResponse {
  token: string;
  user: AuthUser;
}

export function LoginPage() {
  const navigate = useNavigate();
  const setAuth = useAuthStore((s) => s.setAuth);
  const [searchParams] = useSearchParams();
  const inviteToken = searchParams.get('invite') ?? '';

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

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
      // Server sets the httpOnly excali_session cookie on this response;
      // we just persist the user profile for UI bootstrapping. Pass the
      // raw token as legacyToken to keep the axios header fallback alive
      // for callers that don't yet honour the cookie.
      setAuth(response.data.user, { legacyToken: response.data.token });
    } catch (err: unknown) {
      const axiosErr = err as { response?: { status?: number; data?: { error?: string } } };
      if (axiosErr.response?.status === 401) {
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

      <p className="text-center text-sm text-text-secondary">
        Don't have an account?{' '}
        <Link to={inviteToken ? `/register?invite=${encodeURIComponent(inviteToken)}` : '/register'} className="text-purple-400 hover:text-purple-300 transition-colors">Register</Link>
      </p>
    </form>
  );
}

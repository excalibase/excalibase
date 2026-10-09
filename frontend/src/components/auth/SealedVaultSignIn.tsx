import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import { api } from '../../api/client';
import { useAuthStore, type AuthUser } from '../../stores/auth-store';
import { serverErrorMessage } from '../../utils/serverError';

const INPUT_CLASS =
  'mt-1 w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500';

/**
 * Sign-in shown inside the unseal step (EXC-579). A sealed vault keeps every
 * Studio page behind /setup, and unsealing needs a platform admin session, so
 * an admin whose session ended signs in here rather than on /login.
 */
export function SealedVaultSignIn() {
  const setAuth = useAuthStore((s) => s.setAuth);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const { data } = await api.post<{ user: AuthUser }>('/auth/login', {
        username: username.trim(),
        password,
      });
      setAuth(data.user);
    } catch (err) {
      setError(serverErrorMessage(err, 'Sign-in failed'));
    } finally {
      setLoading(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)} data-testid="vault-unseal-signin">
      <p className="mb-3 text-xs text-text-secondary">
        Sign in as a platform admin to unseal the vault.
      </p>
      <label className="block mb-3">
        <span className="text-xs font-medium text-text-secondary">Username or e-mail</span>
        <input
          type="text"
          autoComplete="username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          className={INPUT_CLASS}
          data-testid="vault-unseal-signin-username"
          required
        />
      </label>
      <label className="block mb-3">
        <span className="text-xs font-medium text-text-secondary">Password</span>
        <input
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className={INPUT_CLASS}
          data-testid="vault-unseal-signin-password"
          required
        />
      </label>
      {error && (
        <p role="alert" className="mb-3 text-xs text-red-400">
          {error}
        </p>
      )}
      <button
        type="submit"
        disabled={loading || !username.trim() || !password}
        className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors flex items-center justify-center gap-2"
        data-testid="vault-unseal-signin-submit"
      >
        {loading && <Loader2 className="w-4 h-4 animate-spin" />}
        Sign in
      </button>
    </form>
  );
}

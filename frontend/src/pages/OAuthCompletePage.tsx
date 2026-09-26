import { useEffect } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { api } from '../api/client';
import { useAuthStore, type AuthUser } from '../stores/auth-store';

// The provider sign-in ends here with the session cookie already set; the
// account is read back through it.
export function OAuthCompletePage() {
  const navigate = useNavigate();
  const setAuth = useAuthStore((s) => s.setAuth);
  const [searchParams] = useSearchParams();
  const toOrgs = searchParams.has('joined') || searchParams.has('invite_error');

  useEffect(() => {
    let cancelled = false;
    api.get<AuthUser>('/auth/me')
      .then((res) => {
        if (cancelled) return;
        setAuth(res.data);
        navigate(toOrgs ? '/orgs' : '/', { replace: true });
      })
      .catch(() => { if (!cancelled) navigate('/login?oauth_error=failed', { replace: true }); });
    return () => { cancelled = true; };
  }, [navigate, setAuth, toOrgs]);

  return (
    <div className="flex items-center gap-2 text-text-secondary text-sm">
      <Loader2 className="w-4 h-4 animate-spin" /> Signing you in…
    </div>
  );
}

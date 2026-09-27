import { useEffect, useState } from 'react';
import { api } from '../../api/client';
import { API_BASE } from '../../api/base';

const labels: Record<string, string> = { google: 'Google', github: 'GitHub' };

const refusals: Record<string, string> = {
  cancelled: 'Sign-in was cancelled at the provider.',
  state: 'That sign-in link expired or came from another browser. Please try again.',
  email_not_verified: 'The provider has not verified that email address, so it cannot sign you in.',
  account_conflict: 'That email address belongs to an account that cannot sign in this way.',
  setup_required: 'This platform has no administrator yet. Finish setup first.',
  invite_only: 'Sign-up here needs an invite. Use the link from your invite.',
  invite_invalid: 'That invite is invalid, expired, or was issued to a different email address.',
  unavailable: 'That sign-in provider is not available.',
  failed: 'Sign-in failed. Please try again.',
};

// ProviderRefusal explains why a Google or GitHub sign-in came back refused.
export function ProviderRefusal({ reason }: { readonly reason: string | null }) {
  if (!reason) return null;
  return (
    <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">
      {refusals[reason] ?? refusals.failed}
    </div>
  );
}

// ProviderButtons shows one link per provider the platform has configured.
// The whole exchange happens on the server; the browser only follows links.
export function ProviderButtons({ invite }: { readonly invite?: string }) {
  const [providers, setProviders] = useState<string[]>([]);

  useEffect(() => {
    let cancelled = false;
    api.get<{ providers: string[] }>('/auth/oauth/providers')
      .then((res) => { if (!cancelled) setProviders(res.data.providers ?? []); })
      .catch(() => { if (!cancelled) setProviders([]); });
    return () => { cancelled = true; };
  }, []);

  if (providers.length === 0) return null;
  const query = invite ? `?${new URLSearchParams({ invite }).toString()}` : '';
  return (
    <div className="space-y-2">
      {providers.map((provider) => (
        <a key={provider} href={`${API_BASE}/auth/oauth/${provider}/start${query}`}
          className="block w-full text-center px-3 py-2.5 rounded-lg border border-border-primary text-text-primary hover:bg-bg-secondary transition-colors text-sm">
          Continue with {labels[provider] ?? provider}
        </a>
      ))}
    </div>
  );
}

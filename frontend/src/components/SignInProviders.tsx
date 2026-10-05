import { useEffect, useState } from 'react';
import { api } from '../api/client';
import { Button } from './Button';

interface ProviderSettings {
  provider: string;
  enabled: boolean;
  clientId: string;
  clientSecretSet: boolean;
  callbackUrl: string;
}

const labels: Record<string, string> = { google: 'Google', github: 'GitHub' };

const clientIdExamples: Record<string, string> = {
  google: '1234567890-abc123.apps.googleusercontent.com',
  github: 'Ov23liAbCdEf12345678',
};

const inputClass = 'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm';

function ProviderForm({ initial }: { readonly initial: ProviderSettings }) {
  const [settings, setSettings] = useState(initial);
  const [enabled, setEnabled] = useState(initial.enabled);
  const [clientId, setClientId] = useState(initial.clientId);
  const [secret, setSecret] = useState('');
  const [state, setState] = useState<{ saving: boolean; error: string | null; saved: boolean }>({ saving: false, error: null, saved: false });
  const name = labels[settings.provider] ?? settings.provider;

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setState({ saving: true, error: null, saved: false });
    const body: Record<string, unknown> = { enabled, clientId: clientId.trim() };
    if (secret) body.clientSecret = secret;
    try {
      const res = await api.put<ProviderSettings>(`/admin/sso-providers/${settings.provider}`, body);
      if (res.data) setSettings(res.data);
      setSecret('');
      setState({ saving: false, error: null, saved: true });
    } catch (err) {
      const axiosErr = err as { response?: { data?: { error?: string } } };
      setState({ saving: false, error: axiosErr.response?.data?.error || 'Could not save the provider', saved: false });
    }
  };

  const prefix = `sso-${settings.provider}`;
  return (
    <form data-testid={prefix} onSubmit={save} className="space-y-3 p-4 rounded-lg border border-border-primary">
      <div className="flex items-center justify-between">
        <h3 className="font-medium text-text-primary">{name}</h3>
        <label htmlFor={`${prefix}-enabled`} className="flex items-center gap-2 text-sm text-text-secondary">
          <input id={`${prefix}-enabled`} type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled</span>
        </label>
      </div>
      <div className="text-xs text-text-secondary">
        Callback URL to register at {name}: <code className="text-text-primary break-all">{settings.callbackUrl}</code>
      </div>
      <div>
        <label htmlFor={`${prefix}-client-id`} className="block text-sm text-text-secondary mb-1">Client ID</label>
        <input id={`${prefix}-client-id`} value={clientId} onChange={(e) => setClientId(e.target.value)} className={inputClass} autoComplete="off"
          placeholder={clientIdExamples[settings.provider]} />
      </div>
      <div>
        <label htmlFor={`${prefix}-secret`} className="block text-sm text-text-secondary mb-1">Client secret</label>
        <input id={`${prefix}-secret`} type="password" value={secret} onChange={(e) => setSecret(e.target.value)}
          className={inputClass} autoComplete="new-password"
          placeholder={settings.clientSecretSet ? 'Leave empty to keep the current secret' : `From the OAuth app at ${name}`} />
        <p className="text-xs text-text-secondary mt-1">
          {settings.clientSecretSet ? 'A secret is set. It is never shown again.' : 'No secret set.'}
        </p>
      </div>
      {state.error && <p className="text-sm text-red-400">{state.error}</p>}
      {state.saved && <p className="text-sm text-text-secondary">Saved. It applies to the next sign-in.</p>}
      <Button type="submit" disabled={state.saving}>Save</Button>
    </form>
  );
}

// SignInProviders lets a platform admin configure Studio's Google and GitHub
// sign-in. The client secret is write-only.
export function SignInProviders() {
  const [providers, setProviders] = useState<ProviderSettings[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.get<{ providers: ProviderSettings[] }>('/admin/sso-providers/')
      .then((res) => { if (!cancelled) setProviders(res.data.providers); })
      .catch(() => { if (!cancelled) setError('Could not load the sign-in providers'); });
    return () => { cancelled = true; };
  }, []);

  return (
    <section className="space-y-3">
      <h2 className="text-lg font-semibold">Sign-in providers</h2>
      <p className="text-sm text-text-secondary">
        Let developers sign in to Studio with Google or GitHub. Create an OAuth app at the provider with the callback URL shown, then paste its client ID and secret here.
      </p>
      {error && <p className="text-sm text-red-400">{error}</p>}
      <div className="grid gap-4 md:grid-cols-2">
        {providers?.map((p) => <ProviderForm key={p.provider} initial={p} />)}
      </div>
    </section>
  );
}
